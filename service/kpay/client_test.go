package kpay

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testClient(t *testing.T) (*Client, *rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	merchant, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	platform, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	privateDER, err := x509.MarshalPKCS8PrivateKey(merchant)
	require.NoError(t, err)
	publicDER, err := x509.MarshalPKIXPublicKey(&platform.PublicKey)
	require.NoError(t, err)
	return &Client{BaseURL: GatewaySandbox, MerchantCode: "merchant-001", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})), PlatformPublicKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}))}, merchant, platform
}

// The fixture deliberately constructs the documented lines without the production signer.
func fixtureSignature(t *testing.T, key *rsa.PrivateKey, method, path string, headers http.Header, body []byte) string {
	t.Helper()
	text := method + "\n" + path + "\n" + headers.Get("K-Timestamp") + "\n" + headers.Get("K-Nonce-Str") + "\n" + headers.Get("K-Merchant-Code") + "\n"
	if appID := headers.Get("K-App-Id"); appID != "" {
		text += appID + "\n"
	}
	text += string(body) + "\n"
	hash := sha256.Sum256([]byte(text))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(signature)
}

func verifyRequest(t *testing.T, key *rsa.PrivateKey, request *http.Request, body []byte) {
	t.Helper()
	assert.Len(t, request.Header.Get("K-Nonce-Str"), 32)
	assert.NotEmpty(t, request.Header.Get("K-Timestamp"))
	expected := fixtureSignature(t, key, request.Method, request.URL.RequestURI(), request.Header, body)
	assert.Equal(t, expected, request.Header.Get("K-Signature"))
}

func TestCreateOrderSignsExactNumericBody(t *testing.T) {
	client, merchant, _ := testClient(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		verifyRequest(t, merchant, r, body)
		assert.Equal(t, "/v1/managed/order/add", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		assert.Empty(t, r.Header.Get("K-App-Id"))
		assert.Contains(t, string(body), `"localPayAmount":10.25`)
		assert.Contains(t, string(body), `"price":10.25`)
		assert.NotContains(t, string(body), `"localPayAmount":"`)
		assert.Contains(t, string(body), `"orderRemark":"\u003c充值\u003e\u0026"`)
		_, _ = io.WriteString(w, `{"code":10000,"data":{"managedOrderNo":"k-order-1"}}`)
	}))
	defer server.Close()
	client.BaseURL = server.URL
	result, err := client.CreateOrder(context.Background(), ManagedOrderRequest{ManagedOutTradeNo: "local-1", LocalPayAmount: decimal.RequireFromString("10.25"), LocalPayCurrency: "HKD", NotifyURL: "https://merchant.test/notify", ReturnURL: "https://merchant.test/return", OrderRemark: "<充值>&", ItemList: []Item{{ItemName: "充值", Price: decimal.RequireFromString("10.25"), PriceCurrency: "HKD", Quantity: 1}}})
	require.NoError(t, err)
	assert.Equal(t, "k-order-1", result.ManagedOrderNo)
}

func TestQueryOrderSignsEncodedQueryInServiceProviderMode(t *testing.T) {
	client, merchant, _ := testClient(t)
	client.AppID = "app-123"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyRequest(t, merchant, r, nil)
		assert.Equal(t, "/v1/managed/order/result?managedOrderNo=k%2Border&managedOutTradeNo=local+order", r.URL.RequestURI())
		assert.Equal(t, "app-123", r.Header.Get("K-App-Id"))
		_, _ = io.WriteString(w, `{"code":10000,"data":{"merchantCode":"merchant-001","managedOutTradeNo":"local order","managedOrderNo":"k+order","managedOrderState":2,"localPayAmount":10.25,"localPayCurrency":"HKD","paymentOrderList":[{"outTradeNo":"trade-1","orderNo":"pay-1","result":2,"orderState":2}]}}`)
	}))
	defer server.Close()
	client.BaseURL = server.URL
	result, err := client.QueryOrder(context.Background(), "k+order", "local order")
	require.NoError(t, err)
	assert.Equal(t, 2, result.ManagedOrderState)
	assert.True(t, result.LocalPayAmount.Equal(decimal.RequireFromString("10.25")))
	require.Len(t, result.PaymentOrderList, 1)
	assert.Equal(t, "pay-1", result.PaymentOrderList[0].OrderNo)
}

func TestCheckoutURLSignsAllParametersExceptSignature(t *testing.T) {
	client, merchant, _ := testClient(t)
	client.AppID = "app-123"
	for _, mode := range []string{"web", "h5"} {
		t.Run(mode, func(t *testing.T) {
			checkout, err := client.CheckoutURL("order+1", "zh_HK", mode)
			require.NoError(t, err)
			u, err := url.Parse(checkout)
			require.NoError(t, err)
			assert.Equal(t, "/v1/"+mode+"/managed/order", u.Path)
			params := u.Query()
			assert.Equal(t, "order+1", params.Get("managedOrderNo"))
			assert.Equal(t, "zh_HK", params.Get("language"))
			signature := params.Get("K-Signature")
			params.Del("K-Signature")
			headers := http.Header{}
			for _, name := range []string{"K-Merchant-Code", "K-App-Id", "K-Nonce-Str", "K-Timestamp"} {
				headers.Set(name, params.Get(name))
			}
			assert.Equal(t, fixtureSignature(t, merchant, "GET", u.Path+"?"+params.Encode(), headers, nil), signature)
		})
	}
	_, err := client.CheckoutURL("", "zh_HK", "web")
	assert.Error(t, err)
	_, err = client.CheckoutURL("order", "zh_HK", "other")
	assert.Error(t, err)
}

func TestNotificationVerifiesRawBodyAndConfiguredIdentity(t *testing.T) {
	client, _, platform := testClient(t)
	client.AppID = "app-123"
	path := "/api/kpay/notify"
	body := []byte(`{ "eventType":"PAYMENT", "merchantCode":"merchant-001", "managedMerchantOrderNo":"local-1", "managedOrderNo":"k-1", "appId":"app-123", "transactionState":2, "localPayAmount":10.25, "localPayCurrency":"HKD" }`)
	headers := http.Header{}
	headers.Set("K-Merchant-Code", client.MerchantCode)
	headers.Set("K-App-Id", client.AppID)
	headers.Set("K-Timestamp", "1755515905744")
	headers.Set("K-Nonce-Str", "12345678901234567890123456789012")
	headers.Set("K-Signature", fixtureSignature(t, platform, "POST", path, headers, body))
	require.NoError(t, client.VerifyNotification("POST", path, headers, body))
	assert.Error(t, client.VerifyNotification("POST", path, headers, append(body, ' ')))
	assert.Error(t, client.VerifyNotification("POST", path+"?x=1", headers, body))
	for _, identity := range []string{"K-Merchant-Code", "K-App-Id"} {
		t.Run(identity, func(t *testing.T) {
			changed := headers.Clone()
			changed.Set(identity, "different")
			changed.Set("K-Signature", fixtureSignature(t, platform, "POST", path, changed, body))
			assert.Error(t, client.VerifyNotification("POST", path, changed, body))
		})
	}
	var notification Notification
	require.NoError(t, common.Unmarshal(body, &notification))
	assert.Equal(t, "local-1", notification.ManagedMerchantOrderNo)
	assert.Equal(t, 2, notification.TransactionState)
}

func TestRejectsGatewayFailures(t *testing.T) {
	client, _, _ := testClient(t)
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"http failure", `{"code":10000,"data":{"managedOrderNo":"k-1"}}`, 500},
		{"business failure", `{"code":10001,"message":"declined"}`, 200},
		{"missing data", `{"code":10000}`, 200},
		{"null data", `{"code":10000,"data":null}`, 200},
		{"missing order number", `{"code":10000,"data":{}}`, 200},
		{"malformed", `{bad}`, 200},
		{"trailing garbage", `{"code":10000,"data":{"managedOrderNo":"k-1"}} extra`, 200},
		{"oversized", strings.Repeat(" ", 2<<20), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client.BaseURL = server.URL
			_, err := client.CreateOrder(context.Background(), ManagedOrderRequest{ManagedOutTradeNo: "local-1", LocalPayAmount: decimal.NewFromInt(10), LocalPayCurrency: "HKD"})
			assert.Error(t, err)
		})
	}
}

func TestDoesNotForwardSignedCredentialsAcrossRedirect(t *testing.T) {
	client, _, _ := testClient(t)
	var forwarded atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Store(true)
		_, _ = io.WriteString(w, `{"code":10000,"data":{"managedOrderNo":"k-1"}}`)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client.BaseURL = origin.URL
	client.HTTPClient = origin.Client()
	_, err := client.CreateOrder(context.Background(), ManagedOrderRequest{ManagedOutTradeNo: "local-1", LocalPayAmount: decimal.NewFromInt(10), LocalPayCurrency: "HKD"})
	assert.Error(t, err)
	assert.False(t, forwarded.Load())
}

func TestPaymentDTOsKeepDecimalJSONNumbers(t *testing.T) {
	amount := decimal.RequireFromString("10.25")
	for _, tc := range []struct {
		name   string
		value  any
		fields []string
	}{
		{"notification", Notification{LocalPayAmount: amount, PayAmount: amount}, []string{"localPayAmount", "payAmount"}},
		{"managed result", ManagedOrderResult{LocalPayAmount: amount}, []string{"localPayAmount"}},
		{"payment result", PaymentOrder{LocalPayAmount: amount}, []string{"localPayAmount"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := common.Marshal(tc.value)
			require.NoError(t, err)
			for _, field := range tc.fields {
				assert.Contains(t, string(body), `"`+field+`":10.25`)
			}
		})
	}
}

func TestNotificationRejectsSignedBodyIdentityMismatch(t *testing.T) {
	client, _, platform := testClient(t)
	path := "/api/kpay/notify"
	headers := http.Header{}
	headers.Set("K-Merchant-Code", client.MerchantCode)
	headers.Set("K-Timestamp", "1755515905744")
	headers.Set("K-Nonce-Str", "12345678901234567890123456789012")
	valid := []byte(`{"merchantCode":"merchant-001","transactionState":2}`)
	headers.Set("K-Signature", fixtureSignature(t, platform, "POST", path, headers, valid))
	require.NoError(t, client.VerifyNotification("POST", path, headers, valid))
	for _, body := range []string{`{"merchantCode":"other","transactionState":2}`, `{"merchantCode":"merchant-001","appId":"unexpected","transactionState":2}`} {
		headers.Set("K-Signature", fixtureSignature(t, platform, "POST", path, headers, []byte(body)))
		assert.Error(t, client.VerifyNotification("POST", path, headers, []byte(body)))
	}
	for _, name := range []string{"K-Timestamp", "K-Nonce-Str", "K-Signature"} {
		changed := headers.Clone()
		changed.Del(name)
		assert.Error(t, client.VerifyNotification("POST", path, changed, valid))
	}
}

func TestValidateCredentialsAndAmounts(t *testing.T) {
	client, merchant, platform := testClient(t)
	require.NoError(t, client.Validate())
	client.PrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(merchant)}))
	client.PlatformPublicKey = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&platform.PublicKey)}))
	require.NoError(t, client.Validate())
	for _, tc := range []struct {
		name   string
		modify func(*Client)
	}{
		{"merchant", func(c *Client) { c.MerchantCode = "" }},
		{"private", func(c *Client) { c.PrivateKey = "bad" }},
		{"public", func(c *Client) { c.PlatformPublicKey = "bad" }},
		{"gateway", func(c *Client) { c.BaseURL = "ftp://bad" }},
	} {
		t.Run(tc.name, func(t *testing.T) { copy := *client; tc.modify(&copy); assert.Error(t, copy.Validate()) })
	}
	for _, tc := range []struct {
		input, expected string
		valid           bool
	}{
		{"10.255", "10.26", true}, {"0.01", "0.01", true}, {"999999999.99", "999999999.99", true},
		{"0", "", false}, {"-1", "", false}, {"0.004", "", false}, {"1000000000", "", false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			amount, err := FormatAmount(decimal.RequireFromString(tc.input))
			if tc.valid {
				require.NoError(t, err)
				assert.Equal(t, tc.expected, amount)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

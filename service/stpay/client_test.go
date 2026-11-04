package stpay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testHashSecret = "fixture-secret"

func fixtureSignature(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func testPaymentRequest() PaymentRequest {
	return PaymentRequest{
		OrderNo:      "order-123",
		Amount:       "100.50",
		CurrencyCode: "RUB",
		PayIP:        "192.0.2.10",
		ReturnURL:    "https://merchant.example/return?a=1&b=2",
		MerMgrURL:    "https://merchant.example/manage",
		MerNotifyURL: "https://merchant.example/notify?a=1&b=2",
		GoodsString:  "credits",
		Email:        "payer@example.com",
	}
}

func testClient(baseURL, cardType string) *Client {
	return &Client{
		BaseURL:    baseURL,
		MerNo:      "merchant-1",
		TerNo:      "terminal-1",
		HashSecret: testHashSecret,
		CardType:   cardType,
	}
}

func TestCreatePaymentSubmitsSignedSBPAndTestMIDRequests(t *testing.T) {
	requestHash := fixtureSignature("EncryptionMode=SHA256&CharacterSet=UTF8&merNo=merchant-1&terNo=terminal-1&orderNo=order-123&currencyCode=RUB&amount=100.50&payIP=192.0.2.10&transType=sales&transModel=M&" + testHashSecret)
	responseHash := fixtureSignature("amount=100.50&currencyCode=RUB&merNo=merchant-1&orderNo=order-123&respCode=003&respMsg=pending_async&terNo=terminal-1&tradeNo=provider-456&transType=sales&" + testHashSecret)

	for _, tc := range []struct {
		name     string
		cardType string
		wantCard string
	}{
		{name: "production SBP default", wantCard: CardTypeSBP},
		{name: "test MID online banking", cardType: CardTypeOnlineBanking, wantCard: CardTypeOnlineBanking},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/api/gotoPayment", r.URL.Path)
				assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
				if !assert.NoError(t, r.ParseForm()) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				assert.Equal(t, url.Values{
					"merNo":        {"merchant-1"},
					"terNo":        {"terminal-1"},
					"CharacterSet": {"UTF8"},
					"transType":    {"sales"},
					"transModel":   {"M"},
					"getPayLink":   {"N"},
					"apiType":      {"1"},
					"amount":       {"100.50"},
					"currencyCode": {"RUB"},
					"orderNo":      {"order-123"},
					"returnURL":    {"https://merchant.example/return?a=1|b=2"},
					"merMgrURL":    {"https://merchant.example/manage"},
					"merNotifyURL": {"https://merchant.example/notify?a=1|b=2"},
					"payIP":        {"192.0.2.10"},
					"goodsString":  {"credits"},
					"grEmail":      {"payer@example.com"},
					"cardType":     {tc.wantCard},
					"hashcode":     {requestHash},
				}, r.PostForm)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"amount":"100.50","currencyCode":"RUB","merNo":"merchant-1","orderNo":"order-123","respCode":"003","respMsg":"pending_async","terNo":"terminal-1","tradeNo":"provider-456","transType":"sales","skipTo3DURL":"https://pay.example/qr","hashcode":"` + responseHash + `"}`))
			}))
			defer server.Close()

			response, err := testClient(server.URL, tc.cardType).CreatePayment(context.Background(), testPaymentRequest())
			require.NoError(t, err)
			require.NotNil(t, response)
			assert.Equal(t, "provider-456", response.TradeNo)
			assert.Equal(t, "https://pay.example/qr", response.SkipTo3DURL)
		})
	}
}

func TestCreatePaymentRejectsProviderAndTransportFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statusCode int
		body       string
		wantError  string
	}{
		{name: "unsigned business error", statusCode: http.StatusOK, body: `{"respCode":"1103","respMsg":"card type unsupported"}`, wantError: "respCode=1103"},
		{name: "tampered pending response", statusCode: http.StatusOK, body: `{"amount":"100.50","currencyCode":"RUB","merNo":"merchant-1","orderNo":"order-123","respCode":"003","respMsg":"pending_async","terNo":"terminal-1","tradeNo":"provider-456","transType":"sales","hashcode":"bad-signature"}`, wantError: "signature mismatch"},
		{name: "malformed JSON", statusCode: http.StatusOK, body: `{`, wantError: "not json"},
		{name: "gateway unavailable", statusCode: http.StatusBadGateway, body: `unavailable`, wantError: "http status 502"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			response, err := testClient(server.URL, CardTypeSBP).CreatePayment(context.Background(), testPaymentRequest())
			require.ErrorContains(t, err, tc.wantError)
			assert.Nil(t, response)
		})
	}
}

func TestCreatePaymentRejectsInvalidGatewayURL(t *testing.T) {
	for _, baseURL := range []string{"ftp://pay.example", "http:///missing-host"} {
		t.Run(baseURL, func(t *testing.T) {
			response, err := testClient(baseURL, CardTypeSBP).CreatePayment(context.Background(), testPaymentRequest())
			require.Error(t, err)
			assert.Nil(t, response)
		})
	}
}

func TestQuerySubmitsTradeNumberAndAcceptsSignedResult(t *testing.T) {
	requestHash := fixtureSignature("merchant-1terminal-1order-123100.50RUB" + testHashSecret)
	responseHash := fixtureSignature("merchant-1terminal-1order-123100.50RUBprovider-45600" + testHashSecret)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/external/query", r.URL.Path)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
		if !assert.NoError(t, r.ParseForm()) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal(t, url.Values{
			"merNo":     {"merchant-1"},
			"terNo":     {"terminal-1"},
			"orderNo":   {"order-123"},
			"tradeNo":   {"provider-456"},
			"amount":    {"100.50"},
			"currency":  {"RUB"},
			"signature": {requestHash},
		}, r.PostForm)
		_, _ = w.Write([]byte(`{"merNo":"merchant-1","terNo":"terminal-1","orderNo":"order-123","tradeNo":"provider-456","amount":"100.50","currency":"RUB","respCode":"00","signature":"` + responseHash + `"}`))
	}))
	defer server.Close()

	response, err := testClient(server.URL, CardTypeSBP).Query(context.Background(), "order-123", "provider-456", "100.50", "RUB")
	require.NoError(t, err)
	require.NotNil(t, response)
	assert.Equal(t, "00", response.RespCode)
	assert.Equal(t, "provider-456", response.TradeNo)
}

func TestQueryHandlesProviderErrorAndRejectsInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		wantError string
	}{
		{name: "provider error without response signature", body: `{"errorCode":"ORDER_NOT_FOUND","errorMsg":"unknown order"}`},
		{name: "tampered success response", body: `{"merNo":"merchant-1","terNo":"terminal-1","orderNo":"order-123","tradeNo":"provider-456","amount":"100.50","currency":"RUB","respCode":"00","signature":"bad-signature"}`, wantError: "signature mismatch"},
		{name: "malformed JSON", body: `{`, wantError: "not json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			response, err := testClient(server.URL, CardTypeSBP).Query(context.Background(), "order-123", "provider-456", "100.50", "RUB")
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				assert.Nil(t, response)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, response)
			assert.Equal(t, "ORDER_NOT_FOUND", response.ErrorCode)
		})
	}
}

func TestQueryRejectsInsecureRemoteGateway(t *testing.T) {
	response, err := testClient("http://payments.example", CardTypeSBP).Query(context.Background(), "order-123", "provider-456", "100.50", "RUB")
	require.ErrorContains(t, err, "HTTPS")
	assert.Nil(t, response)
}

func TestQueryValidatesRedirectsBeforeApplyingCallerPolicy(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		target    string
		wantHTTPS bool
	}{
		{"remote HTTP rejected", "http://payments.example/query", true},
		{"HTTPS respects caller policy", "https://payments.example/query", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				http.Redirect(writer, request, testCase.target, http.StatusTemporaryRedirect)
			}))
			defer server.Close()
			policyError := errors.New("caller redirect policy")
			client := testClient(server.URL, CardTypeSBP)
			client.HTTP = server.Client()
			client.HTTP.CheckRedirect = func(request *http.Request, via []*http.Request) error { return policyError }
			response, err := client.Query(context.Background(), "order-123", "provider-456", "100.50", "RUB")
			assert.Nil(t, response)
			if testCase.wantHTTPS {
				require.ErrorContains(t, err, "HTTPS")
				return
			}
			require.ErrorIs(t, err, policyError)
		})
	}
}

func TestQueryRejectsInvalidGatewayURL(t *testing.T) {
	response, err := testClient("ftp://pay.example", CardTypeSBP).Query(context.Background(), "order-123", "provider-456", "100.50", "RUB")
	require.ErrorContains(t, err, "must use http or https")
	assert.Nil(t, response)
}

package kpay

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

const (
	GatewaySandbox    = "https://payment.uat.kpay-group.com"
	GatewayProduction = "https://payment.kpay-group.com"

	// EventTypeSales 是订单结果通知里「销售」类事件的 eventType 取值。
	//
	// 官方文档对通知报文只有一个未展开的模板引用（kpay-docs/zh-hk/v4/api/managed/
	// 全託管訂單.md 的「通知回調-訂單結果通知」一节），枚举值未在任何已交付文档
	// 里给出，因此这里保留单一常量：改动它或把未知取值当成销售处理都会让
	// 真实付款静默不入账，必须先拿一笔真实回调核对。
	EventTypeSales = "SALES"
)

type Client struct {
	BaseURL, MerchantCode, AppID, PrivateKey, PlatformPublicKey string
	HTTPClient                                                  *http.Client
}

type ManagedOrderRequest struct {
	ManagedOutTradeNo   string          `json:"managedOutTradeNo"`
	LocalPayAmount      decimal.Decimal `json:"localPayAmount"`
	LocalPayCurrency    string          `json:"localPayCurrency"`
	NotifyURL           string          `json:"notifyUrl,omitempty"`
	ReturnURL           string          `json:"returnUrl,omitempty"`
	MerchantIcon        string          `json:"merchantIcon,omitempty"`
	OrderRemark         string          `json:"orderRemark,omitempty"`
	PayMethodOrder      []string        `json:"payMethodOrder,omitempty"`
	PageFeatureControls []string        `json:"pageFeatureControls,omitempty"`
	ItemList            []Item          `json:"itemList,omitempty"`
}

type Item struct {
	ItemNo        string          `json:"itemNo"`
	ItemName      string          `json:"itemName"`
	Price         decimal.Decimal `json:"price"`
	PriceCurrency string          `json:"priceCurrency"`
	Quantity      int             `json:"quantity"`
}

type CreateOrderData struct {
	ManagedOrderNo string `json:"managedOrderNo"`
}

type ManagedOrderResult struct {
	MerchantCode      string          `json:"merchantCode"`
	ManagedOutTradeNo string          `json:"managedOutTradeNo"`
	ManagedOrderNo    string          `json:"managedOrderNo"`
	LocalPayAmount    decimal.Decimal `json:"localPayAmount"`
	LocalPayCurrency  string          `json:"localPayCurrency"`
	ManagedOrderState int             `json:"managedOrderState"`
	PaymentOrderList  []PaymentOrder  `json:"paymentOrderList"`
}

type PaymentOrder struct {
	OutTradeNo       string          `json:"outTradeNo"`
	OrderNo          string          `json:"orderNo"`
	TransactionNo    string          `json:"transactionNo"`
	Result           int             `json:"result"`
	OrderState       int             `json:"orderState"`
	LocalPayAmount   decimal.Decimal `json:"localPayAmount"`
	LocalPayCurrency string          `json:"localPayCurrency"`
}

type Notification struct {
	EventType              string          `json:"eventType"`
	MerchantCode           string          `json:"merchantCode"`
	ManagedMerchantOrderNo string          `json:"managedMerchantOrderNo"`
	ManagedOrderNo         string          `json:"managedOrderNo"`
	OutTradeNo             string          `json:"outTradeNo"`
	OrderNo                string          `json:"orderNo"`
	TransactionNo          string          `json:"transactionNo"`
	AppID                  string          `json:"appId"`
	TransactionState       int             `json:"transactionState"`
	LocalPayAmount         decimal.Decimal `json:"localPayAmount"`
	PayAmount              decimal.Decimal `json:"payAmount"`
	LocalPayCurrency       string          `json:"localPayCurrency"`
	PayCurrency            string          `json:"payCurrency"`
	PayMethodID            int64           `json:"payMethodId"`
}

// MarshalJSON keeps monetary values as JSON numbers without changing decimal's
// global serialization setting, which is shared by the rest of the application.
func (request ManagedOrderRequest) MarshalJSON() ([]byte, error) {
	type requestAlias ManagedOrderRequest
	return common.Marshal(struct {
		requestAlias
		LocalPayAmount json.RawMessage `json:"localPayAmount"`
	}{requestAlias: requestAlias(request), LocalPayAmount: json.RawMessage(request.LocalPayAmount.String())})
}

func (item Item) MarshalJSON() ([]byte, error) {
	type itemAlias Item
	return common.Marshal(struct {
		itemAlias
		Price json.RawMessage `json:"price"`
	}{itemAlias: itemAlias(item), Price: json.RawMessage(item.Price.String())})
}

func (result ManagedOrderResult) MarshalJSON() ([]byte, error) {
	type resultAlias ManagedOrderResult
	return common.Marshal(struct {
		resultAlias
		LocalPayAmount json.RawMessage `json:"localPayAmount"`
	}{resultAlias: resultAlias(result), LocalPayAmount: json.RawMessage(result.LocalPayAmount.String())})
}

func (result PaymentOrder) MarshalJSON() ([]byte, error) {
	type resultAlias PaymentOrder
	return common.Marshal(struct {
		resultAlias
		LocalPayAmount json.RawMessage `json:"localPayAmount"`
	}{resultAlias: resultAlias(result), LocalPayAmount: json.RawMessage(result.LocalPayAmount.String())})
}

func (notification Notification) MarshalJSON() ([]byte, error) {
	type notificationAlias Notification
	return common.Marshal(struct {
		notificationAlias
		LocalPayAmount json.RawMessage `json:"localPayAmount"`
		PayAmount      json.RawMessage `json:"payAmount"`
	}{notificationAlias: notificationAlias(notification), LocalPayAmount: json.RawMessage(notification.LocalPayAmount.String()), PayAmount: json.RawMessage(notification.PayAmount.String())})
}

func (c *Client) Validate() error {
	if strings.TrimSpace(c.MerchantCode) == "" {
		return errors.New("KPay merchant code is required")
	}
	if _, err := c.gatewayURL("/"); err != nil {
		return err
	}
	if _, err := parsePrivateKey(c.PrivateKey); err != nil {
		return err
	}
	_, err := parsePublicKey(c.PlatformPublicKey)
	return err
}

// ValidatePrivateKey checks only the merchant RSA private key.
func (c *Client) ValidatePrivateKey() error {
	_, err := parsePrivateKey(c.PrivateKey)
	return err
}

// ValidatePlatformPublicKey checks only the platform RSA public key.
func (c *Client) ValidatePlatformPublicKey() error {
	_, err := parsePublicKey(c.PlatformPublicKey)
	return err
}

func parsePrivateKey(value string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(value)))
	if block == nil {
		return nil, errors.New("KPay private key must be PEM encoded")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		if err := key.Validate(); err != nil {
			return nil, errors.New("invalid KPay RSA private key")
		}
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid KPay private key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("KPay private key must use RSA")
	}
	if err := key.Validate(); err != nil {
		return nil, errors.New("invalid KPay RSA private key")
	}
	return key, nil
}

func parsePublicKey(value string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(value)))
	if block == nil {
		return nil, errors.New("KPay platform public key must be PEM encoded")
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.New("invalid KPay platform public key")
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("KPay platform public key must use RSA")
	}
	return key, nil
}

func (c *Client) gatewayURL(path string) (*url.URL, error) {
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid KPay gateway URL")
	}
	relative, err := url.Parse(path)
	if err != nil {
		return nil, errors.New("invalid KPay request path")
	}
	return u.ResolveReference(relative), nil
}

func signatureText(method, path string, headers http.Header, body []byte) []byte {
	text := method + "\n" + path + "\n" + headers.Get("K-Timestamp") + "\n" + headers.Get("K-Nonce-Str") + "\n" + headers.Get("K-Merchant-Code") + "\n"
	if appID := headers.Get("K-App-Id"); appID != "" {
		text += appID + "\n"
	}
	return []byte(text + string(body) + "\n")
}

func (c *Client) sign(method, path string, headers http.Header, body []byte) (string, error) {
	key, err := parsePrivateKey(c.PrivateKey)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(signatureText(method, path, headers, body))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("sign KPay request: %w", err)
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func (c *Client) requestHeaders() (http.Header, error) {
	if strings.TrimSpace(c.MerchantCode) == "" {
		return nil, errors.New("KPay merchant code is required")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate KPay nonce: %w", err)
	}
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	headers.Set("K-Merchant-Code", c.MerchantCode)
	headers.Set("K-Nonce-Str", hex.EncodeToString(nonce))
	headers.Set("K-Timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	if c.AppID != "" {
		headers.Set("K-App-Id", c.AppID)
	}
	return headers, nil
}

func (c *Client) callAPI(ctx context.Context, method, path string, body []byte, result any) error {
	u, err := c.gatewayURL(path)
	if err != nil {
		return err
	}
	headers, err := c.requestHeaders()
	if err != nil {
		return err
	}
	signature, err := c.sign(method, u.RequestURI(), headers, body)
	if err != nil {
		return err
	}
	headers.Set("K-Signature", signature)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create KPay request: %w", err)
	}
	request.Header = headers
	client := http.Client{Timeout: 20 * time.Second}
	if c.HTTPClient != nil {
		client = *c.HTTPClient
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("KPay request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("KPay returned HTTP status %d", response.StatusCode)
	}
	const responseLimit = 1 << 20
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		return fmt.Errorf("read KPay response: %w", err)
	}
	if len(responseBody) > responseLimit {
		return errors.New("KPay response exceeds size limit")
	}
	var envelope struct {
		Code    int             `json:"code"`
		Data    json.RawMessage `json:"data"`
		Message string          `json:"message"`
	}
	if err := common.Unmarshal(responseBody, &envelope); err != nil {
		return errors.New("invalid KPay response")
	}
	if envelope.Code != 10000 {
		return fmt.Errorf("KPay rejected request (code %d)", envelope.Code)
	}
	if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) {
		return errors.New("KPay response is missing data")
	}
	if err := common.Unmarshal(envelope.Data, result); err != nil {
		return errors.New("invalid KPay response data")
	}
	return nil
}

func (c *Client) CreateOrder(ctx context.Context, request ManagedOrderRequest) (*CreateOrderData, error) {
	if strings.TrimSpace(request.ManagedOutTradeNo) == "" || len(request.ManagedOutTradeNo) > 32 {
		return nil, errors.New("invalid KPay merchant order number")
	}
	amount, err := FormatAmount(request.LocalPayAmount)
	if err != nil {
		return nil, err
	}
	request.LocalPayAmount, err = decimal.NewFromString(amount)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.LocalPayCurrency) == "" {
		return nil, errors.New("KPay local payment currency is required")
	}
	body, err := common.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal KPay order: %w", err)
	}
	result := new(CreateOrderData)
	if err := c.callAPI(ctx, http.MethodPost, "/v1/managed/order/add", body, result); err != nil {
		return nil, err
	}
	if strings.TrimSpace(result.ManagedOrderNo) == "" {
		return nil, errors.New("KPay response is missing managed order number")
	}
	return result, nil
}

func (c *Client) QueryOrder(ctx context.Context, managedOrderNo, managedOutTradeNo string) (*ManagedOrderResult, error) {
	if managedOrderNo == "" && managedOutTradeNo == "" {
		return nil, errors.New("KPay order number is required")
	}
	params := url.Values{}
	if managedOrderNo != "" {
		params.Set("managedOrderNo", managedOrderNo)
	}
	if managedOutTradeNo != "" {
		params.Set("managedOutTradeNo", managedOutTradeNo)
	}
	result := new(ManagedOrderResult)
	if err := c.callAPI(ctx, http.MethodGet, "/v1/managed/order/result?"+params.Encode(), nil, result); err != nil {
		return nil, err
	}
	if strings.TrimSpace(result.ManagedOrderNo) == "" {
		return nil, errors.New("KPay response is missing managed order number")
	}
	return result, nil
}

func (c *Client) CheckoutURL(managedOrderNo, language, mode string) (string, error) {
	if strings.TrimSpace(managedOrderNo) == "" {
		return "", errors.New("KPay managed order number is required")
	}
	if mode != "web" && mode != "h5" {
		return "", errors.New("invalid KPay checkout mode")
	}
	u, err := c.gatewayURL("/v1/" + mode + "/managed/order")
	if err != nil {
		return "", err
	}
	headers, err := c.requestHeaders()
	if err != nil {
		return "", err
	}
	params := url.Values{"managedOrderNo": {managedOrderNo}}
	if language != "" {
		params.Set("language", language)
	}
	for _, name := range []string{"K-Merchant-Code", "K-App-Id", "K-Nonce-Str", "K-Timestamp"} {
		if value := headers.Get(name); value != "" {
			params.Set(name, value)
		}
	}
	u.RawQuery = params.Encode()
	signature, err := c.sign(http.MethodGet, u.RequestURI(), headers, nil)
	if err != nil {
		return "", err
	}
	// Append the escaped signature so the previously signed query is kept byte-for-byte.
	u.RawQuery += "&K-Signature=" + url.QueryEscape(signature)
	return u.String(), nil
}

func (c *Client) VerifyNotification(method, path string, headers http.Header, rawBody []byte) error {
	if c.MerchantCode == "" || headers.Get("K-Merchant-Code") != c.MerchantCode || headers.Get("K-App-Id") != c.AppID {
		return errors.New("KPay notification identity mismatch")
	}
	if headers.Get("K-Timestamp") == "" || headers.Get("K-Nonce-Str") == "" {
		return errors.New("KPay notification is missing signature headers")
	}
	key, err := parsePublicKey(c.PlatformPublicKey)
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(headers.Get("K-Signature"))
	if err != nil || len(signature) == 0 {
		return errors.New("invalid KPay notification signature")
	}
	hash := sha256.Sum256(signatureText(method, path, headers, rawBody))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], signature); err != nil {
		return errors.New("KPay notification signature verification failed")
	}
	var notification Notification
	if err := common.Unmarshal(rawBody, &notification); err != nil {
		return errors.New("invalid KPay notification body")
	}
	if notification.MerchantCode != c.MerchantCode || notification.AppID != c.AppID {
		return errors.New("KPay notification body identity mismatch")
	}
	return nil
}

func FormatAmount(amount decimal.Decimal) (string, error) {
	rounded := amount.Round(2)
	if !rounded.IsPositive() || rounded.GreaterThan(decimal.RequireFromString("999999999.99")) {
		return "", errors.New("KPay amount must be between 0.01 and 999999999.99")
	}
	return rounded.StringFixed(2), nil
}

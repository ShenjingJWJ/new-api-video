package stpay

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
)

const (
	TransTypeSales  = "sales"
	TransTypeTest   = "test"
	TransModelFixed = "M"
	APITypeNormal   = "1"
	GetPayLinkNo    = "N"
	CharsetUTF8     = "UTF8"

	// CardTypeSBP 是正式环境的支付方式：俄罗斯 SBP 二维码扫码。
	CardTypeSBP = "sbp"
	// CardTypeOnlineBanking 是测试 MID 唯一可用的支付方式。
	CardTypeOnlineBanking = "online_banking"

	RespCodeSuccess = "00"
	RespCodePending = "003"
	RespCodeFailed  = "01"
	RespMsgPending  = "pending_async"
)

type Client struct {
	BaseURL    string
	MerNo      string
	TerNo      string
	HashSecret string
	// CardType 指定支付方式（sbp / online_banking）。
	// 为空时回落到 CardTypeSBP —— 正式环境的正确取值。
	CardType string
	HTTP     *http.Client
}

// cardType 返回本次下发给渠道的 cardType（空值回落生产值，见 NormalizeCardType）。
func (c *Client) cardType() string {
	return NormalizeCardType(c.CardType)
}

type PaymentRequest struct {
	OrderNo      string
	Amount       string
	CurrencyCode string
	PayIP        string
	ReturnURL    string
	MerMgrURL    string
	MerNotifyURL string
	GoodsString  string
	Email        string
}

type PaymentResponse struct {
	TransType      string `json:"transType"`
	OrderNo        string `json:"orderNo"`
	MerNo          string `json:"merNo"`
	TerNo          string `json:"terNo"`
	CurrencyCode   string `json:"currencyCode"`
	Amount         string `json:"amount"`
	TradeNo        string `json:"tradeNo"`
	Hashcode       string `json:"hashcode"`
	RespCode       string `json:"respCode"`
	RespMsg        string `json:"respMsg"`
	Acquirer       string `json:"acquirer"`
	SettleCurrency string `json:"settleCurrency"`
	SettleAmount   string `json:"settleAmount"`
	SkipTo3DURL    string `json:"skipTo3DURL"`
}

type QueryResponse struct {
	MerNo     string `json:"merNo"`
	TerNo     string `json:"terNo"`
	TradeNo   string `json:"tradeNo"`
	OrderNo   string `json:"orderNo"`
	Amount    string `json:"amount"`
	Currency  string `json:"currency"`
	Signature string `json:"signature"`
	RespCode  string `json:"respCode"`
	RespMsg   string `json:"respMsg"`
	ErrorCode string `json:"errorCode"`
	ErrorMsg  string `json:"errorMsg"`
	Acquirer  string `json:"acquirer"`
}

func (c *Client) httpClient() *http.Client {
	client := http.Client{Timeout: 30 * time.Second}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	redirectPolicy := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if err := ValidatePaymentURL(request.URL.String()); err != nil {
			return err
		}
		if redirectPolicy != nil {
			return redirectPolicy(request, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	return &client
}

func (c *Client) CreatePayment(ctx context.Context, req PaymentRequest) (*PaymentResponse, error) {
	form := url.Values{}
	form.Set("merNo", c.MerNo)
	form.Set("terNo", c.TerNo)
	form.Set("CharacterSet", CharsetUTF8)
	form.Set("transType", TransTypeSales)
	form.Set("transModel", TransModelFixed)
	form.Set("getPayLink", GetPayLinkNo)
	form.Set("apiType", APITypeNormal)
	form.Set("amount", req.Amount)
	form.Set("currencyCode", req.CurrencyCode)
	form.Set("orderNo", req.OrderNo)
	form.Set("returnURL", encodeCallbackURL(req.ReturnURL))
	form.Set("merMgrURL", req.MerMgrURL)
	form.Set("merNotifyURL", encodeCallbackURL(req.MerNotifyURL))
	form.Set("payIP", req.PayIP)
	form.Set("goodsString", req.GoodsString)
	form.Set("cardType", c.cardType())
	if strings.TrimSpace(req.Email) != "" {
		form.Set("grEmail", req.Email)
	}
	form.Set("hashcode", SignRequest(
		CharsetUTF8, c.MerNo, c.TerNo, req.OrderNo, req.CurrencyCode, req.Amount,
		req.PayIP, TransTypeSales, TransModelFixed, c.HashSecret,
	))

	endpoint, err := joinURL(c.BaseURL, "/api/gotoPayment")
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	body, err := c.do(httpReq)
	if err != nil {
		return nil, err
	}
	var parsed PaymentResponse
	if err := common.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("stpay create response is not json: %w (body=%s)", err, previewBody(body))
	}
	// 渠道的业务错误响应不带签名（例如 1103 card type unsupported、0025 密钥错误）。
	// 必须先判 respCode：否则会走验签分支，统一报成误导性的 "signature mismatch"，
	// 真实原因（通道未开通 / 密钥错误）会被日志掩盖。
	if parsed.RespCode != RespCodePending {
		return nil, fmt.Errorf("stpay create rejected: respCode=%s respMsg=%q", parsed.RespCode, parsed.RespMsg)
	}
	if !VerifyHex(SignResponse(
		parsed.Amount, parsed.CurrencyCode, parsed.MerNo, parsed.OrderNo, parsed.RespCode,
		parsed.RespMsg, parsed.TerNo, parsed.TradeNo, parsed.TransType, c.HashSecret,
	), parsed.Hashcode) {
		return nil, fmt.Errorf("stpay create response signature mismatch (body=%s)", previewBody(body))
	}
	return &parsed, nil
}

// previewBody 截断响应体用于日志定位，避免把整段报文写进日志。
func previewBody(body []byte) string {
	const maxLen = 256
	if len(body) <= maxLen {
		return string(body)
	}
	return string(body[:maxLen]) + "...(truncated)"
}

func (c *Client) Query(ctx context.Context, orderNo, tradeNo, amount, currency string) (*QueryResponse, error) {
	form := url.Values{}
	form.Set("merNo", c.MerNo)
	form.Set("terNo", c.TerNo)
	form.Set("orderNo", orderNo)
	if strings.TrimSpace(tradeNo) != "" {
		form.Set("tradeNo", tradeNo)
	}
	form.Set("amount", amount)
	form.Set("currency", currency)
	form.Set("signature", SignQuery(c.MerNo, c.TerNo, orderNo, amount, currency, c.HashSecret))

	endpoint, err := joinURL(c.BaseURL, "/external/query")
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	body, err := c.do(httpReq)
	if err != nil {
		return nil, err
	}
	var parsed QueryResponse
	if err := common.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("stpay query response is not json: %w (body=%s)", err, previewBody(body))
	}
	logger.LogInfo(ctx, common.LogText("SBP query response order_no=%s resp_code=%s error_code=%s", orderNo, parsed.RespCode, parsed.ErrorCode))
	if strings.TrimSpace(parsed.ErrorCode) != "" {
		return &parsed, nil
	}
	if !VerifyHex(SignQueryResponse(
		parsed.MerNo, parsed.TerNo, parsed.OrderNo, parsed.Amount, parsed.Currency,
		parsed.TradeNo, parsed.RespCode, c.HashSecret,
	), parsed.Signature) {
		return nil, fmt.Errorf("stpay query response signature mismatch")
	}
	return &parsed, nil
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return body, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, fmt.Errorf("stpay http status %d", resp.StatusCode)
	}
	return body, nil
}

func joinURL(base, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("stpay gateway url must use http or https")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("stpay gateway host is empty")
	}
	if err := ValidatePaymentURL(base); err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func ValidatePaymentURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("invalid Russian SBP payment URL")
	}
	host := parsed.Hostname()
	address := net.ParseIP(host)
	if parsed.Scheme != "https" && host != "localhost" && (address == nil || !address.IsLoopback()) {
		return fmt.Errorf("Russian SBP payment URLs must use HTTPS")
	}
	return nil
}

// encodeCallbackURL follows the provider rule: '&' inside a callback URL is sent as '|'.
func encodeCallbackURL(raw string) string {
	return strings.ReplaceAll(raw, "&", "|")
}

func IsPaid(respCode, respMsg string) bool {
	return strings.TrimSpace(respCode) == RespCodeSuccess
}

func IsPending(respCode, respMsg string) bool {
	code := strings.TrimSpace(respCode)
	msg := strings.TrimSpace(respMsg)
	return code == RespCodePending || (code == RespCodeFailed && strings.EqualFold(msg, RespMsgPending))
}

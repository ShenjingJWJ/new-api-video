package controller

import (
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/kpay"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

const (
	kpayNotifySuffix = "/api/kpay/notify"
	kpayReturnSuffix = "/wallet?show_history=true"
)

func kpayClient(sandbox bool) *kpay.Client {
	if sandbox {
		return &kpay.Client{BaseURL: kpay.GatewaySandbox, MerchantCode: setting.KpaySandboxMerchantCode, AppID: setting.KpaySandboxAppID,
			PrivateKey: setting.KpaySandboxPrivateKey, PlatformPublicKey: setting.KpaySandboxPlatformPublicKey}
	}
	return &kpay.Client{BaseURL: kpay.GatewayProduction, MerchantCode: setting.KpayMerchantCode, AppID: setting.KpayAppID,
		PrivateKey: setting.KpayPrivateKey, PlatformPublicKey: setting.KpayPlatformPublicKey}
}

func kpayNotifyURL() (string, error) {
	address := strings.TrimSpace(setting.KpayNotifyURL)
	if address == "" {
		address = strings.TrimRight(service.GetCallbackAddress(), "/") + kpayNotifySuffix
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", common.NewMessage("The KPay notification URL must be an HTTPS URL without query parameters")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = kpayNotifySuffix
	}
	return parsed.String(), nil
}

// kpayReturnURL accepts a full URL or a bare site origin; an origin is
// completed with the wallet page that opens the billing history.
func kpayReturnURL() (string, error) {
	address := strings.TrimSpace(setting.KpayReturnURL)
	if address == "" {
		address = paymentReturnPath(kpayReturnSuffix)
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", common.NewMessage("The KPay return URL must be a complete HTTP or HTTPS URL")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path, parsed.RawQuery, _ = strings.Cut(kpayReturnSuffix, "?")
	}
	return parsed.String(), nil
}

func isKpayTopUpEnabled() bool {
	if !setting.KpayEnabled || !isPaymentComplianceConfirmed() {
		return false
	}
	if err := kpayClient(setting.KpaySandbox).Validate(); err != nil {
		return false
	}
	_, err := kpayNotifyURL()
	return err == nil
}

func kpayMinTopUp() int64 {
	minimum := decimal.NewFromInt(int64(setting.KpayMinTopUp))
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		if common.QuotaPerUnit <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) {
			return common.MaxWalletQuota
		}
		minimum = minimum.Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	}
	if !minimum.IsPositive() || minimum.GreaterThan(decimal.NewFromInt(common.MaxWalletQuota)) {
		return common.MaxWalletQuota
	}
	return minimum.Ceil().IntPart()
}

// kpayQuote returns the HKD amount, the credited quota, and the payment method
// for a top-up request. The quota is snapshotted on the order so later price
// or display-mode changes never alter what a paid order credits.
func kpayQuote(requested int64, group string) (string, int, string, error) {
	if requested <= 0 || common.QuotaPerUnit <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) ||
		setting.KpayUnitPrice <= 0 || math.IsNaN(setting.KpayUnitPrice) || math.IsInf(setting.KpayUnitPrice, 0) {
		return "", 0, "", common.NewMessage("Invalid top-up amount or payment pricing configuration")
	}
	units := decimal.NewFromInt(requested)
	quota := units.Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	method := model.PaymentMethodKpay
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		quota = units
		units = units.Div(decimal.NewFromFloat(common.QuotaPerUnit))
		method = model.PaymentMethodKpayTokens
	}
	if !quota.Equal(quota.Truncate(0)) {
		return "", 0, "", common.NewMessage("Invalid top-up amount")
	}
	creditedQuota, err := validateCreditedQuota(quota)
	if err != nil {
		return "", 0, "", err
	}
	ratio := common.GetTopupGroupRatio(group)
	if ratio == 0 {
		ratio = 1
	}
	discount := 1.0
	if requested <= math.MaxInt32 {
		if configured, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(requested)]; ok && configured > 0 {
			discount = configured
		}
	}
	if ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) || math.IsNaN(discount) || math.IsInf(discount, 0) {
		return "", 0, "", common.NewMessage("Payment configuration error")
	}
	amount, err := kpay.FormatAmount(units.Mul(decimal.NewFromFloat(setting.KpayUnitPrice)).Mul(decimal.NewFromFloat(ratio)).Mul(decimal.NewFromFloat(discount)))
	if err != nil {
		return "", 0, "", common.NewMessage("Payment amount is too low or out of range")
	}
	return amount, creditedQuota, method, nil
}

// kpayCheckoutOptions resolves the checkout page type and language. "auto"
// picks the H5 page for mobile user agents.
func kpayCheckoutOptions(c *gin.Context, mode, language string) (string, string, error) {
	if mode == "" {
		mode = setting.KpayCheckoutMode
	}
	if mode == "auto" {
		mode = "web"
		userAgent := strings.ToLower(c.Request.UserAgent())
		if strings.Contains(userAgent, "mobile") || strings.Contains(userAgent, "android") || strings.Contains(userAgent, "iphone") {
			mode = "h5"
		}
	}
	if mode != "web" && mode != "h5" {
		return "", "", common.NewMessage("Invalid KPay checkout mode")
	}
	if language == "" {
		language = setting.KpayLanguage
	}
	if language != "zh_CN" && language != "zh_HK" && language != "en_US" {
		return "", "", common.NewMessage("Invalid KPay checkout language")
	}
	return mode, language, nil
}

func kpayCreditBlocked(fields model.KpayPaymentFields) bool {
	return fields.ValidateCreditEnvironment() != nil
}

var (
	kpayPayMethods   = map[string]bool{"CARD": true, "ALIPAYCN": true, "ALIPAYHK": true, "WXPAY": true, "UNIONPAY": true, "PAYME": true, "FPS": true, "APPLEPAY": true, "GOOGLEPAY": true, "OCTOPUS": true}
	kpayPageFeatures = map[string]bool{"HIDE_REMAINING_PAYMENT_TIME": true, "HIDE_ORDER_ITEM_DETAILS_BUTTON": true, "HIDE_BIND_CARD_PAY_BUTTON": true, "MUST_ENABLE_BIND_CARD_PAY_BUTTON": true, "HIDE_EMAIL_ADDRESS": true, "HIDE_BILL_INFORMATION": true}
)

// validateKpayOption checks one Kpay* option before it is saved.
func validateKpayOption(key, value string) error {
	switch key {
	case "KpayEnabled", "KpaySandbox":
		if value != "true" && value != "false" {
			return common.NewMessage("The KPay switch must be true or false")
		}
		if key != "KpayEnabled" || value != "true" {
			return nil
		}
		if !operation_setting.IsPaymentComplianceConfirmed() {
			return common.NewMessage("Confirm the payment compliance terms before enabling KPay")
		}
		if err := kpayClient(setting.KpaySandbox).Validate(); err != nil {
			return common.NewMessage("Configure the merchant code and valid RSA keys for the current KPay environment first")
		}
		_, err := kpayNotifyURL()
		return err
	case "KpayUnitPrice":
		number, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 {
			return common.NewMessage("The KPay HKD exchange rate must be a finite positive number")
		}
	case "KpayMinTopUp", "KpayRateLimitNum", "KpayRateLimitDuration", "KpayReconcileInterval":
		minimum, maximum := 1, 1000000000
		switch key {
		case "KpayRateLimitNum":
			minimum, maximum = 0, 100000
		case "KpayRateLimitDuration":
			maximum = 86400
		case "KpayReconcileInterval":
			minimum, maximum = 30, 86400
		}
		number, err := strconv.Atoi(value)
		if err != nil || number < minimum || number > maximum {
			return common.NewMessage("{{key}} must be an integer between {{min}} and {{max}}", map[string]any{"key": key, "min": minimum, "max": maximum})
		}
	case "KpayLanguage":
		if value != "zh_CN" && value != "zh_HK" && value != "en_US" {
			return common.NewMessage("Invalid KPay checkout language")
		}
	case "KpayCheckoutMode":
		if value != "auto" && value != "web" && value != "h5" {
			return common.NewMessage("Invalid KPay checkout mode")
		}
	case "KpayPayMethodOrder", "KpayPageFeatureControls":
		var values []string
		if err := common.UnmarshalJsonStr(value, &values); err != nil || values == nil {
			return common.NewMessage("KPay checkout settings must be a JSON array of strings")
		}
		allowed := kpayPayMethods
		if key == "KpayPageFeatureControls" {
			allowed = kpayPageFeatures
		}
		seen := make(map[string]bool, len(values))
		for _, entry := range values {
			if !allowed[entry] || seen[entry] {
				return common.NewMessage("KPay checkout settings contain an unknown or duplicate item")
			}
			seen[entry] = true
		}
		if seen["HIDE_BIND_CARD_PAY_BUTTON"] && seen["MUST_ENABLE_BIND_CARD_PAY_BUTTON"] {
			return common.NewMessage("Hiding and requiring card binding cannot both be enabled")
		}
	case "KpayNotifyURL", "KpayReturnURL", "KpayMerchantIcon":
		return validateKpayURLOption(key, value)
	case "KpayPrivateKey", "KpaySandboxPrivateKey", "KpayPlatformPublicKey", "KpaySandboxPlatformPublicKey":
		if strings.TrimSpace(value) == "" {
			return nil
		}
		probe := kpay.Client{BaseURL: kpay.GatewaySandbox, MerchantCode: "probe"}
		if strings.HasSuffix(key, "PrivateKey") {
			probe.PrivateKey = value
			if probe.ValidatePrivateKey() != nil {
				return common.NewMessage("The KPay private key must be a valid PEM RSA private key")
			}
			return nil
		}
		probe.PlatformPublicKey = value
		if probe.ValidatePlatformPublicKey() != nil {
			return common.NewMessage("The KPay platform public key must be a valid PEM RSA public key")
		}
	case "KpayMerchantCode", "KpaySandboxMerchantCode", "KpayAppID", "KpaySandboxAppID":
		if len(value) > 64 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n") {
			return common.NewMessage("Invalid KPay merchant code or app ID")
		}
	case "KpayOrderRemark":
		if utf8.RuneCountInString(value) > 255 {
			return common.NewMessage("The KPay order remark cannot exceed 255 characters")
		}
	default:
		return common.NewMessage("Unknown KPay setting")
	}
	return nil
}

func validateKpayURLOption(key, value string) error {
	if value == "" {
		return nil
	}
	maxLength := 256
	if key == "KpayMerchantIcon" {
		maxLength = 128
	}
	if len(value) > maxLength {
		return common.NewMessage("The KPay URL cannot exceed {{max}} characters", map[string]any{"max": maxLength})
	}
	address, err := url.Parse(value)
	if err != nil || address.Hostname() == "" || address.User != nil || address.Opaque != "" {
		return common.NewMessage("Invalid KPay URL")
	}
	hostname := strings.TrimSuffix(strings.ToLower(address.Hostname()), ".")
	ip := net.ParseIP(hostname)
	local := hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || (ip != nil && ip.IsLoopback())
	if address.Scheme != "https" && !(key == "KpayReturnURL" && address.Scheme == "http" && local) {
		return common.NewMessage("KPay URLs must use HTTPS (a development return URL may use HTTP localhost)")
	}
	if key != "KpayNotifyURL" {
		return nil
	}
	privateIP := ip != nil && (ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast())
	if address.RawQuery != "" || address.ForceQuery || address.Fragment != "" || address.Path == "" || address.Path == "/" ||
		local || privateIP || (ip == nil && !strings.Contains(hostname, ".")) {
		return common.NewMessage("The KPay notification URL must be a public HTTPS path without query parameters or fragments")
	}
	return nil
}

var errKpayNotEnabled error = common.NewMessage("KPay is not enabled or not fully configured")

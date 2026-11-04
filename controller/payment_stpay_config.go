package controller

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/stpay"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
)

const (
	// stpayReturnSuffix opens the billing history after the payer returns.
	stpayReturnSuffix = "/wallet?show_history=true"
	stpayNotifySuffix = "/api/stpay/notify"
	// stpayCurrency is fixed: orders do not store their own currency, so
	// notifications and queries are checked against this value.
	stpayCurrency = "RUB"
	// stpayDefaultPayerEmail is sent as grEmail, which the provider requires.
	stpayDefaultPayerEmail = "payer@example.com"
)

var errStpayNotEnabled error = common.NewMessage("Russian SBP payment is not enabled")

func stpayClient() *stpay.Client {
	return &stpay.Client{
		BaseURL:    setting.StpayGateway,
		MerNo:      setting.StpayMerNo,
		TerNo:      setting.StpayTerNo,
		HashSecret: setting.StpayHashKey,
		CardType:   setting.StpayCardType,
	}
}

func isStpayTopUpEnabled() bool {
	return isPaymentComplianceConfirmed() && setting.StpayEnabled && stpayClient().Configured() && stpay.ValidatePaymentURL(setting.StpayGateway) == nil
}

// stpayPayerEmail can be overridden with STPAY_FIXED_EMAIL.
func stpayPayerEmail() string {
	return strings.TrimSpace(common.GetEnvOrDefaultString("STPAY_FIXED_EMAIL", stpayDefaultPayerEmail))
}

// stpayCallbackURL resolves a configured callback address. A bare origin is
// completed with suffix; an empty or non-absolute value falls back to fallback,
// because the provider follows the value verbatim.
func stpayCallbackURL(configured, suffix, fallback string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return fallback
	}
	parsed, err := url.Parse(configured)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		common.SysError(common.LogText("[stpay] callback URL %q is not an absolute http(s) URL; using %q", configured, fallback))
		return fallback
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return configured
	}
	parsed.Path, parsed.RawQuery, _ = strings.Cut(suffix, "?")
	parsed.Fragment = ""
	return parsed.String()
}

func stpayReturnURL() string {
	return stpayCallbackURL(setting.StpayReturnURL, stpayReturnSuffix, paymentReturnPath(stpayReturnSuffix))
}

func stpayNotifyURL() string {
	return stpayCallbackURL(setting.StpayNotifyURL, stpayNotifySuffix, service.GetCallbackAddress()+stpayNotifySuffix)
}

// stpaySiteURL is the merchant site host reported to the provider.
func stpaySiteURL() string {
	if site := strings.TrimSpace(setting.StpaySiteURL); site != "" {
		return site
	}
	if parsed, err := url.Parse(service.GetCallbackAddress()); err == nil {
		return parsed.Host
	}
	return ""
}

// stpayTestCreditBlocked keeps free test-merchant orders from crediting a
// production wallet unless STPAY_ALLOW_TEST_NOTIFY=true. The provider's test
// merchants only support online_banking.
func stpayTestCreditBlocked() bool {
	if common.GetEnvOrDefaultBool("STPAY_ALLOW_TEST_NOTIFY", false) {
		return false
	}
	return stpay.NormalizeCardType(setting.StpayCardType) == stpay.CardTypeOnlineBanking
}

// WarnStpayConfiguration logs configurations that look like test settings
// carried into production.
func WarnStpayConfiguration() {
	if !setting.StpayEnabled {
		return
	}
	if cardType := stpay.NormalizeCardType(setting.StpayCardType); cardType != stpay.CardTypeSBP {
		common.SysError(common.LogText("[stpay] Russian SBP is enabled with cardType=%q instead of the production value sbp", cardType))
	}
	if setting.StpayUnitPrice <= 1 {
		common.SysError(common.LogText("[stpay] Russian SBP unit price is %v RUB per top-up unit; confirm this is the intended price", setting.StpayUnitPrice))
	}
}

// isValidStpayHashKey accepts letters and digits only: the key is appended to
// the signed string, so spaces, '&', '=' or non-ASCII would break signatures.
func isValidStpayHashKey(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// parseStpayNotifyAllowlist parses a comma-separated list of IPs and CIDRs.
func parseStpayNotifyAllowlist(raw string) ([]*net.IPNet, error) {
	var networks []*net.IPNet
	for entry := range strings.SplitSeq(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, network, err := net.ParseCIDR(entry); err == nil {
			networks = append(networks, network)
			continue
		}
		ip := net.ParseIP(entry)
		if ip == nil {
			return nil, common.NewMessage("The Russian SBP notification allowlist contains an invalid entry: {{entry}}", map[string]any{"entry": entry})
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return networks, nil
}

// stpayNotifySourceIP prefers CF-Connecting-IP only when the direct peer is
// loopback (Cloudflare tunnel to a local proxy); a direct connection always
// uses the real peer address so a forged header cannot bypass the allowlist.
func stpayNotifySourceIP(c *gin.Context) string {
	remoteIP := strings.TrimSpace(c.RemoteIP())
	if ip := net.ParseIP(remoteIP); ip != nil && ip.IsLoopback() {
		if forwarded := strings.TrimSpace(c.GetHeader("CF-Connecting-IP")); net.ParseIP(forwarded) != nil {
			return forwarded
		}
	}
	return remoteIP
}

// stpayNotifySourceAllowed fails closed once an allowlist is configured.
func stpayNotifySourceAllowed(c *gin.Context) bool {
	networks, err := parseStpayNotifyAllowlist(setting.StpayNotifyIPAllowlist)
	if err != nil {
		return false
	}
	if len(networks) == 0 {
		return true
	}
	ip := net.ParseIP(stpayNotifySourceIP(c))
	if ip == nil {
		return false
	}
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// validateStpayOption checks one Stpay* option before it is saved. The value of
// StpayHashKey is trimmed by the caller; an empty key means "keep the current".
func validateStpayOption(key, value string) error {
	switch key {
	case "StpayEnabled", "StpayRequireQueryConfirm":
		if value != "true" && value != "false" {
			return common.NewMessage("The Russian SBP switch must be true or false")
		}
		if key == "StpayEnabled" && value == "true" && !stpayClient().Configured() {
			return common.NewMessage("Configure the Russian SBP gateway, merchant number, terminal number and key first")
		}
	case "StpayCardType":
		if value != stpay.CardTypeSBP && value != stpay.CardTypeOnlineBanking {
			return common.NewMessage("The Russian SBP payment type must be sbp or online_banking")
		}
	case "StpayUnitPrice":
		if !isPositiveOptionValue(value) {
			return common.NewMessage("The Russian SBP RUB exchange rate must be a finite positive number")
		}
	case "StpayMinTopUp", "StpayRateLimitNum", "StpayRateLimitDuration":
		minimum, maximum := 1, 1000000000
		switch key {
		case "StpayRateLimitNum":
			minimum, maximum = 0, 100000
		case "StpayRateLimitDuration":
			maximum = 86400
		}
		number, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || number < minimum || number > maximum {
			return common.NewMessage("{{key}} must be an integer between {{min}} and {{max}}", map[string]any{"key": key, "min": minimum, "max": maximum})
		}
	case "StpayHashKey":
		if value != "" && !isValidStpayHashKey(value) {
			return common.NewMessage("The Russian SBP key must be 8 to 128 letters or digits")
		}
	case "StpayNotifyIPAllowlist":
		_, err := parseStpayNotifyAllowlist(value)
		return err
	case "StpayGateway", "StpayReturnURL", "StpayNotifyURL":
		if value == "" {
			return nil
		}
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return common.NewMessage("Russian SBP URLs must be complete HTTP or HTTPS URLs")
		}
		if key == "StpayGateway" && stpay.ValidatePaymentURL(value) != nil {
			return common.NewMessage("Russian SBP payment URLs must use HTTPS (HTTP localhost is allowed for development)")
		}
	case "StpayMerNo", "StpayTerNo", "StpaySiteURL", "StpayCurrency":
		if len(value) > 255 || strings.ContainsAny(value, "\r\n") {
			return common.NewMessage("Invalid Russian SBP merchant setting")
		}
	default:
		return common.NewMessage("Unknown Russian SBP setting")
	}
	return nil
}

// stpayQueryMatches binds a signed query response to the local order.
func stpayQueryMatches(resp *stpay.QueryResponse, orderNo, tradeNo, amount string) bool {
	return resp.OrderNo == orderNo &&
		strings.TrimSpace(resp.TradeNo) == tradeNo &&
		resp.MerNo == setting.StpayMerNo &&
		resp.TerNo == setting.StpayTerNo &&
		strings.EqualFold(resp.Currency, stpayCurrency) &&
		stpay.SameAmount(resp.Amount, amount)
}

// confirmStpaySettlement queries the provider before a notification credits.
// The notification signature has no timestamp or nonce, so a leaked HashKey
// could forge one; an outbound query cannot be forged.
func confirmStpaySettlement(ctx context.Context, orderNo, tradeNo, amount string) error {
	if !setting.StpayRequireQueryConfirm {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := stpayClient().Query(ctx, orderNo, tradeNo, amount, stpayCurrency)
	if err != nil {
		return err
	}
	if strings.TrimSpace(resp.ErrorCode) != "" {
		return errors.New("SBP query rejected: " + resp.ErrorCode + " " + resp.ErrorMsg)
	}
	if !stpayQueryMatches(resp, orderNo, tradeNo, amount) {
		return errors.New("SBP query response does not match the order")
	}
	if !stpay.IsPaid(resp.RespCode, resp.RespMsg) {
		return errors.New("SBP query does not show the order as paid")
	}
	return nil
}

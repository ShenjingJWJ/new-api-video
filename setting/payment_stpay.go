package setting

// Russian SBP (Stpay) configuration. Settlement currency is always RUB.
var (
	StpayEnabled bool
	StpayGateway string
	StpayMerNo   string
	StpayTerNo   string
	StpayHashKey string
	// StpayUnitPrice is the RUB charged per top-up unit (USD by default).
	StpayUnitPrice float64 = 1.0
	StpayMinTopUp  int     = 1
	StpayReturnURL string
	StpayNotifyURL string
	StpaySiteURL   string
	// StpayCardType is "sbp" in production. The provider's test merchants only
	// support "online_banking"; the default stays on the production value.
	StpayCardType string = "sbp"
	// StpayRateLimitNum 0 disables the per-user order and quote rate limit.
	StpayRateLimitNum      int = 0
	StpayRateLimitDuration int = 60
	// StpayRequireQueryConfirm makes a "paid" notification credit only after an
	// outbound order query confirms it. The notification signature carries no
	// timestamp or nonce, so a leaked HashKey could forge one offline.
	StpayRequireQueryConfirm bool = true
	// StpayNotifyIPAllowlist is a comma-separated list of IPs or CIDRs allowed
	// to send notifications. Empty means unrestricted.
	StpayNotifyIPAllowlist string
)

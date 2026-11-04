package setting

// KPay hosted checkout configuration. Live and sandbox credentials are kept
// side by side; KpaySandbox selects which pair new orders use, and each order
// snapshots that choice so later switches never re-route settlement.
var (
	KpayEnabled                  = false
	KpaySandbox                  = true
	KpayMerchantCode             = ""
	KpayPrivateKey               = ""
	KpayPlatformPublicKey        = ""
	KpayAppID                    = ""
	KpaySandboxMerchantCode      = ""
	KpaySandboxPrivateKey        = ""
	KpaySandboxPlatformPublicKey = ""
	KpaySandboxAppID             = ""
	// KpayUnitPrice is the HKD charged per top-up unit (USD by default).
	KpayUnitPrice           = 7.8
	KpayMinTopUp            = 1
	KpayNotifyURL           = ""
	KpayReturnURL           = ""
	KpayLanguage            = "zh_HK"
	KpayCheckoutMode        = "auto"
	KpayMerchantIcon        = ""
	KpayOrderRemark         = ""
	KpayPayMethodOrder      = "[]"
	KpayPageFeatureControls = `["HIDE_BIND_CARD_PAY_BUTTON"]`
	// KpayRateLimitNum 0 disables the per-user checkout rate limit.
	KpayRateLimitNum      = 0
	KpayRateLimitDuration = 60
	// KpayReconcileInterval is the minimum seconds between queries of one order.
	KpayReconcileInterval = 120
)

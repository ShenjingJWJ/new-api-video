package controller

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/kpay"
	"github.com/QuantumNous/new-api/service/stpay"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func setupGatewayNotificationDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.SubscriptionOrder{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.Log{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	originalDB, originalLogDB := model.DB, model.LOG_DB
	originalMainType, originalLogType := common.MainDatabaseType(), common.LogDatabaseType()
	originalRedis := common.RedisEnabled
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.DB, model.LOG_DB = originalDB, originalLogDB
		common.SetDatabaseTypes(originalMainType, originalLogType)
		common.RedisEnabled = originalRedis
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.Create(&model.User{Id: 501, Username: "notification-test", Quota: 100, Status: common.UserStatusEnabled}).Error)
	return db
}

func TestKpayNotificationBindsPaymentAndPreventsReplayCredit(t *testing.T) {
	platform, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	publicDER, err := x509.MarshalPKIXPublicKey(&platform.PublicKey)
	require.NoError(t, err)
	originalMerchant, originalApp, originalPublicKey := setting.KpayMerchantCode, setting.KpayAppID, setting.KpayPlatformPublicKey
	originalSandboxMerchant, originalSandboxApp, originalSandboxKey := setting.KpaySandboxMerchantCode, setting.KpaySandboxAppID, setting.KpaySandboxPlatformPublicKey
	setting.KpayMerchantCode, setting.KpayAppID = "merchant-test", ""
	setting.KpayPlatformPublicKey = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}))
	setting.KpaySandboxMerchantCode, setting.KpaySandboxAppID, setting.KpaySandboxPlatformPublicKey = setting.KpayMerchantCode, setting.KpayAppID, setting.KpayPlatformPublicKey
	t.Cleanup(func() {
		setting.KpayMerchantCode, setting.KpayAppID, setting.KpayPlatformPublicKey = originalMerchant, originalApp, originalPublicKey
		setting.KpaySandboxMerchantCode, setting.KpaySandboxAppID, setting.KpaySandboxPlatformPublicKey = originalSandboxMerchant, originalSandboxApp, originalSandboxKey
	})
	for _, testCase := range []struct {
		name           string
		amount         string
		currency       string
		merchant       string
		managed        string
		event          string
		signatureValid bool
		sandbox        bool
		wantStatus     int
		wantQuota      int
	}{
		{"paid and replay", "10.25", "HKD", "merchant-test", "managed-test", kpay.EventTypeSales, true, false, 200, 2600},
		{"wrong amount", "10.26", "HKD", "merchant-test", "managed-test", kpay.EventTypeSales, true, false, 400, 100},
		{"wrong currency", "10.25", "USD", "merchant-test", "managed-test", kpay.EventTypeSales, true, false, 400, 100},
		{"wrong merchant", "10.25", "HKD", "another-merchant", "managed-test", kpay.EventTypeSales, true, false, 400, 100},
		{"wrong managed order", "10.25", "HKD", "merchant-test", "another-managed-order", kpay.EventTypeSales, true, false, 400, 100},
		{"invalid signature", "10.25", "HKD", "merchant-test", "managed-test", kpay.EventTypeSales, false, false, 400, 100},
		{"refund event", "10.25", "HKD", "merchant-test", "managed-test", "REFUND", true, false, 200, 100},
		{"sandbox isolation", "10.25", "HKD", "merchant-test", "managed-test", kpay.EventTypeSales, true, true, 200, 100},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := setupGatewayNotificationDatabase(t)
			t.Setenv("KPAY_ALLOW_TEST_NOTIFY", "false")
			order := &model.TopUp{UserId: 501, Amount: 1, Money: 10.25, TradeNo: "notification-order", PaymentProvider: model.PaymentProviderKpay, PaymentMethod: model.PaymentMethodKpay, Status: common.TopUpStatusExpired, KpayQuota: 2500,
				KpayPaymentFields: model.KpayPaymentFields{KpayOrderNo: "managed-test", KpayAmount: "10.25", KpayCurrency: "HKD", KpayMerchantCode: "merchant-test", KpaySandbox: testCase.sandbox, KpayNotifyPath: "/api/kpay/notify"}}
			require.NoError(t, order.Insert())
			body, err := common.Marshal(kpay.Notification{MerchantCode: testCase.merchant, ManagedMerchantOrderNo: order.TradeNo, ManagedOrderNo: testCase.managed, OrderNo: "provider-payment", EventType: testCase.event, TransactionState: 2, LocalPayAmount: decimal.RequireFromString(testCase.amount), LocalPayCurrency: testCase.currency})
			require.NoError(t, err)
			headers := http.Header{}
			headers.Set("Content-Type", "application/json")
			headers.Set("K-Timestamp", "1700000000000")
			headers.Set("K-Nonce-Str", "notification-nonce")
			headers.Set("K-Merchant-Code", "merchant-test")
			text := strings.Join([]string{"POST", "/api/kpay/notify", headers.Get("K-Timestamp"), headers.Get("K-Nonce-Str"), headers.Get("K-Merchant-Code"), string(body), ""}, "\n")
			digest := sha256.Sum256([]byte(text))
			signature, err := rsa.SignPKCS1v15(rand.Reader, platform, crypto.SHA256, digest[:])
			require.NoError(t, err)
			headers.Set("K-Signature", base64.StdEncoding.EncodeToString(signature))
			if !testCase.signatureValid {
				headers.Set("K-Signature", "invalid")
			}
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.POST("/api/kpay/notify", KpayNotify)
			for range 2 {
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/api/kpay/notify", bytes.NewReader(body))
				request.Header = headers.Clone()
				router.ServeHTTP(response, request)
				assert.Equal(t, testCase.wantStatus, response.Code)
			}
			var user model.User
			require.NoError(t, db.First(&user, 501).Error)
			assert.Equal(t, testCase.wantQuota, user.Quota)
		})
	}
}

func TestStpayNotificationRejectsOversizedBody(t *testing.T) {
	originalAllowlist := setting.StpayNotifyIPAllowlist
	setting.StpayNotifyIPAllowlist = ""
	t.Cleanup(func() { setting.StpayNotifyIPAllowlist = originalAllowlist })
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/stpay/notify", strings.NewReader(strings.Repeat("a", (1<<20)+1)))
	StpayNotify(ctx)
	assert.Equal(t, http.StatusBadRequest, response.Code)
}

func TestStpayNotificationRequiresMatchingProviderQuery(t *testing.T) {
	originalGateway, originalMerchant, originalTerminal, originalSecret := setting.StpayGateway, setting.StpayMerNo, setting.StpayTerNo, setting.StpayHashKey
	originalCardType, originalAllowlist, originalConfirm := setting.StpayCardType, setting.StpayNotifyIPAllowlist, setting.StpayRequireQueryConfirm
	setting.StpayMerNo, setting.StpayTerNo, setting.StpayHashKey = "merchant-test", "terminal-test", "TestSecret123"
	setting.StpayCardType, setting.StpayNotifyIPAllowlist, setting.StpayRequireQueryConfirm = stpay.CardTypeSBP, "", true
	t.Cleanup(func() {
		setting.StpayGateway, setting.StpayMerNo, setting.StpayTerNo, setting.StpayHashKey = originalGateway, originalMerchant, originalTerminal, originalSecret
		setting.StpayCardType, setting.StpayNotifyIPAllowlist, setting.StpayRequireQueryConfirm = originalCardType, originalAllowlist, originalConfirm
	})
	for _, testCase := range []struct {
		name           string
		amount         string
		trade          string
		signatureValid bool
		confirmed      bool
		wantStatus     int
		wantQuota      int
		accepted       bool
	}{
		{"paid and replay", "10.25", "provider-test", true, true, 200, 2600, true},
		{"wrong amount", "10.26", "provider-test", true, true, 200, 100, false},
		{"wrong provider reference", "10.25", "another-provider", true, true, 200, 100, false},
		{"invalid signature", "10.25", "provider-test", false, true, 400, 100, false},
		{"provider did not confirm", "10.25", "provider-test", true, false, 200, 100, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := setupGatewayNotificationDatabase(t)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				response := stpay.QueryResponse{MerNo: setting.StpayMerNo, TerNo: setting.StpayTerNo, OrderNo: "notification-order", TradeNo: "provider-test", Amount: "10.25", Currency: "RUB", RespCode: stpay.RespCodeSuccess}
				if !testCase.confirmed {
					response.RespCode = stpay.RespCodePending
				}
				response.Signature = stpay.SignQueryResponse(response.MerNo, response.TerNo, response.OrderNo, response.Amount, response.Currency, response.TradeNo, response.RespCode, setting.StpayHashKey)
				encoded, err := common.Marshal(response)
				assert.NoError(t, err)
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write(encoded)
			}))
			defer server.Close()
			setting.StpayGateway = server.URL
			order := &model.TopUp{UserId: 501, Amount: 2500, Money: 10.25, TradeNo: "notification-order", PaymentProvider: model.PaymentProviderStpay, PaymentMethod: model.PaymentMethodStpaySBPTokens, Status: common.TopUpStatusExpired, StpayTradeNo: "provider-test"}
			require.NoError(t, order.Insert())
			form := url.Values{"merNo": {setting.StpayMerNo}, "terNo": {setting.StpayTerNo}, "orderNo": {order.TradeNo}, "tradeNo": {testCase.trade}, "amount": {testCase.amount}, "currencyCode": {"RUB"}, "respCode": {stpay.RespCodeSuccess}, "respMsg": {"paid"}, "transType": {stpay.TransTypeSales}}
			form.Set("hashcode", stpay.SignResponse(testCase.amount, "RUB", setting.StpayMerNo, order.TradeNo, stpay.RespCodeSuccess, "paid", setting.StpayTerNo, testCase.trade, stpay.TransTypeSales, setting.StpayHashKey))
			if !testCase.signatureValid {
				form.Set("hashcode", "invalid")
			}
			router := gin.New()
			router.POST("/api/stpay/notify", StpayNotify)
			for range 2 {
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/api/stpay/notify", strings.NewReader(form.Encode()))
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				router.ServeHTTP(response, request)
				assert.Equal(t, testCase.wantStatus, response.Code)
				expected := "0"
				if testCase.accepted {
					expected = "1"
				}
				assert.JSONEq(t, "{\"merNotifyStatus\":\""+expected+"\"}", response.Body.String())
			}
			var user model.User
			require.NoError(t, db.First(&user, 501).Error)
			assert.Equal(t, testCase.wantQuota, user.Quota)
		})
	}
}

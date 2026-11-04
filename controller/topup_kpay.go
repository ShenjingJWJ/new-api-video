package controller

import (
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/kpay"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/thanhpk/randstr"
)

type KpayPayRequest struct {
	Amount       int64  `json:"amount"`
	CheckoutMode string `json:"checkout_mode"`
	Language     string `json:"language"`
}

type SubscriptionKpayPayRequest struct {
	PlanID       int    `json:"plan_id"`
	CheckoutMode string `json:"checkout_mode"`
	Language     string `json:"language"`
}

func RequestKpayAmount(c *gin.Context) {
	if !isKpayTopUpEnabled() {
		common.ApiError(c, errKpayNotEnabled)
		return
	}
	var request KpayPayRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Amount < kpayMinTopUp() {
		common.ApiErrorT(c, "Top-up amount is below the minimum or the parameters are invalid")
		return
	}
	group, err := model.GetUserGroup(c.GetInt("id"), true)
	if err != nil {
		common.ApiErrorT(c, "Failed to get the user group")
		return
	}
	amount, _, _, err := kpayQuote(request.Amount, group)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, amount)
}

func RequestKpayPay(c *gin.Context) {
	if !isKpayTopUpEnabled() {
		common.ApiError(c, errKpayNotEnabled)
		return
	}
	var request KpayPayRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Amount < kpayMinTopUp() {
		common.ApiErrorT(c, "Top-up amount is below the minimum or the parameters are invalid")
		return
	}
	mode, language, err := kpayCheckoutOptions(c, request.CheckoutMode, request.Language)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	userID := c.GetInt("id")
	group, err := model.GetUserGroup(userID, true)
	if err != nil {
		common.ApiErrorT(c, "Failed to get the user group")
		return
	}
	amount, quota, method, err := kpayQuote(request.Amount, group)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if err := model.ValidateTopUpQuotaCapacity(userID, quota); err != nil {
		common.ApiError(c, err)
		return
	}
	client := kpayClient(setting.KpaySandbox)
	fields, err := kpayOrderFields(client, amount)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	order := model.TopUp{
		UserId:            userID,
		Amount:            request.Amount,
		Money:             decimal.RequireFromString(amount).InexactFloat64(),
		TradeNo:           "KP-" + randstr.String(26),
		PaymentProvider:   model.PaymentProviderKpay,
		PaymentMethod:     method,
		CreateTime:        time.Now().Unix(),
		Status:            common.TopUpStatusPending,
		KpayQuota:         quota,
		KpayPaymentFields: fields,
	}
	if err := order.Insert(); err != nil {
		logger.LogError(c.Request.Context(), common.LogText("KPay failed to create top-up order user_id=%d trade_no=%s error=%q", userID, order.TradeNo, err.Error()))
		common.ApiErrorT(c, "Failed to create order")
		return
	}
	paymentURL, err := createKpayCheckout(c, client, order.TradeNo, "Wallet top-up", fields, mode, language)
	if err != nil {
		logger.LogWarn(c.Request.Context(), common.LogText("KPay checkout failed trade_no=%s error=%q", order.TradeNo, err.Error()))
		common.ApiErrorT(c, "Failed to create the KPay checkout. Please try again later")
		return
	}
	logger.LogInfo(c.Request.Context(), common.LogText("KPay top-up order created user_id=%d trade_no=%s amount=%s HKD quota=%d", userID, order.TradeNo, amount, quota))
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "success", "data": gin.H{"payment_url": paymentURL, "order_id": order.TradeNo, "amount": amount, "currency": "HKD"}})
}

func SubscriptionRequestKpayPay(c *gin.Context) {
	if !isKpayTopUpEnabled() {
		common.ApiError(c, errKpayNotEnabled)
		return
	}
	var request SubscriptionKpayPayRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.PlanID <= 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	mode, language, err := kpayCheckoutOptions(c, request.CheckoutMode, request.Language)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	plan, err := model.GetSubscriptionPlanById(request.PlanID)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !plan.Enabled {
		common.ApiErrorT(c, "Subscription plan is not enabled")
		return
	}
	userID := c.GetInt("id")
	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userID, plan.Id)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			common.ApiErrorT(c, "Purchase limit for this plan has been reached")
			return
		}
	}
	if plan.PriceAmount <= 0 || math.IsNaN(plan.PriceAmount) || math.IsInf(plan.PriceAmount, 0) ||
		setting.KpayUnitPrice <= 0 || math.IsNaN(setting.KpayUnitPrice) || math.IsInf(setting.KpayUnitPrice, 0) {
		common.ApiErrorT(c, "Invalid plan price or payment pricing configuration")
		return
	}
	// The plan price is final; group ratio and amount discounts do not apply.
	amount, err := kpay.FormatAmount(decimal.NewFromFloat(plan.PriceAmount).Mul(decimal.NewFromFloat(setting.KpayUnitPrice)))
	if err != nil {
		common.ApiErrorT(c, "Payment amount is too low or out of range")
		return
	}
	client := kpayClient(setting.KpaySandbox)
	fields, err := kpayOrderFields(client, amount)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	// The SUB prefix lets the billing history recognize subscription rows.
	order := model.SubscriptionOrder{
		UserId:            userID,
		PlanId:            plan.Id,
		Money:             plan.PriceAmount,
		TradeNo:           "SUB-KP-" + randstr.String(24),
		PaymentProvider:   model.PaymentProviderKpay,
		PaymentMethod:     model.PaymentMethodKpay,
		Status:            common.TopUpStatusPending,
		CreateTime:        time.Now().Unix(),
		KpayPaymentFields: fields,
	}
	if err := order.Insert(); err != nil {
		logger.LogError(c.Request.Context(), common.LogText("KPay failed to create subscription order user_id=%d plan_id=%d error=%q", userID, plan.Id, err.Error()))
		common.ApiErrorT(c, "Failed to create order")
		return
	}
	if err := model.CreatePendingSubscriptionTopUp(&order); err != nil {
		logger.LogWarn(c.Request.Context(), common.LogText("KPay failed to create pending subscription bill trade_no=%s error=%q", order.TradeNo, err.Error()))
	}
	paymentURL, err := createKpayCheckout(c, client, order.TradeNo, plan.Title, fields, mode, language)
	if err != nil {
		logger.LogWarn(c.Request.Context(), common.LogText("KPay subscription checkout failed trade_no=%s error=%q", order.TradeNo, err.Error()))
		common.ApiErrorT(c, "Failed to create the KPay checkout. Please try again later")
		return
	}
	logger.LogInfo(c.Request.Context(), common.LogText("KPay subscription order created user_id=%d plan_id=%d trade_no=%s amount=%s HKD", userID, plan.Id, order.TradeNo, amount))
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "success", "data": gin.H{"payment_url": paymentURL, "order_id": order.TradeNo, "amount": amount, "currency": "HKD"}})
}

// kpayOrderFields snapshots the merchant identity, environment, and HKD amount
// that a new order is created with.
func kpayOrderFields(client *kpay.Client, amount string) (model.KpayPaymentFields, error) {
	if err := client.Validate(); err != nil {
		return model.KpayPaymentFields{}, common.NewMessage("Invalid KPay merchant key configuration")
	}
	notifyURL, err := kpayNotifyURL()
	if err != nil {
		return model.KpayPaymentFields{}, err
	}
	if _, err := kpayReturnURL(); err != nil {
		return model.KpayPaymentFields{}, err
	}
	parsed, err := url.Parse(notifyURL)
	if err != nil {
		return model.KpayPaymentFields{}, err
	}
	return model.KpayPaymentFields{KpayAmount: amount, KpayCurrency: "HKD", KpayMerchantCode: client.MerchantCode, KpayAppID: client.AppID,
		KpaySandbox: setting.KpaySandbox, KpayNotifyPath: parsed.EscapedPath()}, nil
}

// createKpayCheckout creates the managed order and returns the signed checkout
// URL. A transport error leaves the local order pending: the provider may
// already have created it, and reconciliation can recover it by trade number.
func createKpayCheckout(c *gin.Context, client *kpay.Client, tradeNo, itemName string, fields model.KpayPaymentFields, mode, language string) (string, error) {
	notifyURL, err := kpayNotifyURL()
	if err != nil {
		return "", err
	}
	returnURL, err := kpayReturnURL()
	if err != nil {
		return "", err
	}
	var methods, controls []string
	if err := common.UnmarshalJsonStr(setting.KpayPayMethodOrder, &methods); err != nil {
		return "", common.NewMessage("Invalid KPay payment method configuration")
	}
	if err := common.UnmarshalJsonStr(setting.KpayPageFeatureControls, &controls); err != nil {
		return "", common.NewMessage("Invalid KPay page feature configuration")
	}
	amount := decimal.RequireFromString(fields.KpayAmount)
	response, err := client.CreateOrder(c.Request.Context(), kpay.ManagedOrderRequest{
		ManagedOutTradeNo:   tradeNo,
		LocalPayAmount:      amount,
		LocalPayCurrency:    fields.KpayCurrency,
		NotifyURL:           notifyURL,
		ReturnURL:           returnURL,
		MerchantIcon:        setting.KpayMerchantIcon,
		OrderRemark:         setting.KpayOrderRemark,
		PayMethodOrder:      methods,
		PageFeatureControls: controls,
		ItemList:            []kpay.Item{{ItemNo: tradeNo, ItemName: itemName, Price: amount, PriceCurrency: "HKD", Quantity: 1}},
	})
	if err != nil {
		return "", err
	}
	if err := model.SaveKpayOrderNo(tradeNo, response.ManagedOrderNo); err != nil {
		return "", err
	}
	return client.CheckoutURL(response.ManagedOrderNo, language, mode)
}

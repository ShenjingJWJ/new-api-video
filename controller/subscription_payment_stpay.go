package controller

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/stpay"
	"github.com/QuantumNous/new-api/setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/thanhpk/randstr"
)

type SubscriptionStpayPayRequest struct {
	PlanId int `json:"plan_id"`
}

// stpaySubscriptionAmount converts the USD plan price to RUB. The plan price is
// final, so group ratio and amount discounts do not apply.
func stpaySubscriptionAmount(priceAmount float64) (string, error) {
	if priceAmount < 0.01 || math.IsNaN(priceAmount) || math.IsInf(priceAmount, 0) ||
		setting.StpayUnitPrice <= 0 || math.IsNaN(setting.StpayUnitPrice) || math.IsInf(setting.StpayUnitPrice, 0) {
		return "", common.NewMessage("Invalid plan price or payment pricing configuration")
	}
	amountText, err := stpay.FormatAmount(decimal.NewFromFloat(priceAmount).Mul(decimal.NewFromFloat(setting.StpayUnitPrice)))
	if err != nil {
		return "", common.NewMessage("Plan amount is too low")
	}
	return amountText, nil
}

func SubscriptionRequestStpayPay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	var req SubscriptionStpayPayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	if !isStpayTopUpEnabled() {
		common.ApiError(c, errStpayNotEnabled)
		return
	}
	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !plan.Enabled {
		common.ApiErrorT(c, "Subscription plan is not enabled")
		return
	}
	userId := c.GetInt("id")
	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			common.ApiErrorT(c, "Purchase limit for this plan has been reached")
			return
		}
	}
	amountText, err := stpaySubscriptionAmount(plan.PriceAmount)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	// The SUB prefix lets the billing history recognize subscription rows.
	orderNo := fmt.Sprintf("SUB-SBP-%d-%d-%s", userId, time.Now().UnixMilli(), randstr.String(6))
	order := &model.SubscriptionOrder{
		UserId:          userId,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         orderNo,
		PaymentMethod:   model.PaymentMethodStpaySBP,
		PaymentProvider: model.PaymentProviderStpay,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	if err := order.Insert(); err != nil {
		logger.LogError(c.Request.Context(), common.LogText("SBP failed to create subscription order user_id=%d plan_id=%d error=%q", userId, plan.Id, err.Error()))
		common.ApiErrorT(c, "Failed to create order")
		return
	}

	paymentURL, tradeNo, err := createStpayPayment(c, orderNo, amountText, "subscription", "SUB:"+plan.Title)
	if err != nil {
		logger.LogWarn(c.Request.Context(), common.LogText("SBP failed to start subscription payment user_id=%d plan_id=%d trade_no=%s reason=%q", userId, plan.Id, orderNo, err.Error()))
		// The user never received a QR code, so failed is safe here.
		order.Status = common.TopUpStatusFailed
		_ = order.Update()
		common.ApiErrorT(c, "Failed to start payment")
		return
	}
	// The QR code is already usable; a failed write keeps the order pending.
	saved, err := model.SaveSubscriptionStpayPayment(order.Id, tradeNo, amountText)
	if err != nil || !saved {
		logger.LogError(c.Request.Context(), common.LogText("SBP failed to save subscription provider trade number trade_no=%s error=%v saved=%t", orderNo, err, saved))
		common.ApiErrorT(c, "Failed to start payment")
		return
	}
	order.StpayAmount = amountText
	if err := model.CreatePendingSubscriptionTopUp(order); err != nil {
		logger.LogError(c.Request.Context(), common.LogText("SBP failed to create pending subscription bill trade_no=%s error=%q", orderNo, err.Error()))
	}

	logger.LogInfo(c.Request.Context(), common.LogText("SBP subscription order created user_id=%d plan_id=%d trade_no=%s amount=%s RUB", userId, plan.Id, orderNo, amountText))
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{"payment_url": paymentURL, "order_id": orderNo}})
}

// handleStpaySubscriptionNotify settles an SBP notification for a subscription
// order. The caller has verified the signature and holds the order lock. It
// returns false when orderNo is not a subscription order.
func handleStpaySubscriptionNotify(c *gin.Context, form url.Values) bool {
	ctx := c.Request.Context()
	orderNo := form.Get("orderNo")
	order := model.GetSubscriptionOrderByTradeNo(orderNo)
	if order == nil {
		return false
	}
	if order.PaymentProvider != model.PaymentProviderStpay || order.PaymentMethod != model.PaymentMethodStpaySBP {
		logger.LogWarn(ctx, common.LogText("SBP notification subscription payment method mismatch order_no=%s", orderNo))
		stpayNotifyStatus(c, http.StatusOK, false)
		return true
	}
	tradeNo := strings.TrimSpace(form.Get("tradeNo"))
	if tradeNo == "" || (order.StpayTradeNo != "" && tradeNo != order.StpayTradeNo) {
		logger.LogWarn(ctx, common.LogText("SBP notification subscription trade number mismatch order_no=%s", orderNo))
		stpayNotifyStatus(c, http.StatusOK, false)
		return true
	}
	expectedAmount := strings.TrimSpace(order.StpayAmount)
	amount := form.Get("amount")
	if expectedAmount == "" || !strings.EqualFold(form.Get("currencyCode"), stpayCurrency) || !stpay.SameAmount(amount, expectedAmount) {
		logger.LogWarn(ctx, common.LogText("SBP notification subscription amount or currency mismatch order_no=%s", orderNo))
		stpayNotifyStatus(c, http.StatusOK, false)
		return true
	}
	if !stpay.IsPaid(form.Get("respCode"), form.Get("respMsg")) {
		// The order stays pending: CompleteSubscriptionOrder only accepts
		// pending, and the QR code may still be paid.
		stpayNotifyStatus(c, http.StatusOK, true)
		return true
	}
	if err := confirmStpaySettlement(ctx, orderNo, tradeNo, amount); err != nil {
		logger.LogError(ctx, common.LogText("SBP subscription notification not confirmed by order query order_no=%s error=%q", orderNo, err.Error()))
		stpayNotifyStatus(c, http.StatusOK, false)
		return true
	}
	payload := make(map[string]string, len(form))
	for key := range form {
		if key != "hashcode" {
			payload[key] = form.Get(key)
		}
	}
	if err := model.CompleteSubscriptionOrder(orderNo, common.GetJsonString(payload), model.PaymentProviderStpay, model.PaymentMethodStpaySBP); err != nil {
		logger.LogError(ctx, common.LogText("SBP subscription completion failed order_no=%s error=%q", orderNo, err.Error()))
		stpayNotifyStatus(c, http.StatusOK, false)
		return true
	}
	logger.LogInfo(ctx, common.LogText("SBP subscription completed order_no=%s plan_id=%d amount=%s RUB", orderNo, order.PlanId, amount))
	stpayNotifyStatus(c, http.StatusOK, true)
	return true
}

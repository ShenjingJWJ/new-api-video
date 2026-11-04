package controller

import (
	"io"
	"mime"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/kpay"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// KpayNotify handles KPay order result notifications. It verifies the RSA
// signature against the merchant identity snapshotted on the order, then
// fulfills only SALES events whose amount, currency and references match.
func KpayNotify(c *gin.Context) {
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		c.Status(http.StatusBadRequest)
		return
	}
	const bodyLimit = 1 << 20
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, bodyLimit+1))
	if err != nil || len(body) > bodyLimit {
		c.Status(http.StatusBadRequest)
		return
	}
	var notification kpay.Notification
	if err := common.Unmarshal(body, &notification); err != nil || notification.ManagedMerchantOrderNo == "" {
		c.Status(http.StatusBadRequest)
		return
	}
	order, err := model.GetKpayOrder(notification.ManagedMerchantOrderNo)
	if err != nil {
		logger.LogWarn(c.Request.Context(), common.LogText("KPay notification order not found trade_no=%s client_ip=%s", notification.ManagedMerchantOrderNo, c.ClientIP()))
		c.Status(http.StatusBadRequest)
		return
	}
	client := kpayClient(order.KpaySandbox)
	path := order.KpayNotifyPath
	if path == "" {
		path = c.Request.URL.EscapedPath()
	}
	if client.MerchantCode != order.KpayMerchantCode || client.AppID != order.KpayAppID || client.VerifyNotification(http.MethodPost, path, c.Request.Header, body) != nil {
		logger.LogWarn(c.Request.Context(), common.LogText("KPay notification signature verification failed trade_no=%s client_ip=%s", order.TradeNo, c.ClientIP()))
		c.Status(http.StatusBadRequest)
		return
	}
	// The eventType enumeration is undocumented; this line records the fields
	// the decision depends on without logging the payload itself.
	logger.LogInfo(c.Request.Context(), common.LogText(
		"KPay notification verified trade_no=%s event_type=%q transaction_state=%d order_no_present=%t transaction_no_present=%t managed_order_present=%t amount=%s %s",
		order.TradeNo, notification.EventType, notification.TransactionState,
		notification.OrderNo != "", notification.TransactionNo != "", notification.ManagedOrderNo != "",
		notification.LocalPayAmount, notification.LocalPayCurrency))
	// Refund and cancellation events never grant wallet credit or subscriptions.
	if notification.EventType != kpay.EventTypeSales {
		c.Status(http.StatusOK)
		return
	}
	expected, err := decimal.NewFromString(order.KpayAmount)
	if err != nil || !expected.IsPositive() || !notification.LocalPayAmount.Equal(expected) || notification.LocalPayCurrency != order.KpayCurrency ||
		notification.ManagedOrderNo == "" || (order.KpayOrderNo != "" && notification.ManagedOrderNo != order.KpayOrderNo) {
		logger.LogWarn(c.Request.Context(), common.LogText("KPay notification does not match the local order trade_no=%s", order.TradeNo))
		c.Status(http.StatusBadRequest)
		return
	}
	if notification.TransactionState != 2 {
		c.Status(http.StatusOK)
		return
	}
	// transactionNo is optional in the provider's field table; orderNo is the
	// required refund reference.
	if notification.OrderNo == "" {
		c.Status(http.StatusBadRequest)
		return
	}
	if kpayCreditBlocked(order.KpayPaymentFields) {
		c.Status(http.StatusOK)
		return
	}
	LockOrder(order.TradeNo)
	defer UnlockOrder(order.TradeNo)
	if err := completeKpayPayment(order, notification.ManagedOrderNo, notification.OrderNo, notification.TransactionNo, kpayStoredPayload(body), c.ClientIP()); err != nil {
		logger.LogWarn(c.Request.Context(), common.LogText("KPay notification fulfillment failed trade_no=%s error=%q", order.TradeNo, err.Error()))
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Status(http.StatusOK)
}

// kpayStoredPayload drops cardholder personal data (cardInfo, billInfo,
// deliveryInfo) before the notification is stored as a provider payload.
func kpayStoredPayload(raw []byte) string {
	var payload map[string]any
	if err := common.Unmarshal(raw, &payload); err != nil {
		return "{}"
	}
	for _, key := range []string{"cardInfo", "billInfo", "deliveryInfo"} {
		delete(payload, key)
	}
	encoded, err := common.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func completeKpayPayment(order model.KpayPendingOrder, managedOrderNo, paymentOrderNo, transactionNo, payload, callerIP string) error {
	if err := model.SaveKpayOrderNo(order.TradeNo, managedOrderNo); err != nil {
		return err
	}
	if err := model.SaveKpayPaymentOrderNo(order.TradeNo, paymentOrderNo); err != nil {
		return err
	}
	if err := model.SaveKpayTransactionNo(order.TradeNo, transactionNo); err != nil {
		return err
	}
	if order.Subscription {
		return model.CompleteSubscriptionOrder(order.TradeNo, payload, model.PaymentProviderKpay, model.PaymentMethodKpay)
	}
	return model.RechargeKpay(order.TradeNo, callerIP)
}

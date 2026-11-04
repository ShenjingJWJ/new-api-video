package controller

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/stpay"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/thanhpk/randstr"
)

type StpayPayRequest struct {
	Amount int64 `json:"amount"`
}

func stpayMinTopUp() int64 {
	minimum := decimal.NewFromInt(int64(setting.StpayMinTopUp))
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

// stpayQuote returns the RUB amount, the credited quota, and the payment method
// for a top-up request. Quote and checkout share it so both price identically;
// the credited quota matches model.RechargeStpay for the same method.
func stpayQuote(requested int64, group string) (string, int, string, error) {
	if requested <= 0 || common.QuotaPerUnit <= 0 || math.IsNaN(common.QuotaPerUnit) || math.IsInf(common.QuotaPerUnit, 0) ||
		setting.StpayUnitPrice <= 0 || math.IsNaN(setting.StpayUnitPrice) || math.IsInf(setting.StpayUnitPrice, 0) {
		return "", 0, "", common.NewMessage("Invalid top-up amount or payment pricing configuration")
	}
	units := decimal.NewFromInt(requested)
	quota := units.Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	method := model.PaymentMethodStpaySBP
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		quota = units
		units = units.Div(decimal.NewFromFloat(common.QuotaPerUnit))
		method = model.PaymentMethodStpaySBPTokens
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
	price := units.Mul(decimal.NewFromFloat(setting.StpayUnitPrice)).Mul(decimal.NewFromFloat(ratio)).Mul(decimal.NewFromFloat(discount))
	amountText, err := stpay.FormatAmount(price)
	if err != nil {
		return "", 0, "", common.NewMessage("Payment amount is too low")
	}
	// The provider rejects amounts at or below 0.01 RUB.
	paid, err := decimal.NewFromString(amountText)
	if err != nil || paid.LessThanOrEqual(decimal.NewFromFloat(0.01)) {
		return "", 0, "", common.NewMessage("Payment amount is too low")
	}
	return amountText, creditedQuota, method, nil
}

func RequestStpayAmount(c *gin.Context) {
	var req StpayPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		paymentError(c, common.NewMessage("Invalid parameters"))
		return
	}
	minTopup := stpayMinTopUp()
	if req.Amount < minTopup {
		paymentError(c, common.NewMessage("Top-up amount cannot be less than {{min}}", map[string]any{"min": minTopup}))
		return
	}
	group, err := model.GetUserGroup(c.GetInt("id"), true)
	if err != nil {
		paymentError(c, common.NewMessage("Failed to get the user group"))
		return
	}
	amountText, _, _, err := stpayQuote(req.Amount, group)
	if err != nil {
		paymentError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": amountText})
}

func RequestStpayPay(c *gin.Context) {
	if !isStpayTopUpEnabled() {
		paymentError(c, errStpayNotEnabled)
		return
	}
	var req StpayPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		paymentError(c, common.NewMessage("Invalid parameters"))
		return
	}
	minTopup := stpayMinTopUp()
	if req.Amount < minTopup {
		paymentError(c, common.NewMessage("Top-up amount cannot be less than {{min}}", map[string]any{"min": minTopup}))
		return
	}
	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		paymentError(c, common.NewMessage("Failed to get the user group"))
		return
	}
	amountText, quota, method, err := stpayQuote(req.Amount, group)
	if err != nil {
		paymentError(c, err)
		return
	}
	if err := model.ValidateTopUpQuotaCapacity(id, quota); err != nil {
		paymentError(c, err)
		return
	}

	orderNo := fmt.Sprintf("SBP-%d-%d-%s", id, time.Now().UnixMilli(), randstr.String(6))
	money, _ := decimal.NewFromString(amountText)
	topUp := &model.TopUp{
		UserId:          id,
		Amount:          req.Amount,
		Money:           money.InexactFloat64(),
		TradeNo:         orderNo,
		PaymentMethod:   method,
		PaymentProvider: model.PaymentProviderStpay,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	if err := topUp.Insert(); err != nil {
		logger.LogError(c.Request.Context(), common.LogText("SBP failed to create top-up order user_id=%d trade_no=%s error=%q", id, orderNo, err.Error()))
		paymentError(c, common.NewMessage("Failed to create order"))
		return
	}

	paymentURL, tradeNo, err := createStpayPayment(c, orderNo, amountText, "topup", "wallet topup")
	if err != nil {
		logger.LogWarn(c.Request.Context(), common.LogText("SBP failed to start payment user_id=%d trade_no=%s reason=%q", id, orderNo, err.Error()))
		// The user never received a QR code, so the order cannot have been paid.
		_ = model.TransitionTopUpStatus(orderNo, model.PaymentProviderStpay, common.TopUpStatusPending, common.TopUpStatusFailed)
		paymentError(c, common.NewMessage("Failed to start payment"))
		return
	}
	saved, err := model.SaveStpayTradeNo(topUp.Id, tradeNo)
	if err != nil || !saved {
		logger.LogError(c.Request.Context(), common.LogText("SBP failed to save provider trade number user_id=%d trade_no=%s error=%v saved=%t", id, orderNo, err, saved))
		paymentError(c, common.NewMessage("Failed to start payment"))
		return
	}

	logger.LogInfo(c.Request.Context(), common.LogText("SBP top-up order created user_id=%d trade_no=%s amount=%s RUB", id, orderNo, amountText))
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{"payment_url": paymentURL, "order_id": orderNo}})
}

// createStpayPayment creates the provider order and returns the QR page URL
// and the provider trade number after binding the signed response to orderNo.
func createStpayPayment(c *gin.Context, orderNo, amountText, goodsID, goodsName string) (string, string, error) {
	goods, err := common.Marshal(map[string]any{
		"goodsInfo": []map[string]string{{
			"goodsID":    goodsID,
			"goodsName":  goodsName,
			"quantity":   "1",
			"goodsPrice": amountText,
		}},
	})
	if err != nil {
		return "", "", err
	}
	resp, err := stpayClient().CreatePayment(c.Request.Context(), stpay.PaymentRequest{
		OrderNo:      orderNo,
		Amount:       amountText,
		CurrencyCode: stpayCurrency,
		PayIP:        c.ClientIP(),
		ReturnURL:    stpayReturnURL(),
		MerMgrURL:    stpaySiteURL(),
		MerNotifyURL: stpayNotifyURL(),
		GoodsString:  string(goods),
		Email:        stpayPayerEmail(),
	})
	if err != nil {
		return "", "", err
	}
	if resp.RespCode != stpay.RespCodePending || resp.OrderNo != orderNo ||
		resp.MerNo != setting.StpayMerNo || resp.TerNo != setting.StpayTerNo ||
		strings.TrimSpace(resp.TradeNo) == "" ||
		!strings.EqualFold(resp.CurrencyCode, stpayCurrency) || !stpay.SameAmount(resp.Amount, amountText) {
		return "", "", fmt.Errorf("provider response does not match the order")
	}
	if stpay.ValidatePaymentURL(resp.SkipTo3DURL) != nil {
		return "", "", fmt.Errorf("provider returned an invalid payment URL")
	}
	return resp.SkipTo3DURL, strings.TrimSpace(resp.TradeNo), nil
}

func stpayNotifyStatus(c *gin.Context, status int, accepted bool) {
	value := "0"
	if accepted {
		value = "1"
	}
	c.JSON(status, gin.H{"merNotifyStatus": value})
}

// StpayNotify handles SBP asynchronous notifications for top-ups and
// subscriptions. merNotifyStatus=0 makes the provider retry; 1 acknowledges.
func StpayNotify(c *gin.Context) {
	ctx := c.Request.Context()
	if !stpayNotifySourceAllowed(c) {
		logger.LogWarn(ctx, common.LogText("SBP notification source not allowed source_ip=%s client_ip=%s", stpayNotifySourceIP(c), c.ClientIP()))
		stpayNotifyStatus(c, http.StatusForbidden, false)
		return
	}
	// The body is never logged: it carries the hash code, amount and trade number.
	const bodyLimit = 1 << 20
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, bodyLimit+1))
	if err != nil || len(body) > bodyLimit {
		stpayNotifyStatus(c, http.StatusBadRequest, false)
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if !stpayClient().Configured() {
		logger.LogWarn(ctx, common.LogText("SBP notification rejected reason=not_configured client_ip=%s", c.ClientIP()))
		stpayNotifyStatus(c, http.StatusForbidden, false)
		return
	}
	mediaType, _, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if mediaType == "multipart/form-data" {
		err = c.Request.ParseMultipartForm(1 << 20)
		if c.Request.MultipartForm != nil {
			defer c.Request.MultipartForm.RemoveAll()
		}
	} else {
		err = c.Request.ParseForm()
	}
	if err != nil {
		stpayNotifyStatus(c, http.StatusBadRequest, false)
		return
	}
	form := c.Request.PostForm
	orderNo := form.Get("orderNo")
	expected := stpay.SignResponse(
		form.Get("amount"), form.Get("currencyCode"), form.Get("merNo"), orderNo, form.Get("respCode"), form.Get("respMsg"),
		form.Get("terNo"), form.Get("tradeNo"), form.Get("transType"), setting.StpayHashKey,
	)
	if !stpay.VerifyHex(expected, form.Get("hashcode")) || form.Get("merNo") != setting.StpayMerNo || form.Get("terNo") != setting.StpayTerNo {
		logger.LogWarn(ctx, common.LogText("SBP notification signature verification failed order_no=%s client_ip=%s", orderNo, c.ClientIP()))
		stpayNotifyStatus(c, http.StatusBadRequest, false)
		return
	}
	transType := form.Get("transType")
	if transType != stpay.TransTypeSales && transType != stpay.TransTypeTest {
		logger.LogWarn(ctx, common.LogText("SBP notification has an unexpected transaction type order_no=%s trans_type=%q", orderNo, transType))
		stpayNotifyStatus(c, http.StatusBadRequest, false)
		return
	}
	if stpayTestCreditBlocked() || (transType == stpay.TransTypeTest && !common.GetEnvOrDefaultBool("STPAY_ALLOW_TEST_NOTIFY", false)) {
		// Acknowledge a verified test notification so the provider stops retrying.
		logger.LogWarn(ctx, common.LogText("SBP test notification not credited order_no=%s", orderNo))
		stpayNotifyStatus(c, http.StatusOK, true)
		return
	}

	LockOrder(orderNo)
	defer UnlockOrder(orderNo)
	topUp := model.GetTopUpByTradeNo(orderNo)
	// A subscription's bill row has no provider, so anything that is not an
	// SBP top-up is offered to the subscription branch first.
	if topUp == nil || topUp.PaymentProvider != model.PaymentProviderStpay {
		if handleStpaySubscriptionNotify(c, form) {
			return
		}
		logger.LogWarn(ctx, common.LogText("SBP notification order not found order_no=%s client_ip=%s", orderNo, c.ClientIP()))
		stpayNotifyStatus(c, http.StatusOK, false)
		return
	}
	accepted := handleStpayTopUpNotify(c, topUp, form)
	stpayNotifyStatus(c, http.StatusOK, accepted)
}

func handleStpayTopUpNotify(c *gin.Context, topUp *model.TopUp, form url.Values) bool {
	ctx := c.Request.Context()
	orderNo := topUp.TradeNo
	tradeNo := strings.TrimSpace(form.Get("tradeNo"))
	if tradeNo == "" || tradeNo != topUp.StpayTradeNo {
		logger.LogWarn(ctx, common.LogText("SBP notification trade number mismatch order_no=%s client_ip=%s", orderNo, c.ClientIP()))
		return false
	}
	amount := form.Get("amount")
	if !strings.EqualFold(form.Get("currencyCode"), stpayCurrency) || !stpay.SameAmount(amount, strconv.FormatFloat(topUp.Money, 'f', 2, 64)) {
		logger.LogWarn(ctx, common.LogText("SBP notification amount or currency mismatch order_no=%s", orderNo))
		return false
	}
	respCode, respMsg := form.Get("respCode"), form.Get("respMsg")
	if !stpay.IsPaid(respCode, respMsg) {
		// A definite failure is shown as failed; the QR code can still be paid
		// later and RechargeStpay accepts failed orders.
		if !stpay.IsPending(respCode, respMsg) {
			_ = model.TransitionTopUpStatus(orderNo, model.PaymentProviderStpay, common.TopUpStatusPending, common.TopUpStatusFailed)
		}
		return true
	}
	if err := confirmStpaySettlement(ctx, orderNo, tradeNo, amount); err != nil {
		logger.LogError(ctx, common.LogText("SBP notification not confirmed by order query order_no=%s error=%q", orderNo, err.Error()))
		return false
	}
	if err := model.RechargeStpay(orderNo, c.ClientIP()); err != nil {
		logger.LogError(ctx, common.LogText("SBP top-up credit failed order_no=%s error=%q", orderNo, err.Error()))
		return false
	}
	logger.LogInfo(ctx, common.LogText("SBP top-up credited order_no=%s amount=%s RUB", orderNo, amount))
	return true
}

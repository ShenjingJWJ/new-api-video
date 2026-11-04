package model

import (
	"cmp"
	"errors"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting"

	"gorm.io/gorm"
)

// KpayPaymentFields captures the payment identity and amount at creation time.
// Credentials remain in server settings; they are never stored with orders.
type KpayPaymentFields struct {
	KpayOrderNo        string `json:"-" gorm:"type:varchar(255)"`
	KpayAmount         string `json:"-" gorm:"type:varchar(32)"`
	KpayCurrency       string `json:"-" gorm:"type:varchar(8)"`
	KpayMerchantCode   string `json:"-" gorm:"type:varchar(64)"`
	KpayAppID          string `json:"-" gorm:"type:varchar(64)"`
	KpayTransactionNo  string `json:"-" gorm:"type:varchar(255)"`
	KpayPaymentOrderNo string `json:"-" gorm:"type:varchar(255)"`
	KpaySandbox        bool   `json:"-"`
	KpayNotifyPath     string `json:"-" gorm:"type:varchar(1024)"`
	KpayNextQueryTime  int64  `json:"-" gorm:"index"`
}

var ErrKpaySandboxCreditBlocked error = common.NewMessage("KPay sandbox orders cannot be credited in this environment")

// ValidateCreditEnvironment keeps a sandbox order from crediting a production
// wallet. It may credit only while sandbox mode is on and the operator has set
// KPAY_ALLOW_TEST_NOTIFY=true.
func (fields KpayPaymentFields) ValidateCreditEnvironment() error {
	if fields.KpaySandbox && (!setting.KpaySandbox || !common.GetEnvOrDefaultBool("KPAY_ALLOW_TEST_NOTIFY", false)) {
		return ErrKpaySandboxCreditBlocked
	}
	return nil
}

var kpayRetryableStatuses = []string{common.TopUpStatusPending, common.TopUpStatusFailed, common.TopUpStatusExpired}

// RechargeKpay credits the quota snapshotted at checkout. Failed and expired
// orders are still accepted: a KPay managed order can be paid after a failed
// attempt or after the local display state expired.
func RechargeKpay(tradeNo, callerIP string) error {
	if strings.TrimSpace(tradeNo) == "" {
		return errors.New("payment order number not provided")
	}
	var credited int
	var topUp TopUp
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if topUp.PaymentProvider != PaymentProviderKpay || (topUp.PaymentMethod != PaymentMethodKpay && topUp.PaymentMethod != PaymentMethodKpayTokens) {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status == common.TopUpStatusSuccess {
			return nil
		}
		if err := topUp.KpayPaymentFields.ValidateCreditEnvironment(); err != nil {
			return err
		}
		if topUp.KpayQuota <= 0 {
			return ErrInvalidTopUpQuota
		}
		result := tx.Model(&TopUp{}).
			Where("id = ? AND payment_provider = ? AND payment_method = ? AND status IN ?", topUp.Id, PaymentProviderKpay, topUp.PaymentMethod, kpayRetryableStatuses).
			Updates(map[string]any{"status": common.TopUpStatusSuccess, "complete_time": common.GetTimestamp()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			// SQLite skips the row lock, so a concurrent callback may have won.
			var current TopUp
			if err := tx.Select("status").First(&current, topUp.Id).Error; err != nil {
				return err
			}
			if current.Status == common.TopUpStatusSuccess {
				return nil
			}
			return ErrTopUpStatusInvalid
		}
		if err := creditTopUpQuota(tx, topUp.UserId, topUp.KpayQuota, nil); err != nil {
			return err
		}
		credited = topUp.KpayQuota
		return nil
	})
	if err != nil {
		return err
	}
	if credited > 0 {
		syncCreditUserQuotaCache(topUp.UserId, credited, common.LogText("kpay topup"))
		RecordTopupLog(topUp.UserId, common.NewMessage("{{provider}} top-up succeeded, amount added: {{quota}}, payment amount: {{amount}}", map[string]any{"provider": "KPay", "quota": logger.FormatQuota(credited), "amount": topUp.KpayCurrency + " " + topUp.KpayAmount}), callerIP, topUp.PaymentMethod, PaymentMethodKpay)
	}
	return nil
}

func SaveKpayOrderNo(tradeNo, managedOrderNo string) error {
	return saveKpayPaymentReference(tradeNo, "kpay_order_no", managedOrderNo)
}

func SaveKpayTransactionNo(tradeNo, transactionNo string) error {
	if strings.TrimSpace(transactionNo) == "" {
		return nil
	}
	return saveKpayPaymentReference(tradeNo, "kpay_transaction_no", transactionNo)
}

func SaveKpayPaymentOrderNo(tradeNo, paymentOrderNo string) error {
	if strings.TrimSpace(paymentOrderNo) == "" {
		return nil
	}
	return saveKpayPaymentReference(tradeNo, "kpay_payment_order_no", paymentOrderNo)
}

var errKpayReferenceMismatch = errors.New("KPay payment reference mismatch")

// saveKpayPaymentReference stores a provider reference on the subscription
// order or top-up owning tradeNo. A reference is immutable once observed,
// including after fulfillment; column is always one of the fixed names above.
func saveKpayPaymentReference(tradeNo, column, reference string) error {
	reference = strings.TrimSpace(reference)
	if strings.TrimSpace(tradeNo) == "" || reference == "" || len(reference) > 255 {
		return errors.New("invalid KPay payment reference")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var record any
		var fields KpayPaymentFields
		var subscription SubscriptionOrder
		err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&subscription).Error
		switch {
		case err == nil:
			if subscription.PaymentProvider != PaymentProviderKpay {
				return ErrPaymentMethodMismatch
			}
			record, fields = &SubscriptionOrder{}, subscription.KpayPaymentFields
		case errors.Is(err, gorm.ErrRecordNotFound):
			var topUp TopUp
			if err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(&topUp).Error; err != nil {
				return ErrTopUpNotFound
			}
			if topUp.PaymentProvider != PaymentProviderKpay {
				return ErrPaymentMethodMismatch
			}
			record, fields = &TopUp{}, topUp.KpayPaymentFields
		default:
			return err
		}
		existing := map[string]string{
			"kpay_order_no":         fields.KpayOrderNo,
			"kpay_transaction_no":   fields.KpayTransactionNo,
			"kpay_payment_order_no": fields.KpayPaymentOrderNo,
		}[column]
		if existing == reference {
			return nil
		}
		if existing != "" {
			return errKpayReferenceMismatch
		}
		result := tx.Model(record).
			Where("trade_no = ? AND payment_provider = ?", tradeNo, PaymentProviderKpay).
			Where("("+column+" = ? OR "+column+" IS NULL)", "").
			Update(column, reference)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errKpayReferenceMismatch
		}
		return nil
	})
}

type KpayPendingOrder struct {
	TradeNo    string
	UserId     int
	CreateTime int64
	Status     string
	KpayPaymentFields
	Subscription bool
}

// GetKpayOrder finds the KPay subscription order or top-up owning tradeNo.
func GetKpayOrder(tradeNo string) (KpayPendingOrder, error) {
	var subscription SubscriptionOrder
	err := DB.Where("trade_no = ? AND payment_provider = ?", tradeNo, PaymentProviderKpay).First(&subscription).Error
	if err == nil {
		if subscription.PaymentMethod != PaymentMethodKpay {
			return KpayPendingOrder{}, ErrPaymentMethodMismatch
		}
		return KpayPendingOrder{TradeNo: subscription.TradeNo, UserId: subscription.UserId, CreateTime: subscription.CreateTime, Status: subscription.Status, Subscription: true, KpayPaymentFields: subscription.KpayPaymentFields}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return KpayPendingOrder{}, err
	}
	var topUp TopUp
	if err := DB.Where("trade_no = ? AND payment_provider = ?", tradeNo, PaymentProviderKpay).First(&topUp).Error; err != nil {
		return KpayPendingOrder{}, err
	}
	if topUp.PaymentMethod != PaymentMethodKpay && topUp.PaymentMethod != PaymentMethodKpayTokens {
		return KpayPendingOrder{}, ErrPaymentMethodMismatch
	}
	return KpayPendingOrder{TradeNo: topUp.TradeNo, UserId: topUp.UserId, CreateTime: topUp.CreateTime, Status: topUp.Status, KpayPaymentFields: topUp.KpayPaymentFields}, nil
}

// GetPendingKpayOrders returns top-ups and subscription orders from the last
// 96 hours whose next query time has come, ordered by that time. userID 0
// scans every user.
func GetPendingKpayOrders(userID, limit int, now int64) ([]KpayPendingOrder, error) {
	if userID < 0 || now <= 0 {
		return nil, errors.New("invalid KPay reconciliation request")
	}
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 1000)
	query := DB.Where("payment_provider = ? AND status IN ? AND create_time >= ? AND create_time <= ? AND (kpay_next_query_time <= ? OR kpay_next_query_time IS NULL)",
		PaymentProviderKpay, kpayRetryableStatuses, now-96*3600, now, now)
	if userID > 0 {
		query = query.Where("user_id = ?", userID)
	}
	var topUps []TopUp
	if err := query.Session(&gorm.Session{}).Order("kpay_next_query_time asc").Order("id asc").Limit(limit).Find(&topUps).Error; err != nil {
		return nil, err
	}
	var subscriptions []SubscriptionOrder
	if err := query.Session(&gorm.Session{}).Order("kpay_next_query_time asc").Order("id asc").Limit(limit).Find(&subscriptions).Error; err != nil {
		return nil, err
	}
	orders := make([]KpayPendingOrder, 0, len(topUps)+len(subscriptions))
	for _, order := range topUps {
		orders = append(orders, KpayPendingOrder{TradeNo: order.TradeNo, UserId: order.UserId, CreateTime: order.CreateTime, Status: order.Status, KpayPaymentFields: order.KpayPaymentFields})
	}
	for _, order := range subscriptions {
		orders = append(orders, KpayPendingOrder{TradeNo: order.TradeNo, UserId: order.UserId, CreateTime: order.CreateTime, Status: order.Status, KpayPaymentFields: order.KpayPaymentFields, Subscription: true})
	}
	slices.SortStableFunc(orders, func(a, b KpayPendingOrder) int { return cmp.Compare(a.KpayNextQueryTime, b.KpayNextQueryTime) })
	return orders, nil
}

// ReserveKpayQuery claims one query of the order by advancing its next query
// time with a compare-and-set, so concurrent nodes never query the same order.
func ReserveKpayQuery(order KpayPendingOrder, nextQuery int64) (bool, error) {
	if order.TradeNo == "" || nextQuery <= order.KpayNextQueryTime {
		return false, errors.New("invalid KPay query reservation")
	}
	var record any = &TopUp{}
	if order.Subscription {
		record = &SubscriptionOrder{}
	}
	query := DB.Model(record).Where("trade_no = ? AND payment_provider = ? AND status IN ?", order.TradeNo, PaymentProviderKpay, kpayRetryableStatuses)
	if order.KpayNextQueryTime == 0 {
		query = query.Where("kpay_next_query_time = ? OR kpay_next_query_time IS NULL", 0)
	} else {
		query = query.Where("kpay_next_query_time = ?", order.KpayNextQueryTime)
	}
	result := query.Update("kpay_next_query_time", nextQuery)
	return result.RowsAffected == 1, result.Error
}

// ExpireKpayOrder mirrors a terminal managed-order state without overwriting a
// successful callback. A later valid payment may still fulfill expired orders.
func ExpireKpayOrder(order KpayPendingOrder) error {
	if order.TradeNo == "" {
		return errors.New("invalid KPay order")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var record any = &TopUp{}
		if order.Subscription {
			record = &SubscriptionOrder{}
		}
		openStatuses := []string{common.TopUpStatusPending, common.TopUpStatusFailed}
		result := tx.Model(record).Where("trade_no = ? AND payment_provider = ? AND status IN ?", order.TradeNo, PaymentProviderKpay, openStatuses).Update("status", common.TopUpStatusExpired)
		if result.Error != nil || !order.Subscription || result.RowsAffected == 0 {
			return result.Error
		}
		// The subscription's pending bill row carries no provider; see CreatePendingSubscriptionTopUp.
		return tx.Model(&TopUp{}).Where("trade_no = ? AND payment_method = ? AND amount = ? AND payment_provider = ? AND status IN ?", order.TradeNo, PaymentMethodKpay, 0, "", openStatuses).Update("status", common.TopUpStatusExpired).Error
	})
}

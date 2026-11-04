package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// SBP top-ups stay creditable after failed or expired: the QR code can be paid
// again while valid, and both states only drive the billing display.
var stpayRetryableStatuses = []string{common.TopUpStatusPending, common.TopUpStatusFailed, common.TopUpStatusExpired}

func GetPendingStpayTopUpsAtOffset(minCreateTime int64, limit, offset int) ([]*TopUp, error) {
	if limit <= 0 {
		limit = 50
	}
	offset = max(offset, 0)
	var topUps []*TopUp
	err := DB.Where("payment_provider = ? AND status IN ? AND create_time >= ?", PaymentProviderStpay, stpayRetryableStatuses, minCreateTime).
		Order("create_time asc").Order("id asc").Offset(offset).Limit(limit).Find(&topUps).Error
	return topUps, err
}

func GetPendingStpayTopUpsByUser(userId int, minCreateTime int64, limit int) ([]*TopUp, error) {
	if limit <= 0 {
		limit = 10
	}
	var topUps []*TopUp
	err := DB.Where("user_id = ? AND payment_provider = ? AND status IN ? AND create_time >= ?", userId, PaymentProviderStpay, stpayRetryableStatuses, minCreateTime).
		Order("create_time desc").Order("id desc").Limit(limit).Find(&topUps).Error
	return topUps, err
}

func ExpirePendingStpayTopUps(beforeCreateTime int64) (int64, error) {
	result := DB.Model(&TopUp{}).
		Where("payment_provider = ? AND status = ? AND create_time < ?", PaymentProviderStpay, common.TopUpStatusPending, beforeCreateTime).
		Update("status", common.TopUpStatusExpired)
	return result.RowsAffected, result.Error
}

func SaveStpayTradeNo(topUpID int, providerTradeNo string) (bool, error) {
	providerTradeNo = strings.TrimSpace(providerTradeNo)
	if topUpID <= 0 || providerTradeNo == "" {
		return false, errors.New("invalid SBP order or provider trade number")
	}
	result := DB.Model(&TopUp{}).
		Where("id = ? AND payment_provider = ? AND status = ?", topUpID, PaymentProviderStpay, common.TopUpStatusPending).
		Update("stpay_trade_no", providerTradeNo)
	return result.RowsAffected == 1, result.Error
}

// TransitionTopUpStatus moves an order of the given provider from one status
// to another with a compare-and-set, so a concurrent credit is never undone.
func TransitionTopUpStatus(tradeNo, provider, fromStatus, targetStatus string) error {
	if tradeNo == "" {
		return errors.New("payment order number not provided")
	}
	result := DB.Model(&TopUp{}).
		Where("trade_no = ? AND payment_provider = ? AND status = ?", tradeNo, provider, fromStatus).
		Update("status", targetStatus)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrTopUpStatusInvalid
	}
	return nil
}

// stpayCreditQuota returns the wallet quota an SBP top-up credits. Token-mode
// orders store the exact quota in Amount; currency-mode orders store units.
func stpayCreditQuota(topUp *TopUp) (int, error) {
	if topUp.PaymentMethod != PaymentMethodStpaySBP && topUp.PaymentMethod != PaymentMethodStpaySBPTokens {
		return 0, ErrPaymentMethodMismatch
	}
	quota := decimal.NewFromInt(topUp.Amount)
	if topUp.PaymentMethod == PaymentMethodStpaySBP {
		quota = quota.Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	}
	value, err := common.WalletQuotaFromDecimalStrict(quota)
	if err != nil || value <= 0 {
		return 0, ErrInvalidTopUpQuota
	}
	return value, nil
}

func RechargeStpay(tradeNo, callerIP string) error {
	if tradeNo == "" {
		return errors.New("payment order number not provided")
	}
	var quotaToAdd int
	topUp := &TopUp{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockForUpdate(tx).Where("trade_no = ?", tradeNo).First(topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if topUp.PaymentProvider != PaymentProviderStpay {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status == common.TopUpStatusSuccess {
			return nil
		}
		quota, err := stpayCreditQuota(topUp)
		if err != nil {
			return err
		}
		result := tx.Model(&TopUp{}).
			Where("id = ? AND payment_provider = ? AND payment_method = ? AND status IN ?", topUp.Id, PaymentProviderStpay, topUp.PaymentMethod, stpayRetryableStatuses).
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
		if err := creditTopUpQuota(tx, topUp.UserId, quota, nil); err != nil {
			return err
		}
		quotaToAdd = quota
		return nil
	})
	if err != nil {
		common.SysError(common.LogText("stpay topup failed: %s", err.Error()))
		return err
	}
	if quotaToAdd > 0 {
		syncCreditUserQuotaCache(topUp.UserId, quotaToAdd, common.LogText("stpay topup"))
		RecordTopupLog(topUp.UserId, common.NewMessage("{{provider}} top-up succeeded, amount added: {{quota}}, payment amount: {{amount}}", map[string]any{"provider": "Russian SBP", "quota": logger.FormatQuota(quotaToAdd), "amount": fmt.Sprintf("RUB %.2f", topUp.Money)}), callerIP, topUp.PaymentMethod, PaymentMethodStpaySBP)
	}
	return nil
}

// GetPendingStpaySubscriptionOrdersAtOffset lists SBP subscription orders that
// still need reconciliation. Only pending is scanned: the subscription flow
// never writes failed/expired, so a late payment can still complete the order.
func GetPendingStpaySubscriptionOrdersAtOffset(minCreateTime int64, limit, offset int) ([]*SubscriptionOrder, error) {
	if limit <= 0 {
		limit = 50
	}
	offset = max(offset, 0)
	var orders []*SubscriptionOrder
	err := DB.Where("payment_provider = ? AND status = ? AND create_time >= ?", PaymentProviderStpay, common.TopUpStatusPending, minCreateTime).
		Order("create_time asc").Order("id asc").Offset(offset).Limit(limit).Find(&orders).Error
	return orders, err
}

// SaveSubscriptionStpayPayment records the provider trade number and the RUB
// amount quoted to the provider while the order is still pending.
func SaveSubscriptionStpayPayment(orderId int, providerTradeNo, rubAmount string) (bool, error) {
	providerTradeNo = strings.TrimSpace(providerTradeNo)
	rubAmount = strings.TrimSpace(rubAmount)
	if orderId <= 0 || providerTradeNo == "" || rubAmount == "" {
		return false, errors.New("invalid SBP subscription order or provider trade number")
	}
	result := DB.Model(&SubscriptionOrder{}).
		Where("id = ? AND payment_provider = ? AND status = ?", orderId, PaymentProviderStpay, common.TopUpStatusPending).
		Updates(map[string]any{"stpay_trade_no": providerTradeNo, "stpay_amount": rubAmount})
	return result.RowsAffected == 1, result.Error
}

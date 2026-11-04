package controller

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/kpay"
	"github.com/QuantumNous/new-api/setting"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

const kpayUserReconcileCooldown = 20 * time.Second

var (
	kpayReconcileOnce     sync.Once
	kpayReconcileRunning  atomic.Bool
	kpayUserReconcileLast sync.Map
	kpayReconcileSlots    = make(chan struct{}, 8)
)

// StartKpayReconcileTask queries unsettled KPay orders in the background so a
// lost notification still credits the user. kpay_next_query_time is claimed
// with a compare-and-set, so several master nodes never query the same order.
func StartKpayReconcileTask() {
	kpayReconcileOnce.Do(func() {
		if !common.IsMasterNode {
			return
		}
		gopool.Go(func() {
			for {
				time.Sleep(kpayReconcileInterval())
				if !kpayReconcileRunning.CompareAndSwap(false, true) {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				reconcileKpayOrders(ctx, 0, 25)
				cancel()
				kpayReconcileRunning.Store(false)
				cutoff := time.Now().Add(-10 * time.Minute)
				kpayUserReconcileLast.Range(func(key, value any) bool {
					if stamp, ok := value.(time.Time); ok && stamp.Before(cutoff) {
						kpayUserReconcileLast.Delete(key)
					}
					return true
				})
			}
		})
	})
}

func kpayReconcileInterval() time.Duration {
	interval := time.Duration(setting.KpayReconcileInterval) * time.Second
	if interval < 30*time.Second {
		return 120 * time.Second
	}
	return interval
}

// kpayClaimUserReconcile throttles user-triggered reconciliation and bounds
// how many run at once. On success the caller must release the slot.
func kpayClaimUserReconcile(userID int) bool {
	if previous, ok := kpayUserReconcileLast.Load(userID); ok && time.Since(previous.(time.Time)) < kpayUserReconcileCooldown {
		return false
	}
	select {
	case kpayReconcileSlots <- struct{}{}:
	default:
		return false
	}
	kpayUserReconcileLast.Store(userID, time.Now())
	return true
}

// reconcileKpayOrdersForUser runs a bounded, nonblocking reconciliation when
// the user opens payment settings or billing history.
func reconcileKpayOrdersForUser(userID int) {
	if userID <= 0 || !kpayClaimUserReconcile(userID) {
		return
	}
	gopool.Go(func() {
		defer func() { <-kpayReconcileSlots }()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		reconcileKpayOrders(ctx, userID, 5)
	})
}

// RequestKpayReconcile reconciles the caller's orders synchronously, so the
// frontend can refresh the balance after returning from the checkout.
func RequestKpayReconcile(c *gin.Context) {
	userID := c.GetInt("id")
	if userID <= 0 {
		c.Status(http.StatusUnauthorized)
		return
	}
	if !kpayClaimUserReconcile(userID) {
		common.ApiSuccess(c, nil)
		return
	}
	defer func() { <-kpayReconcileSlots }()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	reconcileKpayOrders(ctx, userID, 5)
	common.ApiSuccess(c, nil)
}

// kpayQueryInterval backs off older orders so abandoned checkouts do not keep
// occupying the reconciliation window.
func kpayQueryInterval(order model.KpayPendingOrder, now time.Time) time.Duration {
	interval := kpayReconcileInterval()
	age := now.Sub(time.Unix(order.CreateTime, 0))
	if age > 2*time.Hour {
		return max(interval, time.Hour)
	}
	if age > 10*time.Minute {
		return max(interval, 10*time.Minute)
	}
	return interval
}

// kpayReconcileClient returns the client to query the order with, or the
// reason it cannot be queried this round.
func kpayReconcileClient(order model.KpayPendingOrder) (*kpay.Client, string) {
	if kpayCreditBlocked(order.KpayPaymentFields) {
		return nil, "test_credit_blocked"
	}
	client := kpayClient(order.KpaySandbox)
	if client.MerchantCode != order.KpayMerchantCode || client.AppID != order.KpayAppID {
		return nil, "merchant_snapshot_mismatch"
	}
	if client.Validate() != nil {
		return nil, "client_invalid"
	}
	return client, ""
}

func reconcileKpayOrders(ctx context.Context, userID, limit int) {
	now := time.Now()
	orders, err := model.GetPendingKpayOrders(userID, limit, now.Unix())
	if err != nil {
		logger.LogWarn(ctx, common.LogText("KPay pending-order scan failed error=%q", err.Error()))
		return
	}
	skipped, stale := 0, 0
	for _, order := range orders {
		if ctx.Err() != nil {
			return
		}
		interval := kpayQueryInterval(order, now)
		client, skipReason := kpayReconcileClient(order)
		if skipReason != "" {
			// Unqueryable orders still advance their next query time; otherwise
			// they stay first in line and crowd out queryable orders.
			if _, err := model.ReserveKpayQuery(order, now.Add(min(interval, 10*time.Minute)).Unix()); err != nil {
				stale++
			}
			skipped++
			continue
		}
		claimed, err := model.ReserveKpayQuery(order, now.Add(interval).Unix())
		if err != nil || !claimed {
			stale++
			continue
		}
		if err := reconcileKpayOrder(ctx, order, client); err != nil {
			logger.LogWarn(ctx, common.LogText("KPay order query failed trade_no=%s error=%q", order.TradeNo, err.Error()))
		}
	}
	if skipped > 0 || stale > 0 {
		logger.LogInfo(ctx, common.LogText("KPay reconcile round orders=%d skipped=%d stale=%d", len(orders), skipped, stale))
	}
}

func reconcileKpayOrder(ctx context.Context, order model.KpayPendingOrder, client *kpay.Client) error {
	result, err := client.QueryOrder(ctx, order.KpayOrderNo, order.TradeNo)
	if err != nil {
		return err
	}
	expected, err := decimal.NewFromString(order.KpayAmount)
	if err != nil || !expected.IsPositive() || result.MerchantCode != order.KpayMerchantCode || result.ManagedOutTradeNo != order.TradeNo ||
		result.ManagedOrderNo == "" || (order.KpayOrderNo != "" && result.ManagedOrderNo != order.KpayOrderNo) ||
		!result.LocalPayAmount.Equal(expected) || result.LocalPayCurrency != order.KpayCurrency {
		return errors.New("KPay query response does not match the local order")
	}
	if err := model.SaveKpayOrderNo(order.TradeNo, result.ManagedOrderNo); err != nil {
		return err
	}
	// Failed attempts do not close a managed order; only a paid managed order
	// with one successful payment attempt may fulfill it.
	if result.ManagedOrderState != 2 {
		if result.ManagedOrderState == 3 || result.ManagedOrderState == 5 {
			return model.ExpireKpayOrder(order)
		}
		return nil
	}
	var paid *kpay.PaymentOrder
	for index := range result.PaymentOrderList {
		attempt := &result.PaymentOrderList[index]
		if attempt.Result != 2 {
			continue
		}
		if attempt.OrderNo == "" {
			return errors.New("KPay successful payment identifiers are missing")
		}
		// Amount and currency are optional per attempt; when present they must match.
		if (!attempt.LocalPayAmount.IsZero() && !attempt.LocalPayAmount.Equal(expected)) ||
			(attempt.LocalPayCurrency != "" && attempt.LocalPayCurrency != order.KpayCurrency) {
			return errors.New("KPay successful payment amount mismatch")
		}
		if paid != nil && paid.OrderNo != attempt.OrderNo {
			return errors.New("KPay managed order has multiple successful payments")
		}
		paid = attempt
	}
	if paid == nil {
		return errors.New("KPay paid managed order is missing a successful payment")
	}
	payload, err := common.Marshal(result)
	if err != nil {
		return err
	}
	LockOrder(order.TradeNo)
	defer UnlockOrder(order.TradeNo)
	return completeKpayPayment(order, result.ManagedOrderNo, paid.OrderNo, paid.TransactionNo, string(payload), "kpay-reconcile")
}

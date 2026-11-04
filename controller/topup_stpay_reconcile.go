package controller

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/stpay"

	"github.com/bytedance/gopkg/util/gopool"
)

const (
	stpayReconcileDefaultIntervalMinutes = 2
	// Pending orders older than this are shown as expired; they stay creditable.
	stpayReconcileExpireAfterHours = 72
	// Expired orders are still queried for one more day to catch late payments.
	stpayReconcileLookbackHours = stpayReconcileExpireAfterHours + 24
	stpayReconcileBatchSize     = 50
	stpayReconcileUserBatchSize = 10
	stpayReconcileUserCooldown  = 20 * time.Second
	stpayReconcileUserTimeout   = 8 * time.Second
	stpayReconcileBatchTimeout  = 30 * time.Second
	// New orders are left alone briefly so the notification can arrive first.
	stpayReconcileMinAge = 60 * time.Second
)

var (
	stpayReconcileOnce          sync.Once
	stpayReconcileRunning       atomic.Bool
	stpayReconcileScanOffset    atomic.Int64
	stpayReconcileSubScanOffset atomic.Int64
	stpayReconcileUserLast      sync.Map // userId -> time.Time
	stpayReconcileUserRunning   sync.Map // userId -> struct{}
	stpayReconcileSlots         = make(chan struct{}, 8)
	stpayReconcileAttempts      sync.Map // tradeNo -> time.Time of last query
)

// StartStpayReconcileTask queries unsettled SBP orders so a lost notification
// still credits the user. RechargeStpay and CompleteSubscriptionOrder are
// idempotent, so racing a notification never credits twice.
func StartStpayReconcileTask() {
	stpayReconcileOnce.Do(func() {
		if !common.IsMasterNode || !common.GetEnvOrDefaultBool("STPAY_RECONCILE_TASK_ENABLED", true) {
			return
		}
		intervalMinutes := common.GetEnvOrDefault("STPAY_RECONCILE_TASK_INTERVAL_MINUTES", stpayReconcileDefaultIntervalMinutes)
		if intervalMinutes < 1 {
			intervalMinutes = stpayReconcileDefaultIntervalMinutes
		}
		interval := time.Duration(intervalMinutes) * time.Minute
		gopool.Go(func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				runStpayReconcileOnce()
				<-ticker.C
			}
		})
	})
}

// stpayReconcileConfigured ignores StpayEnabled on purpose: existing pending
// orders may already be paid even after the gateway is switched off.
func stpayReconcileConfigured() bool {
	return common.IsMasterNode && stpayClient().Configured()
}

func runStpayReconcileOnce() {
	if !stpayReconcileRunning.CompareAndSwap(false, true) {
		return
	}
	defer stpayReconcileRunning.Store(false)
	if !stpayReconcileConfigured() {
		return
	}
	now := time.Now()
	minCreateTime := now.Add(-stpayReconcileLookbackHours * time.Hour).Unix()
	cleanupStpayReconcileState(now)
	expireStaleStpayOrders(now)

	topUps := scanStpayBatch(&stpayReconcileScanOffset, func(offset int) ([]*model.TopUp, error) {
		return model.GetPendingStpayTopUpsAtOffset(minCreateTime, stpayReconcileBatchSize, offset)
	})
	dueTopUps := make([]*model.TopUp, 0, len(topUps))
	for _, topUp := range topUps {
		if stpayReconcileDue(topUp.TradeNo, topUp.CreateTime, now) {
			dueTopUps = append(dueTopUps, topUp)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), stpayReconcileBatchTimeout)
	reconcileStpayTopUps(ctx, dueTopUps)
	cancel()

	// Subscriptions get their own budget so top-ups cannot starve them.
	orders := scanStpayBatch(&stpayReconcileSubScanOffset, func(offset int) ([]*model.SubscriptionOrder, error) {
		return model.GetPendingStpaySubscriptionOrdersAtOffset(minCreateTime, stpayReconcileBatchSize, offset)
	})
	dueOrders := make([]*model.SubscriptionOrder, 0, len(orders))
	for _, order := range orders {
		if stpayReconcileDue(order.TradeNo, order.CreateTime, now) {
			dueOrders = append(dueOrders, order)
		}
	}
	subCtx, subCancel := context.WithTimeout(context.Background(), stpayReconcileBatchTimeout)
	reconcileStpaySubscriptionOrders(subCtx, dueOrders)
	subCancel()
}

// scanStpayBatch pages through candidates with a cursor and wraps to the
// start once the end is reached, so new orders keep being scanned.
func scanStpayBatch[T any](cursor *atomic.Int64, fetch func(offset int) ([]T, error)) []T {
	offset := int(cursor.Load())
	items, err := fetch(offset)
	if err == nil && len(items) == 0 && offset > 0 {
		offset = 0
		items, err = fetch(offset)
	}
	if err != nil {
		logger.LogWarn(context.Background(), common.LogText("stpay reconcile: query pending orders failed error=%q", err.Error()))
		return nil
	}
	cursor.Store(int64(offset + len(items)))
	return items
}

// expireStaleStpayOrders only changes the billing display: expired orders and
// subscription bill rows are still credited if a payment arrives later.
func expireStaleStpayOrders(now time.Time) {
	cutoff := now.Add(-stpayReconcileExpireAfterHours * time.Hour).Unix()
	if _, err := model.ExpirePendingStpayTopUps(cutoff); err != nil {
		logger.LogWarn(context.Background(), common.LogText("stpay reconcile: expire stale orders failed error=%q", err.Error()))
	}
	if _, err := model.ExpirePendingSubscriptionTopUps(cutoff); err != nil {
		logger.LogWarn(context.Background(), common.LogText("stpay reconcile: expire stale subscription bills failed error=%q", err.Error()))
	}
}

// stpayReconcileDue backs off older orders and records the attempt before the
// query, so a failed or timed-out query is not retried on the very next round.
func stpayReconcileDue(tradeNo string, createTime int64, now time.Time) bool {
	if createTime <= 0 || strings.TrimSpace(tradeNo) == "" {
		return false
	}
	age := now.Sub(time.Unix(createTime, 0))
	if age < stpayReconcileMinAge {
		return false
	}
	var interval time.Duration
	switch {
	case age >= 2*time.Hour:
		interval = time.Hour
	case age >= 10*time.Minute:
		interval = 10 * time.Minute
	}
	if last, ok := stpayReconcileAttempts.Load(tradeNo); ok && interval > 0 && now.Sub(last.(time.Time)) < interval {
		return false
	}
	stpayReconcileAttempts.Store(tradeNo, now)
	return true
}

func cleanupStpayReconcileState(now time.Time) {
	cutoff := now.Add(-stpayReconcileLookbackHours * time.Hour)
	for _, store := range []*sync.Map{&stpayReconcileAttempts, &stpayReconcileUserLast} {
		store.Range(func(key, value any) bool {
			if stamp, ok := value.(time.Time); ok && stamp.Before(cutoff) {
				store.Delete(key)
			}
			return true
		})
	}
}

// reconcileStpayOrdersForUser runs a bounded background reconciliation when
// the user opens payment settings or billing history. Within the cooldown it
// only runs when an order newer than the last run exists.
func reconcileStpayOrdersForUser(userId int) {
	if userId <= 0 || !stpayReconcileConfigured() {
		return
	}
	select {
	case stpayReconcileSlots <- struct{}{}:
	default:
		return
	}
	if _, loaded := stpayReconcileUserRunning.LoadOrStore(userId, struct{}{}); loaded {
		<-stpayReconcileSlots
		return
	}
	gopool.Go(func() {
		defer func() { <-stpayReconcileSlots }()
		defer stpayReconcileUserRunning.Delete(userId)
		now := time.Now()
		topUps, err := model.GetPendingStpayTopUpsByUser(userId, now.Add(-stpayReconcileLookbackHours*time.Hour).Unix(), stpayReconcileUserBatchSize)
		if err != nil || len(topUps) == 0 {
			return
		}
		if last, ok := stpayReconcileUserLast.Load(userId); ok && now.Sub(last.(time.Time)) < stpayReconcileUserCooldown {
			lastTime := last.(time.Time)
			hasNewer := false
			for _, topUp := range topUps {
				if time.Unix(topUp.CreateTime, 0).After(lastTime) {
					hasNewer = true
					break
				}
			}
			if !hasNewer {
				return
			}
		}
		stpayReconcileUserLast.Store(userId, now)
		ctx, cancel := context.WithTimeout(context.Background(), stpayReconcileUserTimeout)
		defer cancel()
		reconcileStpayTopUps(ctx, topUps)
	})
}

// queryStpayOrder queries one order and returns the response only when it is
// signed, error-free, and bound to the local order.
func queryStpayOrder(ctx context.Context, orderNo, tradeNo, amount string) *stpay.QueryResponse {
	resp, err := stpayClient().Query(ctx, orderNo, tradeNo, amount, stpayCurrency)
	if err != nil {
		logger.LogWarn(ctx, common.LogText("stpay reconcile: query failed order_no=%s error=%q", orderNo, err.Error()))
		return nil
	}
	// errorCode 0001 is a signature error; logging keeps a configuration
	// problem from looking like an unpaid order.
	if strings.TrimSpace(resp.ErrorCode) != "" {
		logger.LogWarn(ctx, common.LogText("stpay reconcile: query rejected order_no=%s error_code=%q error_msg=%q", orderNo, resp.ErrorCode, resp.ErrorMsg))
		return nil
	}
	if !stpayQueryMatches(resp, orderNo, tradeNo, amount) {
		logger.LogWarn(ctx, common.LogText("stpay reconcile: response does not match order order_no=%s", orderNo))
		return nil
	}
	return resp
}

func reconcileStpayTopUps(ctx context.Context, topUps []*model.TopUp) {
	for _, topUp := range topUps {
		if ctx.Err() != nil {
			return
		}
		// A notification may have settled the order since the scan.
		latest := model.GetTopUpByTradeNo(topUp.TradeNo)
		if latest == nil || latest.Status == common.TopUpStatusSuccess || latest.PaymentProvider != model.PaymentProviderStpay {
			continue
		}
		tradeNo := strings.TrimSpace(latest.StpayTradeNo)
		if tradeNo == "" {
			continue
		}
		amount := strconv.FormatFloat(latest.Money, 'f', 2, 64)
		resp := queryStpayOrder(ctx, latest.TradeNo, tradeNo, amount)
		if resp == nil {
			continue
		}
		if !stpay.IsPaid(resp.RespCode, resp.RespMsg) {
			// Still processing restores a failed display state to pending;
			// a definite failure is shown as failed but keeps being queried.
			switch {
			case stpay.IsPending(resp.RespCode, resp.RespMsg) && latest.Status == common.TopUpStatusFailed:
				_ = model.TransitionTopUpStatus(latest.TradeNo, model.PaymentProviderStpay, common.TopUpStatusFailed, common.TopUpStatusPending)
			case !stpay.IsPending(resp.RespCode, resp.RespMsg) && latest.Status == common.TopUpStatusPending:
				_ = model.TransitionTopUpStatus(latest.TradeNo, model.PaymentProviderStpay, common.TopUpStatusPending, common.TopUpStatusFailed)
			}
			continue
		}
		if stpayTestCreditBlocked() {
			continue
		}
		LockOrder(latest.TradeNo)
		err := model.RechargeStpay(latest.TradeNo, "stpay-reconcile")
		UnlockOrder(latest.TradeNo)
		if err != nil {
			logger.LogWarn(ctx, common.LogText("stpay reconcile: recharge failed order_no=%s error=%q", latest.TradeNo, err.Error()))
			continue
		}
		logger.LogInfo(ctx, common.LogText("stpay reconcile: order credited order_no=%s amount=%s RUB", latest.TradeNo, amount))
	}
}

// reconcileStpaySubscriptionOrders never writes failed or expired: a
// subscription order can only complete from pending.
func reconcileStpaySubscriptionOrders(ctx context.Context, orders []*model.SubscriptionOrder) {
	for _, order := range orders {
		if ctx.Err() != nil {
			return
		}
		latest := model.GetSubscriptionOrderByTradeNo(order.TradeNo)
		if latest == nil || latest.Status != common.TopUpStatusPending {
			continue
		}
		tradeNo := strings.TrimSpace(latest.StpayTradeNo)
		amount := strings.TrimSpace(latest.StpayAmount)
		if tradeNo == "" || amount == "" {
			continue
		}
		resp := queryStpayOrder(ctx, latest.TradeNo, tradeNo, amount)
		if resp == nil || !stpay.IsPaid(resp.RespCode, resp.RespMsg) || stpayTestCreditBlocked() {
			continue
		}
		LockOrder(latest.TradeNo)
		err := model.CompleteSubscriptionOrder(latest.TradeNo, common.GetJsonString(resp), model.PaymentProviderStpay, model.PaymentMethodStpaySBP)
		UnlockOrder(latest.TradeNo)
		if err != nil {
			logger.LogWarn(ctx, common.LogText("stpay reconcile: subscription completion failed order_no=%s error=%q", latest.TradeNo, err.Error()))
			continue
		}
		logger.LogInfo(ctx, common.LogText("stpay reconcile: subscription completed order_no=%s amount=%s RUB", latest.TradeNo, amount))
	}
}

package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/pkg/logger"
)

// OrderReconciliation polls orders that have been pending for too long, asks
// Stripe for ground truth, and force-confirms or fails them. This catches
// dropped webhooks (Stripe should have delivered, but didn't) and partial
// outages.
// orderReconciliation implements Reconciler.
type orderReconciliation struct {
	Orders    repository.OrderRepository
	Stripe    stripe.Client
	Publisher kafka.EventPublisher

	Interval   time.Duration
	StuckAfter time.Duration
	BatchSize  int
}

// ReconciliationOptions tunes the reconciler loop. Zero values fall back to
// the production defaults (5m / 10m / 500).
type ReconciliationOptions struct {
	Interval   time.Duration
	StuckAfter time.Duration
	BatchSize  int
}

func NewOrderReconciliation(orders repository.OrderRepository, s stripe.Client, p kafka.EventPublisher) Reconciler {
	return NewOrderReconciliationWithOptions(orders, s, p, ReconciliationOptions{})
}

func NewOrderReconciliationWithOptions(orders repository.OrderRepository, s stripe.Client, p kafka.EventPublisher, opts ReconciliationOptions) Reconciler {
	r := &orderReconciliation{
		Orders: orders, Stripe: s, Publisher: p,
		Interval:   opts.Interval,
		StuckAfter: opts.StuckAfter,
		BatchSize:  opts.BatchSize,
	}
	if r.Interval == 0 {
		r.Interval = 5 * time.Minute
	}
	if r.StuckAfter == 0 {
		r.StuckAfter = 10 * time.Minute
	}
	if r.BatchSize == 0 {
		r.BatchSize = 500
	}
	return r
}

func (r *orderReconciliation) Run(ctx context.Context) error {
	log := logger.L().With("component", "order_reconciler")
	tk := time.NewTicker(r.Interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tk.C:
			if err := r.tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Warn("reconcile tick failed", "err", err)
			}
		}
	}
}

func (r *orderReconciliation) tick(ctx context.Context) error {
	cutoff := time.Now().UTC().Add(-r.StuckAfter)
	stuck, err := r.Orders.GetPendingBefore(ctx, cutoff, r.BatchSize)
	if err != nil {
		return fmt.Errorf("list stuck orders: %w", err)
	}
	for _, o := range stuck {
		r.reconcileOne(ctx, o)
	}
	return nil
}

func (r *orderReconciliation) reconcileOne(ctx context.Context, o *domain.Order) {
	log := logger.L().With("order_id", o.OrderID, "merchant_id", o.MerchantID)
	if o.StripePaymentIntentID == "" {
		// Crypto orders are reconciled by the blockchain Reconciler; nothing
		// to do here.
		return
	}
	pi, err := r.Stripe.GetPaymentIntent(ctx, o.StripePaymentIntentID)
	if err != nil {
		log.Warn("stripe get failed", "err", err)
		return
	}
	switch pi.Status {
	case "succeeded":
		if err := r.Orders.UpdateStatus(ctx, o.OrderID, domain.OrderStatusPaid, pi.ID); err != nil {
			log.Warn("force confirm failed", "err", err)
			return
		}
		_ = r.Publisher.PublishPaymentConfirmed(ctx, kafka.PaymentConfirmedEvent{
			OrderID:               o.OrderID,
			MerchantID:            o.MerchantID,
			Status:                string(domain.OrderStatusPaid),
			StripePaymentIntentID: pi.ID,
			Amount:                o.Amount,
			Currency:              o.Currency,
			CallbackURL:           o.CallbackURL,
			ConfirmedAt:           time.Now().UTC().Unix(),
		})
		log.Info("force-confirmed via reconciliation")
	case "canceled", "requires_payment_method":
		_ = r.Orders.UpdateStatus(ctx, o.OrderID, domain.OrderStatusFailed, pi.ID)
	}
}

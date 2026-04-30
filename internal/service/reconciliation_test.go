package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/quangdangfit/easypay/internal/domain"
)

func TestOrderReconciliation_TickForceConfirms(t *testing.T) {
	order := &domain.Order{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1", Status: domain.OrderStatusPending}
	repo := newOrderStore(t, order)
	repo.pending = []*domain.Order{order}
	pub := newEventCapture(t)
	r := &orderReconciliation{Orders: repo.mock, Stripe: newWebhookStripe(t, webhookStripeOpts{}), Publisher: pub.mock, Interval: time.Hour, StuckAfter: time.Minute, BatchSize: 10}

	if err := r.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if order.Status != domain.OrderStatusPaid {
		t.Fatalf("expected paid, got %s", order.Status)
	}
	if len(pub.confirmed) != 1 {
		t.Fatalf("expected 1 confirmation, got %d", len(pub.confirmed))
	}
}

func TestOrderReconciliation_TickIgnoresCryptoOrders(t *testing.T) {
	order := &domain.Order{OrderID: "ord-1", MerchantID: "M1", Status: domain.OrderStatusPending} // no PI
	repo := newOrderStore(t, order)
	repo.pending = []*domain.Order{order}
	r := &orderReconciliation{Orders: repo.mock, Stripe: newWebhookStripe(t, webhookStripeOpts{}), Publisher: newEventCapture(t).mock, BatchSize: 5}
	if err := r.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if order.Status != domain.OrderStatusPending {
		t.Fatalf("crypto order shouldn't change: %s", order.Status)
	}
}

func TestOrderReconciliation_TickFailsCanceled(t *testing.T) {
	order := &domain.Order{OrderID: "ord-1", MerchantID: "M1", StripePaymentIntentID: "pi_x", Status: domain.OrderStatusPending}
	repo := newOrderStore(t, order)
	repo.pending = []*domain.Order{order}
	r := &orderReconciliation{Orders: repo.mock, Stripe: newWebhookStripe(t, webhookStripeOpts{piStatus: "canceled"}), Publisher: newEventCapture(t).mock, BatchSize: 5}
	_ = r.tick(context.Background())
	if order.Status != domain.OrderStatusFailed {
		t.Fatalf("expected failed, got %s", order.Status)
	}
}

func TestOrderReconciliation_TickIgnoresGetError(t *testing.T) {
	// Stripe lookup fails — the loop logs and continues without changing state.
	order := &domain.Order{OrderID: "ord-1", MerchantID: "M1", StripePaymentIntentID: "pi_x", Status: domain.OrderStatusPending}
	repo := newOrderStore(t, order)
	repo.pending = []*domain.Order{order}
	r := &orderReconciliation{Orders: repo.mock, Stripe: newWebhookStripe(t, webhookStripeOpts{piErr: errors.New("network")}), Publisher: newEventCapture(t).mock, BatchSize: 5}
	_ = r.tick(context.Background())
	if order.Status != domain.OrderStatusPending {
		t.Fatalf("expected unchanged, got %s", order.Status)
	}
}

func TestNewOrderReconciliation_DefaultsApplied(t *testing.T) {
	r := NewOrderReconciliation(newOrderStore(t).mock, newWebhookStripe(t, webhookStripeOpts{}), newEventCapture(t).mock)
	or := r.(*orderReconciliation)
	if or.Interval != 5*time.Minute {
		t.Errorf("Interval=%v", or.Interval)
	}
	if or.StuckAfter != 10*time.Minute {
		t.Errorf("StuckAfter=%v", or.StuckAfter)
	}
	if or.BatchSize != 500 {
		t.Errorf("BatchSize=%d", or.BatchSize)
	}
}

func TestOrderReconciliation_RunReturnsOnCancel(t *testing.T) {
	or := &orderReconciliation{
		Orders:    newOrderStore(t).mock,
		Stripe:    newWebhookStripe(t, webhookStripeOpts{}),
		Publisher: newEventCapture(t).mock,
		Interval:  10 * time.Millisecond,
		BatchSize: 5,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := or.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestOrderReconciliation_RunInvokesTick(t *testing.T) {
	order := &domain.Order{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1", Status: domain.OrderStatusPending}
	repo := newOrderStore(t, order)
	repo.pending = []*domain.Order{order}
	pub := newEventCapture(t)
	or := &orderReconciliation{
		Orders:    repo.mock,
		Stripe:    newWebhookStripe(t, webhookStripeOpts{}),
		Publisher: pub.mock,
		Interval:  5 * time.Millisecond,
		BatchSize: 5,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_ = or.Run(ctx)
	if order.Status != domain.OrderStatusPaid {
		t.Fatalf("expected tick to fire and confirm, got %s", order.Status)
	}
}

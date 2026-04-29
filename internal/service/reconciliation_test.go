package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/quangdangfit/easypay/internal/domain"
)

// extends fakeOrderRepo with stub for GetPendingBefore
type pendingFakeRepo struct {
	*fakeOrderRepo
	pending []*domain.Order
}

func (r *pendingFakeRepo) GetPendingBefore(ctx context.Context, _ time.Time, _ int) ([]*domain.Order, error) {
	return r.pending, nil
}

func TestOrderReconciliation_TickForceConfirms(t *testing.T) {
	repo := &fakeOrderRepo{byID: map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1", Status: domain.OrderStatusPending},
	}}
	pendingRepo := &pendingFakeRepo{
		fakeOrderRepo: repo,
		pending:       []*domain.Order{repo.byID["ORD-1"]},
	}
	pub := &fakePublisher{}
	sf := &stripeWebhookFake{}
	r := &orderReconciliation{Orders: pendingRepo, Stripe: sf, Publisher: pub, Interval: time.Hour, StuckAfter: time.Minute, BatchSize: 10}

	if err := r.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if repo.byID["ORD-1"].Status != domain.OrderStatusPaid {
		t.Fatalf("expected paid, got %s", repo.byID["ORD-1"].Status)
	}
	if len(pub.confirmed) != 1 {
		t.Fatalf("expected 1 confirmation, got %d", len(pub.confirmed))
	}
}

func TestOrderReconciliation_TickIgnoresCryptoOrders(t *testing.T) {
	repo := &fakeOrderRepo{byID: map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", MerchantID: "M1", Status: domain.OrderStatusPending}, // no PI
	}}
	pendingRepo := &pendingFakeRepo{fakeOrderRepo: repo, pending: []*domain.Order{repo.byID["ORD-1"]}}
	r := &orderReconciliation{Orders: pendingRepo, Stripe: &stripeWebhookFake{}, Publisher: &fakePublisher{}, BatchSize: 5}
	if err := r.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.byID["ORD-1"].Status != domain.OrderStatusPending {
		t.Fatalf("crypto order shouldn't change: %s", repo.byID["ORD-1"].Status)
	}
}

func TestOrderReconciliation_TickFailsCanceled(t *testing.T) {
	repo := &fakeOrderRepo{byID: map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", MerchantID: "M1", StripePaymentIntentID: "pi_x", Status: domain.OrderStatusPending},
	}}
	pendingRepo := &pendingFakeRepo{fakeOrderRepo: repo, pending: []*domain.Order{repo.byID["ORD-1"]}}
	sf := &stripeWebhookFake{piStatus: "canceled"}
	r := &orderReconciliation{Orders: pendingRepo, Stripe: sf, Publisher: &fakePublisher{}, BatchSize: 5}
	_ = r.tick(context.Background())
	if repo.byID["ORD-1"].Status != domain.OrderStatusFailed {
		t.Fatalf("expected failed, got %s", repo.byID["ORD-1"].Status)
	}
}

func TestOrderReconciliation_TickIgnoresGetError(t *testing.T) {
	// Stripe lookup fails — the loop logs and continues without changing state.
	repo := &fakeOrderRepo{byID: map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", MerchantID: "M1", StripePaymentIntentID: "pi_x", Status: domain.OrderStatusPending},
	}}
	pendingRepo := &pendingFakeRepo{fakeOrderRepo: repo, pending: []*domain.Order{repo.byID["ORD-1"]}}
	sf := &stripeWebhookFake{piErr: errors.New("network")}
	r := &orderReconciliation{Orders: pendingRepo, Stripe: sf, Publisher: &fakePublisher{}, BatchSize: 5}
	_ = r.tick(context.Background())
	if repo.byID["ORD-1"].Status != domain.OrderStatusPending {
		t.Fatalf("expected unchanged, got %s", repo.byID["ORD-1"].Status)
	}
}

func TestNewOrderReconciliation_DefaultsApplied(t *testing.T) {
	r := NewOrderReconciliation(&fakeOrderRepo{}, &stripeWebhookFake{}, &fakePublisher{})
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
		Orders:    &pendingFakeRepo{fakeOrderRepo: &fakeOrderRepo{}},
		Stripe:    &stripeWebhookFake{},
		Publisher: &fakePublisher{},
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
	repo := &fakeOrderRepo{byID: map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1", Status: domain.OrderStatusPending},
	}}
	pendingRepo := &pendingFakeRepo{fakeOrderRepo: repo, pending: []*domain.Order{repo.byID["ORD-1"]}}
	pub := &fakePublisher{}
	or := &orderReconciliation{
		Orders:    pendingRepo,
		Stripe:    &stripeWebhookFake{},
		Publisher: pub,
		Interval:  5 * time.Millisecond,
		BatchSize: 5,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_ = or.Run(ctx)
	if repo.byID["ORD-1"].Status != domain.OrderStatusPaid {
		t.Fatalf("expected tick to fire and confirm, got %s", repo.byID["ORD-1"].Status)
	}
}

package service

import (
	"context"
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

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/quangdangfit/easypay/internal/domain"
	repomock "github.com/quangdangfit/easypay/internal/mocks/repo"
)

func TestOrderReconciliation_TickForceConfirms(t *testing.T) {
	order := &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1", Status: domain.TransactionStatusPending}
	repo := newTxStore(t, order)
	repo.pending = []*domain.Transaction{order}
	pub := newEventCapture(t)
	r := &orderReconciliation{Orders: repo.mock, Stripe: newWebhookStripe(t, webhookStripeOpts{}), Publisher: pub.mock, Interval: time.Hour, StuckAfter: time.Minute, BatchSize: 10}

	if err := r.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if order.Status != domain.TransactionStatusPaid {
		t.Fatalf("expected paid, got %s", order.Status)
	}
	if len(pub.confirmed) != 1 {
		t.Fatalf("expected 1 confirmation, got %d", len(pub.confirmed))
	}
}

func TestOrderReconciliation_TickIgnoresCryptoOrders(t *testing.T) {
	order := &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Status: domain.TransactionStatusPending} // no PI
	repo := newTxStore(t, order)
	repo.pending = []*domain.Transaction{order}
	r := &orderReconciliation{Orders: repo.mock, Stripe: newWebhookStripe(t, webhookStripeOpts{}), Publisher: newEventCapture(t).mock, BatchSize: 5}
	if err := r.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if order.Status != domain.TransactionStatusPending {
		t.Fatalf("crypto order shouldn't change: %s", order.Status)
	}
}

func TestOrderReconciliation_TickFailsCanceled(t *testing.T) {
	order := &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", StripePaymentIntentID: "pi_x", Status: domain.TransactionStatusPending}
	repo := newTxStore(t, order)
	repo.pending = []*domain.Transaction{order}
	r := &orderReconciliation{Orders: repo.mock, Stripe: newWebhookStripe(t, webhookStripeOpts{piStatus: "canceled"}), Publisher: newEventCapture(t).mock, BatchSize: 5}
	_ = r.tick(context.Background())
	if order.Status != domain.TransactionStatusFailed {
		t.Fatalf("expected failed, got %s", order.Status)
	}
}

func TestOrderReconciliation_TickIgnoresGetError(t *testing.T) {
	// Stripe lookup fails — the loop logs and continues without changing state.
	order := &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", StripePaymentIntentID: "pi_x", Status: domain.TransactionStatusPending}
	repo := newTxStore(t, order)
	repo.pending = []*domain.Transaction{order}
	r := &orderReconciliation{Orders: repo.mock, Stripe: newWebhookStripe(t, webhookStripeOpts{piErr: errors.New("network")}), Publisher: newEventCapture(t).mock, BatchSize: 5}
	_ = r.tick(context.Background())
	if order.Status != domain.TransactionStatusPending {
		t.Fatalf("expected unchanged, got %s", order.Status)
	}
}

func TestNewOrderReconciliation_DefaultsApplied(t *testing.T) {
	r := NewOrderReconciliation(newTxStore(t).mock, newWebhookStripe(t, webhookStripeOpts{}), newEventCapture(t).mock)
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
		Orders:    newTxStore(t).mock,
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
	order := &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1", Status: domain.TransactionStatusPending}
	repo := newTxStore(t, order)
	repo.pending = []*domain.Transaction{order}
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
	if order.Status != domain.TransactionStatusPaid {
		t.Fatalf("expected tick to fire and confirm, got %s", order.Status)
	}
}

func TestOrderReconciliation_TickFailsRequiresPaymentMethod(t *testing.T) {
	order := &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", StripePaymentIntentID: "pi_x", Status: domain.TransactionStatusPending}
	repo := newTxStore(t, order)
	repo.pending = []*domain.Transaction{order}
	r := &orderReconciliation{Orders: repo.mock, Stripe: newWebhookStripe(t, webhookStripeOpts{piStatus: "requires_payment_method"}), Publisher: newEventCapture(t).mock, BatchSize: 5}
	_ = r.tick(context.Background())
	if order.Status != domain.TransactionStatusFailed {
		t.Fatalf("expected failed, got %s", order.Status)
	}
}

func TestOrderReconciliation_TickGetPendingError(t *testing.T) {
	ctrl := gomock.NewController(t)
	orders := repomock.NewMockTransactionRepository(ctrl)
	orders.EXPECT().GetPendingBefore(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errors.New("db unavailable"))

	r := &orderReconciliation{Orders: orders, Stripe: newWebhookStripe(t, webhookStripeOpts{}), Publisher: newEventCapture(t).mock, BatchSize: 5}
	err := r.tick(context.Background())
	if err == nil || !strings.Contains(err.Error(), "list stuck orders") {
		t.Fatalf("expected list error, got %v", err)
	}
}

func TestOrderReconciliation_TickSuccessUpdateError(t *testing.T) {
	ctrl := gomock.NewController(t)
	orders := repomock.NewMockTransactionRepository(ctrl)
	order := &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1", Status: domain.TransactionStatusPending, ShardIndex: 0}
	orders.EXPECT().GetPendingBefore(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]*domain.Transaction{order}, nil)
	orders.EXPECT().UpdateStatus(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(errors.New("update failed"))

	r := &orderReconciliation{Orders: orders, Stripe: newWebhookStripe(t, webhookStripeOpts{}), Publisher: newEventCapture(t).mock, BatchSize: 5}
	err := r.tick(context.Background())
	if err != nil {
		t.Fatalf("tick should not propagate update error, got %v", err)
	}
	// Status should remain unchanged since update failed
	if order.Status != domain.TransactionStatusPending {
		t.Fatalf("expected pending, got %s", order.Status)
	}
}

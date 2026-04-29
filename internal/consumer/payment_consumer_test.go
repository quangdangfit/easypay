package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
)

func TestPaymentConsumer_BatchHappyPath(t *testing.T) {
	repo := &batchRepo{}
	c := NewPaymentConsumer(repo)
	msgs := []kafkago.Message{
		mustEvent(t, "ORD-1", "M1", "TXN-1"),
		mustEvent(t, "ORD-2", "M1", "TXN-2"),
	}
	if err := c.Handle(context.Background(), msgs); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(repo.created) != 2 {
		t.Fatalf("expected 2 created, got %d", len(repo.created))
	}
}

func TestPaymentConsumer_HandleOneTreatsDuplicateAsSuccess(t *testing.T) {
	repo := &batchRepo{createErr: errors.New("Error 1062: Duplicate entry 'ORD-1' for key 'order_id'")}
	c := NewPaymentConsumer(repo)
	if err := c.HandleOne(context.Background(), mustEvent(t, "ORD-1", "M1", "TXN-1")); err != nil {
		t.Fatalf("expected nil for duplicate, got %v", err)
	}
}

func TestPaymentConsumer_RejectsMalformed(t *testing.T) {
	c := NewPaymentConsumer(&batchRepo{})
	err := c.Handle(context.Background(), []kafkago.Message{{Value: []byte(`{"order_id":""}`)}})
	if err == nil {
		t.Fatal("expected error for malformed message")
	}
}

func mustEvent(t *testing.T, orderID, merchantID, txID string) kafkago.Message {
	t.Helper()
	b, err := json.Marshal(kafka.PaymentEvent{
		OrderID: orderID, MerchantID: merchantID, TransactionID: txID,
		Amount: 1000, Currency: "USD", Status: "pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	return kafkago.Message{Value: b}
}

// batchRepo is the in-test repository; matches repository.OrderRepository.
// Defined separately so we can satisfy the time.Time signature without
// importing it into the test file's main path.
type batchRepo struct {
	created   []*domain.Order
	batchErr  error
	createErr error
}

func (r *batchRepo) Create(ctx context.Context, o *domain.Order) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.created = append(r.created, o)
	return nil
}
func (r *batchRepo) GetByOrderID(ctx context.Context, id string) (*domain.Order, error)         { return nil, nil }
func (r *batchRepo) GetByPaymentIntentID(ctx context.Context, p string) (*domain.Order, error)  { return nil, nil }
func (r *batchRepo) UpdateStatus(ctx context.Context, id string, s domain.OrderStatus, p string) error { return nil }
func (r *batchRepo) UpdateCheckout(ctx context.Context, id, ssid, pi, url string) error { return nil }
func (r *batchRepo) BatchCreate(ctx context.Context, orders []*domain.Order) error {
	if r.batchErr != nil {
		return r.batchErr
	}
	r.created = append(r.created, orders...)
	return nil
}
func (r *batchRepo) GetPendingBefore(ctx context.Context, _ time.Time, _ int) ([]*domain.Order, error) {
	return nil, nil
}

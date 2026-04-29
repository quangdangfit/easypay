package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	kafkago "github.com/segmentio/kafka-go"
	"go.uber.org/mock/gomock"

	"github.com/quangdangfit/easypay/internal/config"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	repomock "github.com/quangdangfit/easypay/internal/mocks/repo"
)

func testKafkaCfg() config.KafkaConfig {
	return config.KafkaConfig{
		Brokers:        []string{"127.0.0.1:0"},
		TopicEvents:    "payment.events",
		TopicConfirmed: "payment.confirmed",
		TopicDLQ:       "payment.events.dlq",
		ConsumerGroup:  "test",
	}
}

func TestPaymentConsumer_BatchHappyPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := repomock.NewMockOrderRepository(ctrl)
	var inserted []*domain.Order
	repo.EXPECT().BatchCreate(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, orders []*domain.Order) error {
			inserted = append(inserted, orders...)
			return nil
		})

	c := NewPaymentConsumer(repo)
	msgs := []kafkago.Message{
		mustEvent(t, "ORD-1", "M1", "TXN-1"),
		mustEvent(t, "ORD-2", "M1", "TXN-2"),
	}
	if err := c.Handle(context.Background(), msgs); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(inserted) != 2 {
		t.Fatalf("expected 2 inserted, got %d", len(inserted))
	}
}

func TestPaymentConsumer_HandleOneTreatsDuplicateAsSuccess(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := repomock.NewMockOrderRepository(ctrl)
	repo.EXPECT().Create(gomock.Any(), gomock.Any()).
		Return(errors.New("Error 1062: Duplicate entry 'ORD-1' for key 'order_id'"))

	c := NewPaymentConsumer(repo)
	if err := c.HandleOne(context.Background(), mustEvent(t, "ORD-1", "M1", "TXN-1")); err != nil {
		t.Fatalf("expected nil for duplicate, got %v", err)
	}
}

func TestPaymentConsumer_RejectsMalformed(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := repomock.NewMockOrderRepository(ctrl)
	// Decode fails before BatchCreate is called.

	c := NewPaymentConsumer(repo)
	err := c.Handle(context.Background(), []kafkago.Message{{Value: []byte(`{"order_id":""}`)}})
	if err == nil {
		t.Fatal("expected error for malformed message")
	}
}

func TestPaymentConsumer_NewBatchWiresUp(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := repomock.NewMockOrderRepository(ctrl)
	c := NewPaymentConsumer(repo)
	bc := c.NewBatch(testKafkaCfg())
	if bc == nil {
		t.Fatal("nil batch consumer")
	}
}

func TestIsDuplicateError(t *testing.T) {
	if !isDuplicateError(errors.New("Error 1062: Duplicate entry 'x'")) {
		t.Error("1062 not detected")
	}
	if !isDuplicateError(errors.New("Duplicate entry 'x' for key 'y'")) {
		t.Error("Duplicate entry phrase not detected")
	}
	if isDuplicateError(errors.New("connection refused")) {
		t.Error("non-dup matched")
	}
	if isDuplicateError(nil) {
		t.Error("nil matched")
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

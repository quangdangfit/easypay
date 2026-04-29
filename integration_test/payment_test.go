//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/quangdangfit/easypay/internal/cache"
	"github.com/quangdangfit/easypay/internal/config"
	"github.com/quangdangfit/easypay/internal/consumer"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/internal/service"
)

// TestCreateOrder_HappyPath exercises the full sync + async flow: payment
// service publishes to Kafka, the payment_consumer batch-inserts into MySQL.
func TestCreateOrder_HappyPath(t *testing.T) {
	env := SetupEnv(t)
	defer env.Cleanup(t)

	idem := cache.NewIdempotency(env.Redis)
	stripeMock := NewMockStripe()
	kafkaCfg := config.KafkaConfig{
		Brokers:        env.KafkaBrokers,
		TopicEvents:    "payment.events",
		TopicConfirmed: "payment.confirmed",
		TopicDLQ:       "payment.events.dlq",
		ConsumerGroup:  "test-group-" + t.Name(),
	}
	publisher := kafka.NewPublisher(kafkaCfg)
	defer publisher.Close()

	svc := service.NewPaymentService(idem, stripeMock, publisher, nil, service.PaymentServiceOptions{
		DefaultCurrency: "USD",
		CryptoContract:  "0xCONTRACT",
		CryptoChainID:   11155111,
	})

	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s", RateLimit: 100}
	res, err := svc.Create(context.Background(), service.CreatePaymentInput{
		Merchant: merchant, TransactionID: "TXN-INT-1", Amount: 1500, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.OrderID == "" || res.CheckoutURL == "" {
		t.Fatalf("missing fields: %+v", res)
	}

	// Run consumer briefly to drain the message into MySQL.
	orderRepo := repository.NewOrderRepository(env.DB)
	pc := consumer.NewPaymentConsumer(orderRepo).NewBatch(kafkaCfg)
	pc.BatchSize = 5
	pc.BatchWait = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go pc.Run(ctx)

	// Poll for the row.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		o, err := orderRepo.GetByOrderID(context.Background(), res.OrderID)
		if err == nil && o.OrderID == res.OrderID {
			if o.Amount != 1500 {
				t.Fatalf("amount: got %d want 1500", o.Amount)
			}
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("order %s not seen in MySQL within timeout", res.OrderID)
}

// TestIdempotency_DuplicateOrder verifies that the same {merchant, txn} returns
// the same order_id and only emits one Kafka event, even on duplicate calls.
func TestIdempotency_DuplicateOrder(t *testing.T) {
	env := SetupEnv(t)
	defer env.Cleanup(t)

	idem := cache.NewIdempotency(env.Redis)
	stripeMock := NewMockStripe()
	kafkaCfg := config.KafkaConfig{
		Brokers:        env.KafkaBrokers,
		TopicEvents:    "payment.events.idem",
		TopicConfirmed: "payment.confirmed.idem",
		TopicDLQ:       "payment.events.dlq.idem",
		ConsumerGroup:  "test-group-idem",
	}
	publisher := kafka.NewPublisher(kafkaCfg)
	defer publisher.Close()

	svc := service.NewPaymentService(idem, stripeMock, publisher, nil, service.PaymentServiceOptions{
		DefaultCurrency: "USD",
		CryptoContract:  "0xCONTRACT",
		CryptoChainID:   11155111,
	})
	merchant := &domain.Merchant{MerchantID: "M_IDEM", SecretKey: "s", RateLimit: 100}
	in := service.CreatePaymentInput{
		Merchant: merchant, TransactionID: "TXN-DUPE", Amount: 999, Currency: "USD",
	}

	first, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.OrderID != second.OrderID {
		t.Fatalf("ids differ across idempotent calls: %s vs %s", first.OrderID, second.OrderID)
	}
	if stripeMock.CheckoutCount != 1 {
		t.Fatalf("Stripe should be called once for duplicates, got %d", stripeMock.CheckoutCount)
	}

	// Verify exactly one event on the topic. We read from partition 0
	// directly (no consumer group) so we always see prior messages — using a
	// new consumer group would default to StartOffset=LastOffset and skip
	// everything already produced.
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:   env.KafkaBrokers,
		Topic:     kafkaCfg.TopicEvents,
		Partition: 0,
		MinBytes:  1,
		MaxBytes:  10e6,
		MaxWait:   time.Second,
	})
	defer func() { _ = reader.Close() }()
	if err := reader.SetOffset(kafkago.FirstOffset); err != nil {
		t.Fatalf("set offset: %v", err)
	}

	count := 0
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		m, err := reader.ReadMessage(ctx)
		cancel()
		if err != nil {
			break
		}
		var ev kafka.PaymentEvent
		_ = json.Unmarshal(m.Value, &ev)
		if ev.TransactionID == "TXN-DUPE" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 event for TXN-DUPE, got %d", count)
	}
}

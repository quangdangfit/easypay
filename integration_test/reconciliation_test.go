//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/quangdangfit/easypay/internal/config"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/internal/service"
)

// TestReconciliationCron seeds a stuck pending order, points the reconciler
// at a Stripe mock that reports the PI as succeeded, and verifies that the
// reconciler force-confirms the order and emits a payment.confirmed event —
// the recovery path for missed Stripe webhooks.
func TestReconciliationCron(t *testing.T) {
	env := SetupEnv(t)
	defer env.Cleanup(t)

	orderRepo := repository.NewOrderRepository(env.DB)

	const merchantID = "M_RECON"
	stuck := SeedOrder(t, orderRepo, merchantID, "RECON-1", 2500, domain.OrderStatusPending, func(o *domain.Order) {
		o.StripePaymentIntentID = "pi_recon_1"
	})
	// Backdate created_at so the reconciler considers the row stuck.
	BackdateOrder(t, env.DB, merchantID, stuck.OrderID, time.Hour)

	mock := NewMockStripe()

	kafkaCfg := config.KafkaConfig{
		Brokers:        env.KafkaBrokers,
		TopicConfirmed: "payment.confirmed.recon",
		TopicDLQ:       "payment.confirmed.dlq.recon",
		ConsumerGroup:  "recon-group",
	}
	publisher := kafka.NewPublisher(kafkaCfg)
	defer func() { _ = publisher.Close() }()

	reconciler := service.NewOrderReconciliationWithOptions(
		orderRepo, mock, publisher,
		service.ReconciliationOptions{
			Interval:   100 * time.Millisecond,
			StuckAfter: time.Minute,
			BatchSize:  10,
		},
	)

	// Set up the reader BEFORE starting the reconciler so we don't miss the
	// publish, and so we don't have to worry about the publish racing with
	// our cancel — the publish call uses the reconciler's ctx, and on slow CI
	// Kafka the cancel can land before WriteMessages returns.
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:   env.KafkaBrokers,
		Topic:     kafkaCfg.TopicConfirmed,
		Partition: 0,
		MinBytes:  1,
		MaxBytes:  10e6,
		MaxWait:   500 * time.Millisecond,
	})
	defer func() { _ = reader.Close() }()
	if err := reader.SetOffset(kafkago.FirstOffset); err != nil {
		t.Fatalf("set offset: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = reconciler.Run(ctx)
		close(done)
	}()

	found := false
	deadline := time.Now().Add(20 * time.Second)
	for !found && time.Now().Before(deadline) {
		rctx, rcancel := context.WithTimeout(ctx, 2*time.Second)
		m, err := reader.ReadMessage(rctx)
		rcancel()
		if err != nil {
			continue
		}
		var ev kafka.PaymentConfirmedEvent
		if json.Unmarshal(m.Value, &ev) == nil && ev.OrderID == stuck.OrderID {
			found = true
		}
	}
	cancel()
	<-done

	if !found {
		t.Fatal("payment.confirmed event for reconciled order not found")
	}

	o, err := orderRepo.GetByOrderID(context.Background(), stuck.OrderID)
	if err != nil {
		t.Fatalf("read order: %v", err)
	}
	if o.Status != domain.OrderStatusPaid {
		t.Fatalf("status: got %s want paid", o.Status)
	}
}

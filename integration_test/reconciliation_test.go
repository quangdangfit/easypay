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

	// Seed a 'pending' order with a backdated created_at. We rely on the
	// reconciler's StuckAfter knob to consider it stuck — backdating the row
	// in DB sidesteps the production 10-minute wait.
	stuck := &domain.Order{
		OrderID:               "ORD-RECON-1",
		MerchantID:            "M_RECON",
		TransactionID:         "TXN-RECON-1",
		Amount:                2500,
		Currency:              "USD",
		Status:                domain.OrderStatusPending,
		StripePaymentIntentID: "pi_recon_1",
		CallbackURL:           "https://merchant.test/cb",
	}
	if err := orderRepo.Create(context.Background(), stuck); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Force created_at to 1h ago so the order is "stuck".
	if _, err := env.DB.ExecContext(context.Background(),
		`UPDATE orders SET created_at = ? WHERE order_id = ?`,
		time.Now().Add(-time.Hour), stuck.OrderID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	mock := NewMockStripe()

	kafkaCfg := config.KafkaConfig{
		Brokers:        env.KafkaBrokers,
		TopicEvents:    "payment.events.recon",
		TopicConfirmed: "payment.confirmed.recon",
		TopicDLQ:       "payment.events.dlq.recon",
		ConsumerGroup:  "recon-group",
	}
	publisher := kafka.NewPublisher(kafkaCfg)
	defer publisher.Close()

	reconciler := service.NewOrderReconciliationWithOptions(
		orderRepo, mock, publisher,
		service.ReconciliationOptions{
			Interval:   100 * time.Millisecond,
			StuckAfter: time.Minute,
			BatchSize:  10,
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = reconciler.Run(ctx)
		close(done)
	}()

	// Poll until the order flips to 'paid' (the reconciler's first tick fires
	// after Interval, so this should happen within ~500ms).
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		o, err := orderRepo.GetByOrderID(context.Background(), stuck.OrderID)
		if err == nil && o.Status == domain.OrderStatusPaid {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done

	o, err := orderRepo.GetByOrderID(context.Background(), stuck.OrderID)
	if err != nil {
		t.Fatalf("read order: %v", err)
	}
	if o.Status != domain.OrderStatusPaid {
		t.Fatalf("status: got %s want paid", o.Status)
	}

	// Verify a payment.confirmed event was published.
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

	found := false
	deadline = time.Now().Add(10 * time.Second)
	for !found && time.Now().Before(deadline) {
		rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
		m, err := reader.ReadMessage(rctx)
		rcancel()
		if err != nil {
			break
		}
		var ev kafka.PaymentConfirmedEvent
		if json.Unmarshal(m.Value, &ev) == nil && ev.OrderID == stuck.OrderID {
			found = true
		}
	}
	if !found {
		t.Fatal("payment.confirmed event for reconciled order not found")
	}
}

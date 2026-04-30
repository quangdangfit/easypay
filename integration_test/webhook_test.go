//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/quangdangfit/easypay/internal/config"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/internal/service"
)

const webhookSecret = "whsec_int_test"

// TestStripeWebhookFlow exercises the webhook pipeline end-to-end against
// MySQL + Redis + Kafka. Each subtest seeds its own order and event ID so
// they don't step on each other.
func TestStripeWebhookFlow(t *testing.T) {
	env := SetupEnv(t)
	defer env.Cleanup(t)

	orderRepo := repository.NewOrderRepository(env.DB)
	mock := NewMockStripe()

	seed := func(t *testing.T, orderID, txnID, piID string) {
		t.Helper()
		o := &domain.Order{
			OrderID: orderID, MerchantID: "M_WB", TransactionID: txnID,
			Amount: 1500, Currency: "USD",
			Status:                domain.OrderStatusPending,
			StripePaymentIntentID: piID,
			CallbackURL:           "https://merchant.test/cb",
		}
		if err := orderRepo.Create(context.Background(), o); err != nil {
			t.Fatalf("seed order: %v", err)
		}
	}

	kafkaCfg := config.KafkaConfig{
		Brokers:        env.KafkaBrokers,
		TopicEvents:    "payment.events.wb",
		TopicConfirmed: "payment.confirmed.wb",
		TopicDLQ:       "payment.events.dlq.wb",
		ConsumerGroup:  "wb-group",
	}
	publisher := kafka.NewPublisher(kafkaCfg)
	defer publisher.Close()

	svc := service.NewWebhookService(mock, orderRepo, publisher, env.Redis, webhookSecret)

	seed(t, "ord-wb-1", "TXN-WB-1", "pi_wb_1")

	t.Run("rejects bad signature", func(t *testing.T) {
		payload := mustEvent(t, "evt_wb_bad", "pi_wb_1", "ord-wb-1", "payment_intent.succeeded")
		err := svc.Process(context.Background(), payload, "t=0,v1=deadbeef")
		if err == nil {
			t.Fatal("expected signature failure")
		}
	})

	t.Run("rejects expired timestamp (replay window)", func(t *testing.T) {
		payload := mustEvent(t, "evt_wb_old", "pi_wb_1", "ord-wb-1", "payment_intent.succeeded")
		// 10 minutes old — outside the 5-minute Stripe tolerance.
		stale := time.Now().Add(-10 * time.Minute).Unix()
		sig := stripe.SignPayload(payload, webhookSecret, stale)
		err := svc.Process(context.Background(), payload, sig)
		if !errors.Is(err, stripe.ErrSignatureExpired) {
			t.Fatalf("expected ErrSignatureExpired, got %v", err)
		}
	})

	t.Run("rejects tampered body", func(t *testing.T) {
		// Sign one body, then send a different body with that signature.
		original := mustEvent(t, "evt_wb_tamper", "pi_wb_1", "ord-wb-1", "payment_intent.succeeded")
		sig := stripe.SignPayload(original, webhookSecret, time.Now().Unix())
		tampered := []byte(`{"id":"evt_wb_tamper","type":"payment_intent.succeeded"}`)
		err := svc.Process(context.Background(), tampered, sig)
		if !errors.Is(err, stripe.ErrSignatureMismatch) {
			t.Fatalf("expected ErrSignatureMismatch, got %v", err)
		}
	})

	t.Run("succeeded transitions order to paid", func(t *testing.T) {
		payload := mustEvent(t, "evt_wb_ok", "pi_wb_1", "ord-wb-1", "payment_intent.succeeded")
		sig := stripe.SignPayload(payload, webhookSecret, time.Now().Unix())
		if err := svc.Process(context.Background(), payload, sig); err != nil {
			t.Fatalf("process: %v", err)
		}
		o, _ := orderRepo.GetByOrderID(context.Background(), "ord-wb-1")
		if o.Status != domain.OrderStatusPaid {
			t.Fatalf("status: got %s want paid", o.Status)
		}
	})

	t.Run("duplicate event.id is no-op", func(t *testing.T) {
		payload := mustEvent(t, "evt_wb_dupe", "pi_wb_1", "ord-wb-1", "payment_intent.succeeded")
		sig := stripe.SignPayload(payload, webhookSecret, time.Now().Unix())
		if err := svc.Process(context.Background(), payload, sig); err != nil {
			t.Fatalf("first: %v", err)
		}
		err := svc.Process(context.Background(), payload, sig)
		if !errors.Is(err, service.ErrWebhookDuplicate) {
			t.Fatalf("expected duplicate error, got %v", err)
		}
	})

	t.Run("payment_failed transitions order to failed", func(t *testing.T) {
		seed(t, "ord-wb-fail", "TXN-WB-FAIL", "pi_wb_fail")
		payload := mustEvent(t, "evt_wb_fail", "pi_wb_fail", "ord-wb-fail", "payment_intent.payment_failed")
		sig := stripe.SignPayload(payload, webhookSecret, time.Now().Unix())
		if err := svc.Process(context.Background(), payload, sig); err != nil {
			t.Fatalf("process: %v", err)
		}
		o, _ := orderRepo.GetByOrderID(context.Background(), "ord-wb-fail")
		if o.Status != domain.OrderStatusFailed {
			t.Fatalf("status: got %s want failed", o.Status)
		}
	})

	t.Run("charge_refunded transitions order to refunded and emits confirmed event", func(t *testing.T) {
		seed(t, "ord-wb-ref", "TXN-WB-REF", "pi_wb_ref")
		payload := mustRefundedEvent(t, "evt_wb_ref", "ch_wb_ref", "pi_wb_ref", "ord-wb-ref")
		sig := stripe.SignPayload(payload, webhookSecret, time.Now().Unix())
		if err := svc.Process(context.Background(), payload, sig); err != nil {
			t.Fatalf("process: %v", err)
		}
		o, _ := orderRepo.GetByOrderID(context.Background(), "ord-wb-ref")
		if o.Status != domain.OrderStatusRefunded {
			t.Fatalf("status: got %s want refunded", o.Status)
		}

		// Read the payment.confirmed topic from the start to assert
		// publication. We use a partition reader (offset 0) instead of a
		// consumer group so we don't race on offset commits.
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
		deadline := time.Now().Add(10 * time.Second)
		for !found && time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			m, err := reader.ReadMessage(ctx)
			cancel()
			if err != nil {
				break
			}
			var ev kafka.PaymentConfirmedEvent
			if json.Unmarshal(m.Value, &ev) == nil &&
				ev.OrderID == "ord-wb-ref" &&
				ev.Status == string(domain.OrderStatusRefunded) {
				found = true
			}
		}
		if !found {
			t.Fatal("payment.confirmed event for refund not found on topic")
		}
	})
}

func mustEvent(t *testing.T, eventID, piID, orderID, eventType string) []byte {
	t.Helper()
	body := map[string]any{
		"id":      eventID,
		"type":    eventType,
		"created": time.Now().Unix(),
		"data": map[string]any{
			"object": map[string]any{
				"id":     piID,
				"object": "payment_intent",
				"amount": 1500,
				"status": "succeeded",
				"metadata": map[string]string{
					"order_id":    orderID,
					"merchant_id": "M_WB",
				},
			},
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustRefundedEvent(t *testing.T, eventID, chargeID, piID, orderID string) []byte {
	t.Helper()
	body := map[string]any{
		"id":      eventID,
		"type":    "charge.refunded",
		"created": time.Now().Unix(),
		"data": map[string]any{
			"object": map[string]any{
				"id":             chargeID,
				"object":         "charge",
				"amount":         1500,
				"status":         "succeeded",
				"refunded":       true,
				"payment_intent": piID,
				"metadata": map[string]string{
					"order_id":    orderID,
					"merchant_id": "M_WB",
				},
			},
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

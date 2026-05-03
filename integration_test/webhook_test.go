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
// MySQL + Redis + Kafka. Each subtest seeds its own order so they don't
// step on each other.
func TestStripeWebhookFlow(t *testing.T) {
	env := SetupEnv(t)
	defer env.Cleanup(t)

	orderRepo := repository.NewOrderRepository(env.Router)
	merchantRepo := repository.NewMerchantRepository(env.Router, 16)
	mock := NewMockStripe()

	const merchantID = "M_WB"
	SeedMerchant(t, env.DB, merchantID, "ms-secret", "")
	type seeded struct {
		order *domain.Order
		piID  string
	}
	seed := func(t *testing.T, slug, piID string) seeded {
		t.Helper()
		o := SeedOrder(t, orderRepo, merchantID, slug, 1500, domain.OrderStatusPending, func(o *domain.Order) {
			o.StripePaymentIntentID = piID
		})
		return seeded{order: o, piID: piID}
	}

	kafkaCfg := config.KafkaConfig{
		Brokers:        env.KafkaBrokers,
		TopicConfirmed: "payment.confirmed.wb",
		TopicDLQ:       "payment.confirmed.dlq.wb",
		ConsumerGroup:  "wb-group",
	}
	publisher := kafka.NewPublisher(kafkaCfg)
	defer func() { _ = publisher.Close() }()

	svc := service.NewWebhookService(mock, orderRepo, merchantRepo, publisher, env.Redis, webhookSecret)

	main := seed(t, "WB-1", "pi_wb_1")

	t.Run("rejects bad signature", func(t *testing.T) {
		payload := mustEvent(t, "evt_wb_bad", main.piID, main.order.OrderID, "payment_intent.succeeded")
		err := svc.Process(context.Background(), payload, "t=0,v1=deadbeef")
		if err == nil {
			t.Fatal("expected signature failure")
		}
	})

	t.Run("rejects expired timestamp (replay window)", func(t *testing.T) {
		payload := mustEvent(t, "evt_wb_old", main.piID, main.order.OrderID, "payment_intent.succeeded")
		stale := time.Now().Add(-10 * time.Minute).Unix()
		sig := stripe.SignPayload(payload, webhookSecret, stale)
		err := svc.Process(context.Background(), payload, sig)
		if !errors.Is(err, stripe.ErrSignatureExpired) {
			t.Fatalf("expected ErrSignatureExpired, got %v", err)
		}
	})

	t.Run("rejects tampered body", func(t *testing.T) {
		original := mustEvent(t, "evt_wb_tamper", main.piID, main.order.OrderID, "payment_intent.succeeded")
		sig := stripe.SignPayload(original, webhookSecret, time.Now().Unix())
		tampered := []byte(`{"id":"evt_wb_tamper","type":"payment_intent.succeeded"}`)
		err := svc.Process(context.Background(), tampered, sig)
		if !errors.Is(err, stripe.ErrSignatureMismatch) {
			t.Fatalf("expected ErrSignatureMismatch, got %v", err)
		}
	})

	t.Run("succeeded transitions order to paid", func(t *testing.T) {
		payload := mustEvent(t, "evt_wb_ok", main.piID, main.order.OrderID, "payment_intent.succeeded")
		sig := stripe.SignPayload(payload, webhookSecret, time.Now().Unix())
		if err := svc.Process(context.Background(), payload, sig); err != nil {
			t.Fatalf("process: %v", err)
		}
		o, _ := orderRepo.GetByMerchantOrderID(context.Background(), 0, main.order.MerchantID, main.order.OrderID)
		if o.Status != domain.OrderStatusPaid {
			t.Fatalf("status: got %s want paid", o.Status)
		}
	})

	t.Run("duplicate event.id is no-op", func(t *testing.T) {
		payload := mustEvent(t, "evt_wb_dupe", main.piID, main.order.OrderID, "payment_intent.succeeded")
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
		s := seed(t, "WB-FAIL", "pi_wb_fail")
		payload := mustEvent(t, "evt_wb_fail", s.piID, s.order.OrderID, "payment_intent.payment_failed")
		sig := stripe.SignPayload(payload, webhookSecret, time.Now().Unix())
		if err := svc.Process(context.Background(), payload, sig); err != nil {
			t.Fatalf("process: %v", err)
		}
		o, _ := orderRepo.GetByMerchantOrderID(context.Background(), 0, s.order.MerchantID, s.order.OrderID)
		if o.Status != domain.OrderStatusFailed {
			t.Fatalf("status: got %s want failed", o.Status)
		}
	})

	t.Run("missing order on UPDATE is a hard error", func(t *testing.T) {
		// Synthesise an event for an order_id that has no row in any shard.
		// With sync write this is a tampering signal, not a benign race;
		// service must return ErrWebhookOrderMissing so the handler 5xxs.
		bogus := "00112233445566778899aabb" // 24-hex, never seeded
		payload := mustEvent(t, "evt_wb_missing", "pi_missing", bogus, "payment_intent.succeeded")
		sig := stripe.SignPayload(payload, webhookSecret, time.Now().Unix())
		err := svc.Process(context.Background(), payload, sig)
		if !errors.Is(err, service.ErrWebhookOrderMissing) {
			t.Fatalf("expected ErrWebhookOrderMissing, got %v", err)
		}
	})

	t.Run("charge_refunded transitions order to refunded and emits confirmed event", func(t *testing.T) {
		s := seed(t, "WB-REF", "pi_wb_ref")
		payload := mustRefundedEvent(t, "evt_wb_ref", "ch_wb_ref", s.piID, s.order.OrderID)
		sig := stripe.SignPayload(payload, webhookSecret, time.Now().Unix())
		if err := svc.Process(context.Background(), payload, sig); err != nil {
			t.Fatalf("process: %v", err)
		}
		o, _ := orderRepo.GetByMerchantOrderID(context.Background(), 0, s.order.MerchantID, s.order.OrderID)
		if o.Status != domain.OrderStatusRefunded {
			t.Fatalf("status: got %s want refunded", o.Status)
		}

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
				ev.OrderID == s.order.OrderID &&
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
	return mustEventForMerchant(t, eventID, piID, orderID, eventType, "M_WB")
}

func mustEventForMerchant(t *testing.T, eventID, piID, orderID, eventType, merchantID string) []byte {
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
					"merchant_id": merchantID,
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

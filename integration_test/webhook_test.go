//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/quangdangfit/easypay/internal/config"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/internal/service"
)

const webhookSecret = "whsec_int_test"

// TestStripeWebhookFlow runs three webhook scenarios end-to-end against MySQL +
// Redis: signature verification, event idempotency on event.id, and routing
// of payment_intent.succeeded into status='paid'.
func TestStripeWebhookFlow(t *testing.T) {
	env := SetupEnv(t)
	defer env.Cleanup(t)

	orderRepo := repository.NewOrderRepository(env.DB)
	mock := NewMockStripe()

	// Seed one order in 'pending' state.
	order := &domain.Order{
		OrderID:               "ORD-WB-1",
		MerchantID:            "M_WB",
		TransactionID:         "TXN-WB-1",
		Amount:                1500,
		Currency:              "USD",
		Status:                domain.OrderStatusPending,
		StripePaymentIntentID: "pi_wb_1",
	}
	if err := orderRepo.Create(context.Background(), order); err != nil {
		t.Fatalf("seed order: %v", err)
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

	t.Run("rejects bad signature", func(t *testing.T) {
		payload := mustEvent(t, "evt_wb_bad", "pi_wb_1", "ORD-WB-1")
		err := svc.Process(context.Background(), payload, "t=0,v1=deadbeef")
		if err == nil {
			t.Fatal("expected signature failure")
		}
	})

	t.Run("succeeded transitions order to paid", func(t *testing.T) {
		payload := mustEvent(t, "evt_wb_ok", "pi_wb_1", "ORD-WB-1")
		sig := stripe.SignPayload(payload, webhookSecret, time.Now().Unix())
		if err := svc.Process(context.Background(), payload, sig); err != nil {
			t.Fatalf("process: %v", err)
		}
		o, _ := orderRepo.GetByOrderID(context.Background(), "ORD-WB-1")
		if o.Status != domain.OrderStatusPaid {
			t.Fatalf("status: got %s want paid", o.Status)
		}
	})

	t.Run("duplicate event.id is no-op", func(t *testing.T) {
		payload := mustEvent(t, "evt_wb_dupe", "pi_wb_1", "ORD-WB-1")
		sig := stripe.SignPayload(payload, webhookSecret, time.Now().Unix())
		if err := svc.Process(context.Background(), payload, sig); err != nil {
			t.Fatalf("first: %v", err)
		}
		err := svc.Process(context.Background(), payload, sig)
		if !errors.Is(err, service.ErrWebhookDuplicate) {
			t.Fatalf("expected duplicate error, got %v", err)
		}
	})
}

func mustEvent(t *testing.T, eventID, piID, orderID string) []byte {
	t.Helper()
	body := map[string]any{
		"id":      eventID,
		"type":    "payment_intent.succeeded",
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

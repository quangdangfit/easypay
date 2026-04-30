//go:build integration

package integration

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quangdangfit/easypay/internal/config"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/internal/service"
)

// TestDoubleSpending_ConcurrentConfirm pounds the webhook pipeline with
// concurrent requests for the same order from two angles:
//
//  1. SAME event.id replayed by N workers — Redis SETNX must serialize them
//     so exactly one wins; the others see ErrWebhookDuplicate. This protects
//     against Stripe retrying a delivery before we've ACKed.
//
//  2. DIFFERENT event.ids targeting the same order — all should succeed and
//     converge on a single 'paid' state. This protects against in-order
//     duplicate webhooks (e.g. payment_intent.succeeded + checkout.session.completed
//     for the same payment) racing each other.
func TestDoubleSpending_ConcurrentConfirm(t *testing.T) {
	env := SetupEnv(t)
	defer env.Cleanup(t)

	orderRepo := repository.NewOrderRepository(env.DB)
	mock := NewMockStripe()

	kafkaCfg := config.KafkaConfig{
		Brokers:        env.KafkaBrokers,
		TopicEvents:    "payment.events.ds",
		TopicConfirmed: "payment.confirmed.ds",
		TopicDLQ:       "payment.events.dlq.ds",
		ConsumerGroup:  "ds-group",
	}
	publisher := kafka.NewPublisher(kafkaCfg)
	defer publisher.Close()

	svc := service.NewWebhookService(mock, orderRepo, publisher, env.Redis, webhookSecret)

	t.Run("same event.id replayed concurrently", func(t *testing.T) {
		seed := &domain.Order{
			OrderID: "ORD-DS-1", MerchantID: "M_WB", TransactionID: "TXN-DS-1",
			Amount: 1500, Currency: "USD", Status: domain.OrderStatusPending,
			StripePaymentIntentID: "pi_ds_1",
		}
		if err := orderRepo.Create(context.Background(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}

		payload := mustEvent(t, "evt_ds_same", "pi_ds_1", "ORD-DS-1", "payment_intent.succeeded")
		sig := stripe.SignPayload(payload, webhookSecret, time.Now().Unix())

		const workers = 10
		var winners, dupes, others atomic.Int64
		var wg sync.WaitGroup
		wg.Add(workers)
		start := make(chan struct{})
		for i := 0; i < workers; i++ {
			go func() {
				defer wg.Done()
				<-start
				err := svc.Process(context.Background(), payload, sig)
				switch {
				case err == nil:
					winners.Add(1)
				case errors.Is(err, service.ErrWebhookDuplicate):
					dupes.Add(1)
				default:
					others.Add(1)
					t.Errorf("unexpected error: %v", err)
				}
			}()
		}
		close(start)
		wg.Wait()

		if winners.Load() != 1 {
			t.Errorf("expected exactly 1 winner, got %d (dupes=%d, errs=%d)",
				winners.Load(), dupes.Load(), others.Load())
		}
		if dupes.Load() != workers-1 {
			t.Errorf("expected %d duplicates, got %d", workers-1, dupes.Load())
		}

		o, _ := orderRepo.GetByOrderID(context.Background(), seed.OrderID)
		if o.Status != domain.OrderStatusPaid {
			t.Errorf("status: got %s want paid", o.Status)
		}
	})

	t.Run("different event.ids targeting same order converge to paid", func(t *testing.T) {
		seed := &domain.Order{
			OrderID: "ORD-DS-2", MerchantID: "M_WB", TransactionID: "TXN-DS-2",
			Amount: 1500, Currency: "USD", Status: domain.OrderStatusPending,
			StripePaymentIntentID: "pi_ds_2",
		}
		if err := orderRepo.Create(context.Background(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}

		const workers = 8
		var ok, fail atomic.Int64
		var wg sync.WaitGroup
		wg.Add(workers)
		start := make(chan struct{})
		for i := 0; i < workers; i++ {
			i := i
			go func() {
				defer wg.Done()
				<-start
				payload := mustEventWithID(t, i, "pi_ds_2", "ORD-DS-2")
				sig := stripe.SignPayload(payload, webhookSecret, time.Now().Unix())
				if err := svc.Process(context.Background(), payload, sig); err != nil {
					fail.Add(1)
					t.Errorf("worker %d: %v", i, err)
					return
				}
				ok.Add(1)
			}()
		}
		close(start)
		wg.Wait()

		if ok.Load() != workers || fail.Load() != 0 {
			t.Fatalf("expected all %d to succeed, got ok=%d fail=%d", workers, ok.Load(), fail.Load())
		}

		o, _ := orderRepo.GetByOrderID(context.Background(), seed.OrderID)
		if o.Status != domain.OrderStatusPaid {
			t.Fatalf("status: got %s want paid", o.Status)
		}
	})
}

// mustEventWithID is a per-worker variant of mustEvent that gives each
// goroutine a distinct event.id so they don't collide on Redis SETNX.
func mustEventWithID(t *testing.T, i int, piID, orderID string) []byte {
	t.Helper()
	return mustEvent(t, "evt_ds_diff_"+strconv.Itoa(i), piID, orderID, "payment_intent.succeeded")
}

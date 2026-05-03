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
//     so exactly one wins; the others see ErrWebhookDuplicate.
//
//  2. DIFFERENT event.ids targeting the same order — all should succeed and
//     converge on a single 'paid' state.
func TestDoubleSpending_ConcurrentConfirm(t *testing.T) {
	env := SetupEnv(t)
	defer env.Cleanup(t)

	orderRepo := repository.NewOrderRepository(env.Router)
	merchantRepo := repository.NewMerchantRepository(env.Router, 16)
	mock := NewMockStripe()

	kafkaCfg := config.KafkaConfig{
		Brokers:        env.KafkaBrokers,
		TopicConfirmed: "payment.confirmed.ds",
		TopicDLQ:       "payment.confirmed.dlq.ds",
		ConsumerGroup:  "ds-group",
	}
	publisher := kafka.NewPublisher(kafkaCfg)
	defer func() { _ = publisher.Close() }()

	svc := service.NewWebhookService(mock, orderRepo, merchantRepo, publisher, env.Redis, webhookSecret)
	const merchantID = "M_WB"
	SeedMerchant(t, env.DB, merchantID, "ms-secret", "")

	t.Run("same event.id replayed concurrently", func(t *testing.T) {
		seed := SeedOrder(t, orderRepo, merchantID, "DS-1", 1500, domain.OrderStatusPending, func(o *domain.Order) {
			o.StripePaymentIntentID = "pi_ds_1"
		})

		payload := mustEvent(t, "evt_ds_same", "pi_ds_1", seed.OrderID, "payment_intent.succeeded")
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

		o, _ := orderRepo.GetByMerchantOrderID(context.Background(), 0, seed.MerchantID, seed.OrderID)
		if o.Status != domain.OrderStatusPaid {
			t.Errorf("status: got %s want paid", o.Status)
		}
	})

	t.Run("different event.ids targeting same order converge to paid", func(t *testing.T) {
		seed := SeedOrder(t, orderRepo, merchantID, "DS-2", 1500, domain.OrderStatusPending, func(o *domain.Order) {
			o.StripePaymentIntentID = "pi_ds_2"
		})

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
				payload := mustEventWithID(t, i, "pi_ds_2", seed.OrderID)
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

		o, _ := orderRepo.GetByMerchantOrderID(context.Background(), 0, seed.MerchantID, seed.OrderID)
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

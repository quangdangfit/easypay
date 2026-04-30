//go:build integration

package integration

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quangdangfit/easypay/internal/cache"
	"github.com/quangdangfit/easypay/internal/config"
	"github.com/quangdangfit/easypay/internal/consumer"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/internal/service"
)

// TestFullPaymentFlow_E2E walks the entire pipeline against real
// MySQL/Redis/Kafka:
//
//  1. PaymentService.Create → publishes to payment.events.
//  2. PaymentConsumer drains the topic and batch-inserts into MySQL.
//  3. WebhookService.Process (driven by a synthesised payment_intent.succeeded
//     event) flips the order to 'paid' and publishes to payment.confirmed.
//  4. SettlementConsumer drains payment.confirmed and POSTs to the merchant
//     callback URL — backed here by an httptest.NewServer that records hits.
//
// If this test passes, every wire between the components is good.
func TestFullPaymentFlow_E2E(t *testing.T) {
	env := SetupEnv(t)
	defer env.Cleanup(t)

	const (
		merchantID    = "M_E2E"
		merchantSec   = "merchant-secret-e2e"
		transactionID = "TXN-E2E-1"
	)

	// Merchant callback receiver — the last hop in the pipeline.
	var callbackHits atomic.Int64
	var lastBody atomic.Value // []byte
	merchantSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lastBody.Store(body)
		callbackHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer merchantSrv.Close()

	// Wire the kafka topology with E2E-suffixed topics so we don't collide
	// with sibling tests.
	kafkaCfg := config.KafkaConfig{
		Brokers:        env.KafkaBrokers,
		TopicEvents:    "payment.events.e2e",
		TopicConfirmed: "payment.confirmed.e2e",
		TopicDLQ:       "payment.events.dlq.e2e",
		ConsumerGroup:  "e2e-group",
	}
	publisher := kafka.NewPublisher(kafkaCfg)
	defer publisher.Close()

	orderRepo := repository.NewOrderRepository(env.DB)
	idem := cache.NewIdempotency(env.Redis)
	stripeMock := NewMockStripe()

	paySvc := service.NewPaymentService(idem, stripeMock, publisher, nil, service.PaymentServiceOptions{
		DefaultCurrency: "USD",
		CryptoContract:  "0xCONTRACT",
		CryptoChainID:   11155111,
	})
	webhookSvc := service.NewWebhookService(stripeMock, orderRepo, publisher, env.Redis, webhookSecret)

	// Boot the two consumers in their own goroutines for the duration of the
	// test. SettlementConsumer needs to look up the merchant secret to sign
	// the outbound POST — we provide a closure for the single test merchant.
	pc := consumer.NewPaymentConsumer(orderRepo).NewBatch(kafkaCfg)
	pc.BatchSize = 5
	pc.BatchWait = 200 * time.Millisecond

	sc := consumer.NewSettlementConsumer(func(mid string) string {
		if mid == merchantID {
			return merchantSec
		}
		return ""
	}).NewBatch(kafkaCfg)
	sc.BatchSize = 5
	sc.BatchWait = 200 * time.Millisecond

	consumerCtx, stopConsumers := context.WithCancel(context.Background())
	var consumerWG sync.WaitGroup
	consumerWG.Add(2)
	go func() { defer consumerWG.Done(); _ = pc.Run(consumerCtx) }()
	go func() { defer consumerWG.Done(); _ = sc.Run(consumerCtx) }()
	defer func() {
		stopConsumers()
		consumerWG.Wait()
	}()

	// ── Step 1: Create payment ──────────────────────────────────────────
	merchant := &domain.Merchant{MerchantID: merchantID, SecretKey: merchantSec, RateLimit: 100}
	res, err := paySvc.Create(context.Background(), service.CreatePaymentInput{
		Merchant:      merchant,
		TransactionID: transactionID,
		Amount:        2500,
		Currency:      "USD",
		CallbackURL:   merchantSrv.URL,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.OrderID == "" || res.StripePaymentIntentID == "" {
		t.Fatalf("bad result: %+v", res)
	}

	// ── Step 2: Wait for the consumer to land the row in MySQL ──────────
	deadline := time.Now().Add(20 * time.Second)
	var landed *domain.Order
	for time.Now().Before(deadline) {
		o, err := orderRepo.GetByOrderID(context.Background(), res.OrderID)
		if err == nil {
			landed = o
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if landed == nil {
		t.Fatalf("order %s never reached MySQL", res.OrderID)
	}
	if landed.Status != domain.OrderStatusPending {
		t.Fatalf("status: got %s want pending", landed.Status)
	}
	if landed.CallbackURL != merchantSrv.URL {
		t.Fatalf("callback_url not persisted: got %q", landed.CallbackURL)
	}

	// ── Step 3: Synthesize the Stripe webhook and process it ────────────
	payload := mustEvent(t, "evt_e2e_succeeded", res.StripePaymentIntentID,
		res.OrderID, "payment_intent.succeeded")
	sig := stripe.SignPayload(payload, webhookSecret, time.Now().Unix())
	if err := webhookSvc.Process(context.Background(), payload, sig); err != nil {
		t.Fatalf("webhook process: %v", err)
	}

	o, _ := orderRepo.GetByOrderID(context.Background(), res.OrderID)
	if o.Status != domain.OrderStatusPaid {
		t.Fatalf("status after webhook: got %s want paid", o.Status)
	}

	// ── Step 4: SettlementConsumer should POST to the merchant callback ─
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if callbackHits.Load() >= 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if callbackHits.Load() == 0 {
		t.Fatal("merchant callback never invoked")
	}
	body, _ := lastBody.Load().([]byte)
	if len(body) == 0 {
		t.Fatal("merchant callback body empty")
	}
}

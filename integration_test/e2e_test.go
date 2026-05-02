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
//  1. PaymentService.Create — sync write to MySQL, Stripe session created.
//  2. WebhookService.Process (driven by a synthesised payment_intent.succeeded
//     event) flips the order to 'paid' and publishes to payment.confirmed.
//  3. SettlementConsumer drains payment.confirmed, looks up the merchant's
//     callback URL + signing secret on the merchants row, and POSTs to the
//     merchant — backed here by an httptest.NewServer that records hits.
//
// If this test passes, every wire between the components is good.
func TestFullPaymentFlow_E2E(t *testing.T) {
	env := SetupEnv(t)
	defer env.Cleanup(t)

	const (
		merchantID  = "M_E2E"
		merchantSec = "merchant-secret-e2e"
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

	// Seed the merchant row so the settlement consumer can resolve callback
	// URL + secret.
	SeedMerchant(t, env.DB, merchantID, merchantSec, merchantSrv.URL)

	kafkaCfg := config.KafkaConfig{
		Brokers:        env.KafkaBrokers,
		TopicConfirmed: "payment.confirmed.e2e",
		TopicDLQ:       "payment.confirmed.dlq.e2e",
		ConsumerGroup:  "e2e-group",
	}
	publisher := kafka.NewPublisher(kafkaCfg)
	defer func() { _ = publisher.Close() }()

	orderRepo := repository.NewOrderRepository(env.DB)
	merchantRepo := repository.NewMerchantRepository(env.DB, 16)
	stripeMock := NewMockStripe()

	paySvc := service.NewPaymentService(stripeMock, orderRepo, service.PaymentServiceOptions{
		DefaultCurrency: "USD",
		CryptoContract:  "0xCONTRACT",
		CryptoChainID:   11155111,
	})
	webhookSvc := service.NewWebhookService(stripeMock, orderRepo, publisher, env.Redis, webhookSecret)

	// Settlement consumer looks up the merchant per event.
	lookup := func(mid string) consumer.MerchantCallback {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m, err := merchantRepo.GetByMerchantID(ctx, mid)
		if err != nil {
			return consumer.MerchantCallback{}
		}
		return consumer.MerchantCallback{URL: m.CallbackURL, Secret: m.SecretKey}
	}
	sc := consumer.NewSettlementConsumer(lookup).NewBatch(kafkaCfg)
	sc.BatchSize = 5
	sc.BatchWait = 200 * time.Millisecond

	consumerCtx, stopConsumer := context.WithCancel(context.Background())
	var consumerWG sync.WaitGroup
	consumerWG.Add(1)
	go func() { defer consumerWG.Done(); _ = sc.Run(consumerCtx) }()
	defer func() {
		stopConsumer()
		consumerWG.Wait()
	}()

	// ── Step 1: Create payment ──────────────────────────────────────────
	merchant := &domain.Merchant{MerchantID: merchantID, SecretKey: merchantSec, RateLimit: 100}
	res, err := paySvc.Create(context.Background(), service.CreatePaymentInput{
		Merchant: merchant,
		OrderID:  "E2E-1",
		Amount:   2500,
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.OrderID == "" || res.StripePaymentIntentID == "" {
		t.Fatalf("bad result: %+v", res)
	}

	// ── Step 2: Row must be in MySQL synchronously ──────────────────────
	landed, err := orderRepo.GetByMerchantOrderID(context.Background(), merchant.MerchantID, res.OrderID)
	if err != nil {
		t.Fatalf("order %s not found after Create: %v", res.OrderID, err)
	}
	if landed.Status != domain.OrderStatusCreated {
		t.Fatalf("status: got %s want created", landed.Status)
	}
	if landed.StripeSessionID == "" {
		t.Fatalf("Stripe session not persisted: %+v", landed)
	}

	// ── Step 3: Synthesize the Stripe webhook and process it ────────────
	payload := mustEventForMerchant(t, "evt_e2e_succeeded", res.StripePaymentIntentID,
		res.OrderID, "payment_intent.succeeded", merchantID)
	sig := stripe.SignPayload(payload, webhookSecret, time.Now().Unix())
	if err := webhookSvc.Process(context.Background(), payload, sig); err != nil {
		t.Fatalf("webhook process: %v", err)
	}

	o, _ := orderRepo.GetByMerchantOrderID(context.Background(), merchant.MerchantID, res.OrderID)
	if o.Status != domain.OrderStatusPaid {
		t.Fatalf("status after webhook: got %s want paid", o.Status)
	}

	// ── Step 4: SettlementConsumer should POST to the merchant callback ─
	deadline := time.Now().Add(15 * time.Second)
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

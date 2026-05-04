package consumer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/quangdangfit/easypay/internal/config"
	"github.com/quangdangfit/easypay/internal/kafka"
)

func testKafkaCfg() config.KafkaConfig {
	return config.KafkaConfig{
		Brokers:        []string{"127.0.0.1:1"},
		TopicConfirmed: "cfm",
		TopicDLQ:       "dlq",
		ConsumerGroup:  "test",
	}
}

func encodeConfirmed(t *testing.T, ev kafka.PaymentConfirmedEvent) []byte {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fixedLookup returns a static MerchantLookup with the given URL/secret
// for any merchant_id. Empty URL = skip notification.
func fixedLookup(url, secret string) MerchantLookup {
	return func(string) MerchantCallback {
		return MerchantCallback{URL: url, Secret: secret}
	}
}

func TestSettlement_HandleOne_NoCallbackIsNoOp(t *testing.T) {
	c := NewSettlementConsumer(fixedLookup("", "secret"))
	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-1", MerchantID: "M1"})
	if err := c.HandleOne(context.Background(), kafkago.Message{Value: body}); err != nil {
		t.Fatalf("err: %v", err)
	}
}

func TestSettlement_HandleOne_HappyPath(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("X-Signature") == "" || r.Header.Get("X-Timestamp") == "" {
			t.Errorf("missing HMAC headers")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewSettlementConsumer(fixedLookup(srv.URL, "secret"))
	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-1", MerchantID: "M1"})
	if err := c.HandleOne(context.Background(), kafkago.Message{Value: body}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits: %d", hits.Load())
	}
}

func TestSettlement_HandleOne_RetriesOn5xx(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewSettlementConsumer(fixedLookup(srv.URL, "secret"))
	c.maxAttempts = 2
	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-1", MerchantID: "M1"})
	err := c.HandleOne(context.Background(), kafkago.Message{Value: body})
	if err == nil {
		t.Fatal("expected error after retries")
	}
	if hits.Load() != 2 {
		t.Fatalf("retries: %d (expected 2)", hits.Load())
	}
}

func TestSettlement_HandleOne_RejectsMissingSecret(t *testing.T) {
	c := NewSettlementConsumer(fixedLookup("https://x", ""))
	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-1", MerchantID: "M1"})
	if err := c.HandleOne(context.Background(), kafkago.Message{Value: body}); err == nil {
		t.Fatal("expected secret-missing error")
	}
}

func TestSettlement_Handle_ReturnsFirstError(t *testing.T) {
	c := NewSettlementConsumer(fixedLookup("https://x", ""))
	// First message has no callback (skipped, no error). Second has a URL
	// but no secret — error.
	good := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-1", MerchantID: "skip"})
	bad := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-2", MerchantID: "M1"})
	c2 := NewSettlementConsumer(func(merchantID string) MerchantCallback {
		if merchantID == "skip" {
			return MerchantCallback{}
		}
		return MerchantCallback{URL: "https://x"}
	})
	if err := c2.Handle(context.Background(), []kafkago.Message{{Value: good}, {Value: bad}}); err == nil {
		t.Fatal("expected error from second message")
	}
	_ = c
}

func TestSettlement_NewBatchWiresUp(t *testing.T) {
	c := NewSettlementConsumer(fixedLookup("", ""))
	bc := c.NewBatch(testKafkaCfg())
	if bc.BatchSize != 50 || bc.BatchWait != 100*time.Millisecond {
		t.Fatalf("settlement batch overrides not applied: %+v", bc)
	}
}

func TestSettlement_HandleOne_InvalidJSON(t *testing.T) {
	c := NewSettlementConsumer(fixedLookup("https://x", "secret"))
	if err := c.HandleOne(context.Background(), kafkago.Message{Value: []byte("invalid json")}); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestSettlement_HandleOne_ContextCancelledDuringRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := NewSettlementConsumer(fixedLookup(srv.URL, "secret"))
	c.maxAttempts = 3

	// Cancel context after first attempt to interrupt retries
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-1", MerchantID: "M1"})
	err := c.HandleOne(ctx, kafkago.Message{Value: body})
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
}

func TestSettlement_HandleOne_SuccessesAfterRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := hits.Add(1)
		if count < 2 {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := NewSettlementConsumer(fixedLookup(srv.URL, "secret"))
	c.maxAttempts = 3
	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-1", MerchantID: "M1"})
	if err := c.HandleOne(context.Background(), kafkago.Message{Value: body}); err != nil {
		t.Fatalf("expected success on retry: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", hits.Load())
	}
}

func TestSettlement_HandleOne_4xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := NewSettlementConsumer(fixedLookup(srv.URL, "secret"))
	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-1", MerchantID: "M1"})
	if err := c.HandleOne(context.Background(), kafkago.Message{Value: body}); err == nil {
		t.Fatal("expected error for 4xx response")
	}
}

func TestSettlement_Handle_ProcessesMultipleMessages(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewSettlementConsumer(fixedLookup(srv.URL, "secret"))
	msgs := []kafkago.Message{
		{Value: encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-1", MerchantID: "M1"})},
		{Value: encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-2", MerchantID: "M1"})},
	}
	if err := c.Handle(context.Background(), msgs); err != nil {
		t.Fatalf("err: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("expected 2 hits, got %d", hits.Load())
	}
}

func TestSettlement_HandleOne_NetworkError(t *testing.T) {
	c := NewSettlementConsumer(fixedLookup("http://localhost:1", "secret"))
	c.maxAttempts = 2
	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-1", MerchantID: "M1"})
	if err := c.HandleOne(context.Background(), kafkago.Message{Value: body}); err == nil {
		t.Fatal("expected error from network failure")
	}
}

func TestSettlement_HandleOne_StatusOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewSettlementConsumer(fixedLookup(srv.URL, "secret"))
	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-1", MerchantID: "M1"})
	if err := c.HandleOne(context.Background(), kafkago.Message{Value: body}); err != nil {
		t.Fatalf("should succeed with 200: %v", err)
	}
}

func TestSettlement_HandleOne_Status299(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(299)
	}))
	defer srv.Close()

	c := NewSettlementConsumer(fixedLookup(srv.URL, "secret"))
	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-1", MerchantID: "M1"})
	if err := c.HandleOne(context.Background(), kafkago.Message{Value: body}); err != nil {
		t.Fatalf("should succeed with 299: %v", err)
	}
}

func TestSettlement_HandleOne_Status300(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(300)
	}))
	defer srv.Close()

	c := NewSettlementConsumer(fixedLookup(srv.URL, "secret"))
	c.maxAttempts = 1
	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ord-1", MerchantID: "M1"})
	if err := c.HandleOne(context.Background(), kafkago.Message{Value: body}); err == nil {
		t.Fatal("should fail with 300")
	}
}

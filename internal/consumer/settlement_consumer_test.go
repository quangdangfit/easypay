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

	"github.com/quangdangfit/easypay/internal/kafka"
)

func encodeConfirmed(t *testing.T, ev kafka.PaymentConfirmedEvent) []byte {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSettlement_HandleOne_NoCallbackIsNoOp(t *testing.T) {
	c := NewSettlementConsumer(func(string) string { return "secret" })
	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ORD-1", MerchantID: "M1"})
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

	c := NewSettlementConsumer(func(string) string { return "secret" })
	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ORD-1", MerchantID: "M1", CallbackURL: srv.URL})
	if err := c.HandleOne(context.Background(), kafkago.Message{Value: body}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits: %d", hits.Load())
	}
}

func TestSettlement_HandleOne_RetriesOn5xx(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewSettlementConsumer(func(string) string { return "secret" })
	c.maxAttempts = 2
	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ORD-1", MerchantID: "M1", CallbackURL: srv.URL})
	err := c.HandleOne(context.Background(), kafkago.Message{Value: body})
	if err == nil {
		t.Fatal("expected error after retries")
	}
	if hits.Load() != 2 {
		t.Fatalf("retries: %d (expected 2)", hits.Load())
	}
}

func TestSettlement_HandleOne_RejectsMissingSecret(t *testing.T) {
	c := NewSettlementConsumer(func(string) string { return "" })
	body := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ORD-1", MerchantID: "M1", CallbackURL: "https://x"})
	if err := c.HandleOne(context.Background(), kafkago.Message{Value: body}); err == nil {
		t.Fatal("expected secret-missing error")
	}
}

func TestSettlement_Handle_ReturnsFirstError(t *testing.T) {
	c := NewSettlementConsumer(func(string) string { return "" })
	good := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ORD-1"})
	bad := encodeConfirmed(t, kafka.PaymentConfirmedEvent{OrderID: "ORD-2", MerchantID: "M1", CallbackURL: "https://x"})
	if err := c.Handle(context.Background(), []kafkago.Message{{Value: good}, {Value: bad}}); err == nil {
		t.Fatal("expected error from second message")
	}
}

func TestSettlement_NewBatchWiresUp(t *testing.T) {
	c := NewSettlementConsumer(func(string) string { return "" })
	bc := c.NewBatch(testKafkaCfg())
	if bc.BatchSize != 50 || bc.BatchWait != 100*time.Millisecond {
		t.Fatalf("settlement batch overrides not applied: %+v", bc)
	}
}

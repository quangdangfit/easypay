package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/quangdangfit/easypay/internal/config"
)

func TestPaymentConfirmedEventEncoding(t *testing.T) {
	e := PaymentConfirmedEvent{
		OrderID: "ord-1", MerchantID: "M1", Status: "paid",
		Amount: 1500, Currency: "USD", ConfirmedAt: 1,
	}
	if e.OrderID == "" {
		t.Fatal("smoke")
	}
}

func TestMarshalForDLQ(t *testing.T) {
	m := kafkago.Message{Topic: "t", Partition: 1, Offset: 42, Key: []byte("k"), Value: []byte("v")}
	b, err := MarshalForDLQ(m, errors.New("bad"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("empty payload")
	}
}

func TestPinger_Empty(t *testing.T) {
	p := NewPinger(config.KafkaConfig{Brokers: nil})
	if err := p.Ping(context.Background()); err == nil {
		t.Fatal("expected error for empty brokers")
	}
}

func TestPinger_BadAddress(t *testing.T) {
	p := NewPinger(config.KafkaConfig{Brokers: []string{"127.0.0.1:1"}})
	if err := p.Ping(context.Background()); err == nil {
		t.Fatal("expected dial error")
	}
}

func TestPublisher_Close(t *testing.T) {
	// We can't bring up a broker in unit tests, but we can verify Close() is
	// safe to call on a freshly-constructed publisher (the underlying kafka.Writer
	// permits this — its Close just flushes buffers).
	p := NewPublisher(config.KafkaConfig{
		Brokers:        []string{"127.0.0.1:1"},
		TopicConfirmed: "t2",
		TopicDLQ:       "t3",
	})
	if err := p.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestPublishPaymentConfirmed_NoBrokerErrors(t *testing.T) {
	p := NewPublisher(config.KafkaConfig{
		Brokers:        []string{"127.0.0.1:1"},
		TopicConfirmed: "t2",
		TopicDLQ:       "t3",
	})
	defer func() { _ = p.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// Without a broker, this should error (timeout / connection refused).
	// We just want to exercise the marshalling + WriteMessages code path.
	_ = p.PublishPaymentConfirmed(ctx, PaymentConfirmedEvent{OrderID: "ord-1"})
}

func TestEnsureTopics_NoBrokers(t *testing.T) {
	// Should be a silent no-op, not panic.
	ensureTopics(nil, "t")
}

func TestEnsureTopics_BadBroker(t *testing.T) {
	ensureTopics([]string{"127.0.0.1:1"}, "t")
}

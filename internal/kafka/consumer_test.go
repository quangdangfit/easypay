package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/quangdangfit/easypay/internal/config"
)

// noopHandler is a tiny in-package BatchHandler. We can't use kafkamock here
// because that package imports internal/kafka (for PaymentEvent), which would
// create an import cycle for tests in package kafka itself.
type noopHandler struct{}

func (noopHandler) Handle(context.Context, []kafkago.Message) error  { return nil }
func (noopHandler) HandleOne(context.Context, kafkago.Message) error { return nil }

func testCfg() config.KafkaConfig {
	return config.KafkaConfig{
		Brokers:        []string{"127.0.0.1:1"},
		TopicEvents:    "evt",
		TopicConfirmed: "cfm",
		TopicDLQ:       "dlq",
		ConsumerGroup:  "test",
	}
}

// NOTE: kafka.NewReader spawns background goroutines that don't terminate
// without a real broker. Calling Reader.Close() in tests blocks indefinitely
// while the consumer-group dialer keeps retrying. We therefore exercise
// constructors but never call Close on the leaked Reader; goroutines die at
// process exit. This mirrors the existing pattern in
// internal/consumer/payment_consumer_test.go (TestPaymentConsumer_NewBatchWiresUp).

func TestNewBatchConsumer_WiresDefaults(t *testing.T) {
	c := NewBatchConsumer(testCfg(), "topic", noopHandler{})
	if c == nil {
		t.Fatal("nil consumer")
	}
	if c.BatchSize != 500 {
		t.Errorf("BatchSize=%d, want 500", c.BatchSize)
	}
	if c.BatchWait != 200*time.Millisecond {
		t.Errorf("BatchWait=%v", c.BatchWait)
	}
	if c.Reader == nil {
		t.Fatal("nil Reader")
	}
	if c.dlq == nil {
		t.Fatal("nil dlq writer")
	}
	if c.DLQTopic != "dlq" {
		t.Errorf("DLQTopic=%q", c.DLQTopic)
	}
}

func TestBatchConsumer_ToDLQ_BadBrokerLogs(t *testing.T) {
	// Construct manually so we only exercise the dlq Writer (no Reader, no
	// background goroutines). Writer.WriteMessages will fail with a context
	// timeout because nothing answers on 127.0.0.1:1, exercising the
	// log-and-continue branch.
	c := &BatchConsumer{
		Handler: noopHandler{},
		dlq: &kafkago.Writer{
			Addr:         kafkago.TCP("127.0.0.1:1"),
			Topic:        "dlq",
			RequiredAcks: kafkago.RequireAll,
		},
	}
	defer func() { _ = c.dlq.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c.toDLQ(ctx, kafkago.Message{Topic: "x", Partition: 0, Offset: 1, Key: []byte("k"), Value: []byte("v")}, errors.New("boom"))
}

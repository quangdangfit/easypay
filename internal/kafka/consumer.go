package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/quangdangfit/easypay/internal/config"
	"github.com/quangdangfit/easypay/pkg/logger"
)

// BatchConsumer reads up to BatchSize messages from a topic, hands them to
// Handler, then commits offsets only on success. Failed batches fall back
// to per-message handling and dead-letter routing.
type BatchConsumer struct {
	Reader    *kafka.Reader
	Brokers   []string
	DLQTopic  string
	BatchSize int
	BatchWait time.Duration
	Handler   BatchHandler
	dlq       *kafka.Writer
}

type BatchHandler interface {
	Handle(ctx context.Context, msgs []kafka.Message) error
	HandleOne(ctx context.Context, msg kafka.Message) error
}

func NewBatchConsumer(cfg config.KafkaConfig, topic string, handler BatchHandler) *BatchConsumer {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        cfg.Brokers,
		Topic:          topic,
		GroupID:        cfg.ConsumerGroup,
		MinBytes:       1,
		MaxBytes:       10 << 20,
		CommitInterval: 0, // we commit manually
		MaxWait:        500 * time.Millisecond,
	})
	dlq := &kafka.Writer{
		Addr:                   kafka.TCP(cfg.Brokers...),
		Topic:                  cfg.TopicDLQ,
		Balancer:               &kafka.Hash{},
		RequiredAcks:           kafka.RequireAll,
		AllowAutoTopicCreation: true,
	}
	return &BatchConsumer{
		Reader:    r,
		Brokers:   cfg.Brokers,
		DLQTopic:  cfg.TopicDLQ,
		BatchSize: 500,
		BatchWait: 200 * time.Millisecond,
		Handler:   handler,
		dlq:       dlq,
	}
}

func (c *BatchConsumer) Run(ctx context.Context) error {
	defer func() { _ = c.Reader.Close() }()
	defer func() { _ = c.dlq.Close() }()

	log := logger.L().With("topic", c.Reader.Config().Topic)
	log.Info("consumer started")

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msgs, err := c.fetchBatch(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			log.Error("fetch batch", "err", err)
			time.Sleep(time.Second)
			continue
		}
		if len(msgs) == 0 {
			continue
		}

		if err := c.Handler.Handle(ctx, msgs); err == nil {
			if err := c.commit(ctx, msgs); err != nil {
				log.Error("commit batch", "err", err)
			}
			continue
		} else {
			log.Warn("batch handler failed, falling back to per-message", "err", err, "size", len(msgs))
		}

		// Per-message fallback.
		for _, m := range msgs {
			if err := c.Handler.HandleOne(ctx, m); err != nil {
				log.Error("per-message handler failed, sending to DLQ", "err", err, "offset", m.Offset)
				c.toDLQ(ctx, m, err)
			}
		}
		if err := c.commit(ctx, msgs); err != nil {
			log.Error("commit after fallback", "err", err)
		}
	}
}

func (c *BatchConsumer) fetchBatch(ctx context.Context) ([]kafka.Message, error) {
	deadline := time.Now().Add(c.BatchWait)
	out := make([]kafka.Message, 0, c.BatchSize)
	first := true
	for len(out) < c.BatchSize {
		fetchCtx := ctx
		var cancel context.CancelFunc
		if !first {
			fetchCtx, cancel = context.WithDeadline(ctx, deadline)
		}
		msg, err := c.Reader.FetchMessage(fetchCtx)
		if cancel != nil {
			cancel()
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && !first {
				return out, nil
			}
			return out, err
		}
		out = append(out, msg)
		first = false
	}
	return out, nil
}

func (c *BatchConsumer) commit(ctx context.Context, msgs []kafka.Message) error {
	return c.Reader.CommitMessages(ctx, msgs...)
}

func (c *BatchConsumer) toDLQ(ctx context.Context, m kafka.Message, cause error) {
	b, _ := MarshalForDLQ(m, cause)
	if err := c.dlq.WriteMessages(ctx, kafka.Message{Key: m.Key, Value: b}); err != nil {
		logger.L().Error("dlq write failed", "err", err, "offset", m.Offset)
	}
}

// MarshalForDLQ formats a failed message + cause into the canonical DLQ
// payload shape. Used by the consumer's per-message fallback and exposed
// for handlers that need to produce the same envelope.
func MarshalForDLQ(m kafka.Message, cause error) ([]byte, error) {
	w := struct {
		Topic     string `json:"topic"`
		Partition int    `json:"partition"`
		Offset    int64  `json:"offset"`
		Error     string `json:"error"`
		Key       string `json:"key"`
		Value     []byte `json:"value"`
	}{m.Topic, m.Partition, m.Offset, cause.Error(), string(m.Key), m.Value}
	b, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("marshal dlq: %w", err)
	}
	return b, nil
}

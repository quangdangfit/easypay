package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/quangdangfit/easypay/internal/config"
)

// PaymentConfirmedEvent is fired by the webhook handler (Stripe) and the
// blockchain confirmation tracker after a payment reaches its terminal
// state. Consumers (settlement) react to this to call back the merchant.
//
// CallbackURL is intentionally NOT carried on the event — it lives on the
// merchant row, and the consumer looks it up on each delivery so a merchant
// rotating their callback URL takes effect immediately.
type PaymentConfirmedEvent struct {
	OrderID               string `json:"order_id"`
	MerchantID            string `json:"merchant_id"`
	Status                string `json:"status"`
	StripePaymentIntentID string `json:"stripe_payment_intent_id,omitempty"`
	Amount                int64  `json:"amount"`
	Currency              string `json:"currency"`
	ConfirmedAt           int64  `json:"confirmed_at"`
}

// EventPublisher fans out terminal-state notifications onto Kafka. The
// payment write path commits directly to MySQL — it does not publish here.
type EventPublisher interface {
	PublishPaymentConfirmed(ctx context.Context, event PaymentConfirmedEvent) error
	Close() error
}

type publisher struct {
	confirmed *kafka.Writer
}

func NewPublisher(cfg config.KafkaConfig) EventPublisher {
	// Best-effort eager topic creation. Some Kafka distributions (e.g. the
	// `confluent-local` image used in CI) ship with `auto.create.topics.enable`
	// disabled, so writing to a non-existent topic returns
	// "Unknown Topic Or Partition" before the writer's auto-create kicks in.
	ensureTopics(cfg.Brokers, cfg.TopicConfirmed)

	return &publisher{
		confirmed: &kafka.Writer{
			Addr:                   kafka.TCP(cfg.Brokers...),
			Topic:                  cfg.TopicConfirmed,
			Balancer:               &kafka.Hash{},
			RequiredAcks:           kafka.RequireAll,
			MaxAttempts:            3,
			BatchTimeout:           5 * time.Millisecond,
			BatchBytes:             64 * 1024,
			AllowAutoTopicCreation: true,
		},
	}
}

// ensureTopics dials the controller and idempotently creates the given
// topics if they don't already exist. Errors are logged but never fatal:
// if the broker has auto-create enabled or the topic already exists, the
// subsequent Writer call will succeed regardless.
func ensureTopics(brokers []string, topics ...string) {
	if len(brokers) == 0 {
		return
	}
	d := &kafka.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(context.Background(), "tcp", brokers[0])
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	controller, err := conn.Controller()
	if err != nil {
		return
	}
	ctrlConn, err := d.DialContext(context.Background(), "tcp", fmt.Sprintf("%s:%d", controller.Host, controller.Port))
	if err != nil {
		return
	}
	defer func() { _ = ctrlConn.Close() }()

	specs := make([]kafka.TopicConfig, 0, len(topics))
	for _, t := range topics {
		if t == "" {
			continue
		}
		specs = append(specs, kafka.TopicConfig{
			Topic:             t,
			NumPartitions:     1,
			ReplicationFactor: 1,
		})
	}
	_ = ctrlConn.CreateTopics(specs...)
}

func (p *publisher) PublishPaymentConfirmed(ctx context.Context, e PaymentConfirmedEvent) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal confirmed event: %w", err)
	}
	return p.confirmed.WriteMessages(ctx, kafka.Message{
		Key:   []byte(e.OrderID),
		Value: payload,
	})
}

func (p *publisher) Close() error {
	if p.confirmed == nil {
		return nil
	}
	return p.confirmed.Close()
}

// Pinger exposes a tiny ping for /readyz by issuing a metadata fetch via
// DialContext to one broker.
type Pinger struct {
	Brokers []string
}

func NewPinger(cfg config.KafkaConfig) *Pinger { return &Pinger{Brokers: cfg.Brokers} }

func (p *Pinger) Ping(ctx context.Context) error {
	if len(p.Brokers) == 0 {
		return fmt.Errorf("no brokers configured")
	}
	d := &kafka.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", p.Brokers[0])
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_, err = conn.Brokers()
	return err
}

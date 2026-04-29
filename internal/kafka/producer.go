package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/quangdangfit/easypay/internal/config"
)

// PaymentEvent is what the API handler enqueues for the async batch consumer.
type PaymentEvent struct {
	OrderID               string `json:"order_id"`
	MerchantID            string `json:"merchant_id"`
	TransactionID         string `json:"transaction_id"`
	Amount                int64  `json:"amount"`
	Currency              string `json:"currency"`
	PaymentMethod         string `json:"payment_method,omitempty"`
	Status                string `json:"status"`
	StripeSessionID       string `json:"stripe_session_id,omitempty"`
	StripePaymentIntentID string `json:"stripe_payment_intent_id,omitempty"`
	CheckoutURL           string `json:"checkout_url,omitempty"`
	CallbackURL           string `json:"callback_url,omitempty"`
	CreatedAt             int64  `json:"created_at"`
}

// PaymentConfirmedEvent is fired after webhook reconciliation.
type PaymentConfirmedEvent struct {
	OrderID               string `json:"order_id"`
	MerchantID            string `json:"merchant_id"`
	Status                string `json:"status"`
	StripePaymentIntentID string `json:"stripe_payment_intent_id,omitempty"`
	StripeChargeID        string `json:"stripe_charge_id,omitempty"`
	Amount                int64  `json:"amount"`
	Currency              string `json:"currency"`
	CallbackURL           string `json:"callback_url,omitempty"`
	ConfirmedAt           int64  `json:"confirmed_at"`
}

type EventPublisher interface {
	PublishPaymentEvent(ctx context.Context, event PaymentEvent) error
	PublishPaymentConfirmed(ctx context.Context, event PaymentConfirmedEvent) error
	Close() error
}

type publisher struct {
	events    *kafka.Writer
	confirmed *kafka.Writer
	dlq       *kafka.Writer
}

func NewPublisher(cfg config.KafkaConfig) EventPublisher {
	common := func(topic string) *kafka.Writer {
		return &kafka.Writer{
			Addr:                   kafka.TCP(cfg.Brokers...),
			Topic:                  topic,
			Balancer:               &kafka.Hash{},
			RequiredAcks:           kafka.RequireAll,
			MaxAttempts:            3,
			BatchTimeout:           5 * time.Millisecond,
			BatchBytes:             64 * 1024,
			AllowAutoTopicCreation: true,
		}
	}
	return &publisher{
		events:    common(cfg.TopicEvents),
		confirmed: common(cfg.TopicConfirmed),
		dlq:       common(cfg.TopicDLQ),
	}
}

func (p *publisher) PublishPaymentEvent(ctx context.Context, e PaymentEvent) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	return p.events.WriteMessages(ctx, kafka.Message{
		Key:   []byte(e.MerchantID),
		Value: payload,
	})
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
	var first error
	for _, w := range []*kafka.Writer{p.events, p.confirmed, p.dlq} {
		if w == nil {
			continue
		}
		if err := w.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// PingableWriter exposes a tiny ping for /readyz by issuing a metadata
// fetch via DialContext to one broker.
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
	defer conn.Close()
	_, err = conn.Brokers()
	return err
}

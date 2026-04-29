package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/quangdangfit/easypay/internal/config"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/repository"
)

// PaymentConsumer batches PaymentEvent messages from payment.events into MySQL
// using OrderRepository.BatchCreate. It implements kafka.BatchHandler.
type PaymentConsumer struct {
	repo repository.OrderRepository
}

func NewPaymentConsumer(repo repository.OrderRepository) *PaymentConsumer {
	return &PaymentConsumer{repo: repo}
}

func (p *PaymentConsumer) Handle(ctx context.Context, msgs []kafkago.Message) error {
	orders := make([]*domain.Order, 0, len(msgs))
	for _, m := range msgs {
		o, err := decode(m.Value)
		if err != nil {
			// One malformed message poisons the batch — caller will fall back
			// to per-message handling and DLQ this one.
			return fmt.Errorf("decode at offset %d: %w", m.Offset, err)
		}
		orders = append(orders, o)
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := p.repo.BatchCreate(c, orders); err != nil {
		return fmt.Errorf("batch insert: %w", err)
	}
	return nil
}

func (p *PaymentConsumer) HandleOne(ctx context.Context, m kafkago.Message) error {
	o, err := decode(m.Value)
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := p.repo.Create(c, o); err != nil {
		// Treat unique-key conflicts as success (already inserted by a retry).
		if isDuplicateError(err) {
			return nil
		}
		return fmt.Errorf("insert: %w", err)
	}
	return nil
}

// NewBatch wires a BatchConsumer for the payment.events topic with this handler.
func (p *PaymentConsumer) NewBatch(cfg config.KafkaConfig) *kafka.BatchConsumer {
	return kafka.NewBatchConsumer(cfg, cfg.TopicEvents, p)
}

func decode(b []byte) (*domain.Order, error) {
	var e kafka.PaymentEvent
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	if e.OrderID == "" || e.MerchantID == "" || e.TransactionID == "" {
		return nil, errors.New("missing required fields")
	}
	status := domain.OrderStatus(e.Status)
	if !status.Valid() {
		status = domain.OrderStatusPending
	}
	return &domain.Order{
		OrderID:               e.OrderID,
		MerchantID:            e.MerchantID,
		TransactionID:         e.TransactionID,
		Amount:                e.Amount,
		Currency:              e.Currency,
		Status:                status,
		PaymentMethod:         e.PaymentMethod,
		StripeSessionID:       e.StripeSessionID,
		StripePaymentIntentID: e.StripePaymentIntentID,
		CheckoutURL:           e.CheckoutURL,
		CallbackURL:           e.CallbackURL,
	}, nil
}

func isDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	// MySQL ER_DUP_ENTRY (1062). We avoid importing the driver here.
	return contains(err.Error(), "Error 1062") || contains(err.Error(), "Duplicate entry")
}

func contains(s, sub string) bool {
	return len(sub) <= len(s) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

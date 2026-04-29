package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// PendingOrder is a thin snapshot stashed in Redis right after the API
// handler publishes to Kafka, so that downstream lazy-checkout flows can
// resolve an order_id even before the async consumer has committed the row
// to MySQL.
type PendingOrder struct {
	OrderID       string `json:"order_id"`
	MerchantID    string `json:"merchant_id"`
	TransactionID string `json:"transaction_id"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	PaymentMethod string `json:"payment_method,omitempty"`
	CustomerEmail string `json:"customer_email,omitempty"`
	SuccessURL    string `json:"success_url,omitempty"`
	CancelURL     string `json:"cancel_url,omitempty"`
	CreatedAt     int64  `json:"created_at"`
}

type PendingOrderStore interface {
	Put(ctx context.Context, o *PendingOrder, ttl time.Duration) error
	Get(ctx context.Context, orderID string) (*PendingOrder, error)
}

type redisPendingOrders struct{ rc *redis.Client }

func NewPendingOrderStore(rc *redis.Client) PendingOrderStore {
	return &redisPendingOrders{rc: rc}
}

var ErrPendingOrderNotFound = errors.New("pending order not found")

func (s *redisPendingOrders) Put(ctx context.Context, o *PendingOrder, ttl time.Duration) error {
	b, err := json.Marshal(o)
	if err != nil {
		return fmt.Errorf("marshal pending order: %w", err)
	}
	if err := s.rc.Set(ctx, "order:"+o.OrderID, b, ttl).Err(); err != nil {
		return fmt.Errorf("put pending order: %w", err)
	}
	return nil
}

func (s *redisPendingOrders) Get(ctx context.Context, orderID string) (*PendingOrder, error) {
	b, err := s.rc.Get(ctx, "order:"+orderID).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrPendingOrderNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get pending order: %w", err)
	}
	var o PendingOrder
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, fmt.Errorf("decode pending order: %w", err)
	}
	return &o, nil
}

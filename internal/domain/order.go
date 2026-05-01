package domain

import (
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type OrderStatus string

const (
	OrderStatusCreated  OrderStatus = "created"
	OrderStatusPending  OrderStatus = "pending"
	OrderStatusPaid     OrderStatus = "paid"
	OrderStatusFailed   OrderStatus = "failed"
	OrderStatusExpired  OrderStatus = "expired"
	OrderStatusRefunded OrderStatus = "refunded"
)

func (s OrderStatus) Valid() bool {
	switch s {
	case OrderStatusCreated, OrderStatusPending, OrderStatusPaid,
		OrderStatusFailed, OrderStatusExpired, OrderStatusRefunded:
		return true
	}
	return false
}

// Order is the canonical app-layer representation of a payment.
//
// Wire types are strings even where storage is binary, because the rest of
// the system (logs, JSON, HMAC inputs, Stripe metadata) deals in strings:
//   - TransactionID is a 32-char lowercase hex (BINARY(16) on disk).
//   - OrderID       is a 24-char lowercase hex (BINARY(12) on disk).
//     The first two hex chars encode the shard index 0..15.
//
// Removed vs. the v1 schema (see migration 003):
//   - ID (surrogate AUTO_INCREMENT) — composite PK is the cluster key.
//   - CallbackURL — lives on Merchant; not per-order.
//   - CheckoutURL — reconstructed on read from StripeSessionID or OrderID.
//   - StripeChargeID — derivable from StripePaymentIntentID via Stripe.
type Order struct {
	MerchantID            string
	TransactionID         string
	OrderID               string
	Amount                int64
	Currency              string
	Status                OrderStatus
	PaymentMethod         string
	StripeSessionID       string
	StripePaymentIntentID string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// ShardOfOrderID extracts the shard index from an order_id hex string. The
// first byte (two hex chars) encodes the shard. Returns the index and an
// error if the hex is malformed or out of range.
func ShardOfOrderID(orderID string) (uint8, error) {
	if len(orderID) < 2 {
		return 0, fmt.Errorf("order_id too short: %q", orderID)
	}
	b, err := hex.DecodeString(orderID[:2])
	if err != nil {
		return 0, fmt.Errorf("decode shard byte: %w", err)
	}
	return b[0], nil
}

// ErrInvalidOrderID is returned when an order_id fails structural validation.
var ErrInvalidOrderID = errors.New("invalid order_id")

// ValidateOrderID checks an incoming order_id string is the expected 24-char
// lowercase hex. Cheap rejection at the API boundary before we route to a
// shard.
func ValidateOrderID(orderID string) error {
	if len(orderID) != 24 {
		return fmt.Errorf("%w: want 24 hex chars, got %d", ErrInvalidOrderID, len(orderID))
	}
	if _, err := hex.DecodeString(orderID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidOrderID, err)
	}
	return nil
}

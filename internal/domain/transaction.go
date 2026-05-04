package domain

import (
	"errors"
	"fmt"
	"time"
)

type TransactionStatus string

const (
	TransactionStatusCreated  TransactionStatus = "created"
	TransactionStatusPending  TransactionStatus = "pending"
	TransactionStatusPaid     TransactionStatus = "paid"
	TransactionStatusFailed   TransactionStatus = "failed"
	TransactionStatusExpired  TransactionStatus = "expired"
	TransactionStatusRefunded TransactionStatus = "refunded"
)

func (s TransactionStatus) Valid() bool {
	switch s {
	case TransactionStatusCreated, TransactionStatusPending, TransactionStatusPaid,
		TransactionStatusFailed, TransactionStatusExpired, TransactionStatusRefunded:
		return true
	}
	return false
}

// Transaction is the canonical app-layer representation of a row in the
// `transactions` table.
//
//   - OrderID       is the merchant-supplied idempotency key (VARCHAR(64)).
//   - TransactionID is the gateway-derived 32-char hex id (BINARY(16) on disk),
//     computed deterministically from (merchant_id, order_id).
type Transaction struct {
	MerchantID            string
	TransactionID         string
	OrderID               string
	Amount                int64
	Currency              string
	Status                TransactionStatus
	PaymentMethod         string
	StripeSessionID       string
	StripePaymentIntentID string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	// ShardIndex is the merchant's logical shard, copied from
	// merchants.shard_index. Set by callers before Insert and stamped on
	// every row read. Follow-up writes (UpdateStatus, UpdateCheckout) read
	// it so the row stays on the physical shard it was first written to.
	ShardIndex uint8
}

// ErrInvalidOrderID is returned when a merchant-supplied order_id fails
// structural validation.
var ErrInvalidOrderID = errors.New("invalid order_id")

// ValidateOrderID enforces the constraints we put on merchant-supplied
// order ids:
//   - 1..64 chars
//   - only [A-Za-z0-9._:-] (safe in URLs and Stripe metadata, no whitespace)
func ValidateOrderID(orderID string) error {
	n := len(orderID)
	if n == 0 || n > 64 {
		return fmt.Errorf("%w: length must be 1..64, got %d", ErrInvalidOrderID, n)
	}
	for i := 0; i < n; i++ {
		c := orderID[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == ':' || c == '-':
		default:
			return fmt.Errorf("%w: invalid char %q at %d", ErrInvalidOrderID, c, i)
		}
	}
	return nil
}

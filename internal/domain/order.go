package domain

import "time"

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

type Order struct {
	ID                    int64
	OrderID               string
	MerchantID            string
	TransactionID         string
	Amount                int64
	Currency              string
	Status                OrderStatus
	PaymentMethod         string
	StripeSessionID       string
	StripePaymentIntentID string
	StripeChargeID        string
	CheckoutURL           string
	CallbackURL           string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

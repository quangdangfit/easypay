package domain

import "time"

type MerchantStatus string

const (
	MerchantStatusActive    MerchantStatus = "active"
	MerchantStatusSuspended MerchantStatus = "suspended"
)

type Merchant struct {
	ID          int64
	MerchantID  string
	Name        string
	APIKey      string
	SecretKey   string
	CallbackURL string
	RateLimit   int
	Status      MerchantStatus
	CreatedAt   time.Time
}

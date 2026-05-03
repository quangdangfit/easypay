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
	// ShardIndex is the merchant's logical shard, in [0, LogicalShardCount).
	// Picked at create time by least-loaded selection. Today every shard
	// lives on the same physical `transactions` table; future deployments
	// can use this column to route a merchant to a specific physical
	// location without changing application code.
	ShardIndex uint8
	CreatedAt  time.Time
}

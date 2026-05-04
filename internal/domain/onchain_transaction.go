package domain

import (
	"math/big"
	"time"
)

// OnchainTxStatus tracks the lifecycle of an on-chain payment we observe.
type OnchainTxStatus string

const (
	OnchainTxStatusPending   OnchainTxStatus = "pending"
	OnchainTxStatusConfirmed OnchainTxStatus = "confirmed"
	OnchainTxStatusFailed    OnchainTxStatus = "failed"
	OnchainTxStatusReorged   OnchainTxStatus = "reorged"
)

// OnchainTransaction represents a row in the `onchain_transactions` table:
// a smart-contract payment event we've observed and are waiting to confirm.
type OnchainTransaction struct {
	ID              int64
	TxHash          string
	BlockNumber     uint64
	MerchantID      string
	OrderID         string
	Payer           string
	Token           string
	Amount          *big.Int
	ChainID         int64
	Confirmations   uint64
	RequiredConfirm uint64
	Status          OnchainTxStatus
	CreatedAt       time.Time
}

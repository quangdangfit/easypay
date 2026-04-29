package domain

import (
	"math/big"
	"time"
)

type PendingTxStatus string

const (
	PendingTxStatusPending   PendingTxStatus = "pending"
	PendingTxStatusConfirmed PendingTxStatus = "confirmed"
	PendingTxStatusFailed    PendingTxStatus = "failed"
	PendingTxStatusReorged   PendingTxStatus = "reorged"
)

type PendingTx struct {
	ID              int64
	TxHash          string
	BlockNumber     uint64
	OrderID         string
	Payer           string
	Token           string
	Amount          *big.Int
	ChainID         int64
	Confirmations   uint64
	RequiredConfirm uint64
	Status          PendingTxStatus
	CreatedAt       time.Time
}

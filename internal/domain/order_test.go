package domain

import (
	"math/big"
	"testing"
	"time"
)

func TestPendingTxAndMerchantSmoke(t *testing.T) {
	// Coverage smoke for fields — ensures struct literals compile and round-trip.
	tx := PendingTx{
		ID: 1, TxHash: "0x", BlockNumber: 1, OrderID: "ord-1",
		Amount: big.NewInt(1), ChainID: 1, Confirmations: 1, RequiredConfirm: 12,
		Status: PendingTxStatusPending, CreatedAt: time.Now(),
	}
	m := Merchant{
		ID: 1, MerchantID: "M1", APIKey: "k", SecretKey: "s",
		RateLimit: 1000, Status: MerchantStatusActive,
	}
	if tx.OrderID != "ord-1" || m.MerchantID != "M1" {
		t.Fatal("smoke")
	}
}

func TestOrderStatus_Valid(t *testing.T) {
	valid := []OrderStatus{
		OrderStatusCreated, OrderStatusPending, OrderStatusPaid,
		OrderStatusFailed, OrderStatusExpired, OrderStatusRefunded,
	}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("expected %q valid", s)
		}
	}
	for _, s := range []OrderStatus{"", "unknown", "PAID"} {
		if s.Valid() {
			t.Errorf("expected %q invalid", s)
		}
	}
}

package domain

import (
	"errors"
	"math/big"
	"testing"
	"time"
)

func TestOnchainAndMerchantSmoke(t *testing.T) {
	tx := OnchainTransaction{
		ID: 1, TxHash: "0x", BlockNumber: 1, OrderID: "ord-1",
		Amount: big.NewInt(1), ChainID: 1, Confirmations: 1, RequiredConfirm: 12,
		Status: OnchainTxStatusPending, CreatedAt: time.Now(),
	}
	m := Merchant{
		ID: 1, MerchantID: "M1", APIKey: "k", SecretKey: "s",
		RateLimit: 1000, Status: MerchantStatusActive, ShardIndex: 3,
	}
	if tx.OrderID != "ord-1" || m.MerchantID != "M1" || m.ShardIndex != 3 {
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

func TestValidateOrderID(t *testing.T) {
	good := []string{
		"a",
		"order-1",
		"my.cart_v2:42",
		"ABCdef-0123_456.xyz:99",
		// 64 chars, max length.
		"abcdefghij0123456789ABCDEFGHIJ0123456789-_.:abcdefghij0123456789aB",
	}
	for _, ok := range good {
		if len(ok) > 64 {
			ok = ok[:64]
		}
		if err := ValidateOrderID(ok); err != nil {
			t.Errorf("valid order_id %q rejected: %v", ok, err)
		}
	}
	bad := []string{
		"",
		"has space",
		"has/slash",
		"has?query",
		"with#hash",
		// 65 chars, over limit.
		"abcdefghij0123456789abcdefghij0123456789abcdefghij0123456789abcdef",
	}
	for _, b := range bad {
		err := ValidateOrderID(b)
		if err == nil {
			t.Errorf("ValidateOrderID(%q) want err, got nil", b)
			continue
		}
		if !errors.Is(err, ErrInvalidOrderID) {
			t.Errorf("ValidateOrderID(%q) want ErrInvalidOrderID, got %v", b, err)
		}
	}
}

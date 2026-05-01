package domain

import (
	"errors"
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

func TestShardOfOrderID(t *testing.T) {
	cases := []struct {
		in   string
		want uint8
		err  bool
	}{
		{"00abcdef0123456789abcdef", 0x00, false},
		{"0fabcdef0123456789abcdef", 0x0f, false},
		{"05deadbeefdeadbeefdeadbe", 0x05, false},
		{"", 0, true},
		{"x", 0, true},
		{"zz", 0, true},
	}
	for _, c := range cases {
		got, err := ShardOfOrderID(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ShardOfOrderID(%q) want err, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ShardOfOrderID(%q) unexpected err: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ShardOfOrderID(%q) = %#x, want %#x", c.in, got, c.want)
		}
	}
}

func TestValidateOrderID(t *testing.T) {
	if err := ValidateOrderID("00abcdef0123456789abcdef"); err != nil {
		t.Errorf("valid order_id rejected: %v", err)
	}
	for _, bad := range []string{"", "tooshort", "00abcdef0123456789abcdef0", "00abcdef0123456789abcdez"} {
		err := ValidateOrderID(bad)
		if err == nil {
			t.Errorf("ValidateOrderID(%q) want err, got nil", bad)
			continue
		}
		if !errors.Is(err, ErrInvalidOrderID) {
			t.Errorf("ValidateOrderID(%q) want ErrInvalidOrderID, got %v", bad, err)
		}
	}
}

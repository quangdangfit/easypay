package checkouttoken

import (
	"errors"
	"testing"
	"time"
)

func TestRoundtrip(t *testing.T) {
	tok := Sign("secret", "M1", "ord-1", 5*time.Minute)
	if err := Verify("secret", "M1", "ord-1", tok); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestRejectsTamperedOrder(t *testing.T) {
	tok := Sign("secret", "M1", "ord-1", 5*time.Minute)
	if err := Verify("secret", "M1", "ord-2", tok); !errors.Is(err, ErrSignature) {
		t.Fatalf("want sig err, got %v", err)
	}
}

func TestRejectsTamperedMerchant(t *testing.T) {
	tok := Sign("secret", "M1", "ord-1", 5*time.Minute)
	if err := Verify("secret", "M2", "ord-1", tok); !errors.Is(err, ErrSignature) {
		t.Fatalf("want sig err, got %v", err)
	}
}

func TestRejectsExpired(t *testing.T) {
	tok := Sign("secret", "M1", "ord-1", -1*time.Second)
	if err := Verify("secret", "M1", "ord-1", tok); !errors.Is(err, ErrExpired) {
		t.Fatalf("want expired err, got %v", err)
	}
}

func TestRejectsMalformed(t *testing.T) {
	if err := Verify("secret", "M1", "ord-1", "garbage"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("want malformed, got %v", err)
	}
}

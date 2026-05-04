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

func TestRejectsBadExpiryFormat(t *testing.T) {
	if err := Verify("secret", "M1", "ord-1", "not-a-number.sig"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("want malformed for bad expiry, got %v", err)
	}
}

func TestRejectsTamperedSecret(t *testing.T) {
	tok := Sign("secret1", "M1", "ord-1", 5*time.Minute)
	if err := Verify("secret2", "M1", "ord-1", tok); !errors.Is(err, ErrSignature) {
		t.Fatalf("want sig err for different secret, got %v", err)
	}
}

func TestRejectsNoDotsInToken(t *testing.T) {
	if err := Verify("secret", "M1", "ord-1", "nodothere"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("want malformed for no dots, got %v", err)
	}
}

func TestRejectsEmptyToken(t *testing.T) {
	if err := Verify("secret", "M1", "ord-1", ""); !errors.Is(err, ErrMalformed) {
		t.Fatalf("want malformed for empty token, got %v", err)
	}
}

func TestAcceptsEdgeCaseExpiryValues(t *testing.T) {
	// Test with expiry far in the future
	tok := Sign("secret", "M1", "ord-1", 365*24*time.Hour)
	if err := Verify("secret", "M1", "ord-1", tok); err != nil {
		t.Fatalf("should accept far-future expiry: %v", err)
	}
}

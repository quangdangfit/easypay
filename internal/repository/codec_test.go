package repository

import (
	"strings"
	"testing"

	"github.com/quangdangfit/easypay/internal/domain"
)

func TestEncodeDecodeStatus_Roundtrip(t *testing.T) {
	all := []domain.TransactionStatus{
		domain.TransactionStatusCreated,
		domain.TransactionStatusPending,
		domain.TransactionStatusPaid,
		domain.TransactionStatusFailed,
		domain.TransactionStatusExpired,
		domain.TransactionStatusRefunded,
	}
	for _, s := range all {
		code, err := encodeStatus(s)
		if err != nil {
			t.Fatalf("encode %s: %v", s, err)
		}
		got, err := decodeStatus(code)
		if err != nil {
			t.Fatalf("decode %d: %v", code, err)
		}
		if got != s {
			t.Errorf("roundtrip mismatch: %s -> %d -> %s", s, code, got)
		}
	}
}

func TestEncodeStatus_Unknown(t *testing.T) {
	if _, err := encodeStatus(domain.TransactionStatus("nope")); err == nil {
		t.Fatal("expected error for unknown status")
	}
}

func TestDecodeStatus_Unknown(t *testing.T) {
	if _, err := decodeStatus(99); err == nil {
		t.Fatal("expected error for unknown status code")
	}
}

func TestEncodeDecodeMethod_Roundtrip(t *testing.T) {
	cases := []string{
		"card", "crypto_eth", "wallet", "bank_transfer",
		"klarna", "afterpay", "affirm", "sepa_debit", "us_bank_account",
	}
	for _, m := range cases {
		code := encodeMethod(m)
		if got := decodeMethod(code); got != m {
			t.Errorf("roundtrip %s -> %d -> %s", m, code, got)
		}
	}
}

func TestEncodeMethod_UnknownFallsBackToUnknown(t *testing.T) {
	if got := decodeMethod(encodeMethod("not-a-method")); got != "unknown" {
		t.Fatalf("got %q, want \"unknown\"", got)
	}
}

func TestDecodeMethod_UnknownCode(t *testing.T) {
	if got := decodeMethod(250); got != "unknown" {
		t.Fatalf("got %q", got)
	}
}

func TestEncodeMethod_CaseInsensitive(t *testing.T) {
	if encodeMethod("CARD") != encodeMethod("card") {
		t.Fatal("expected case-insensitive match")
	}
}

func TestEncodeDecodeCurrency_Roundtrip(t *testing.T) {
	for _, c := range []string{"USD", "EUR", "JPY", "VND", "GBP"} {
		code, err := encodeCurrency(c)
		if err != nil {
			t.Fatalf("encode %s: %v", c, err)
		}
		got, err := decodeCurrency(code)
		if err != nil {
			t.Fatalf("decode %d: %v", code, err)
		}
		if got != c {
			t.Errorf("roundtrip %s -> %d -> %s", c, code, got)
		}
	}
}

func TestEncodeCurrency_LowercaseAccepted(t *testing.T) {
	a, err := encodeCurrency("usd")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := encodeCurrency("USD")
	if a != b {
		t.Fatalf("case sensitivity mismatch: %d vs %d", a, b)
	}
}

func TestEncodeCurrency_Unknown(t *testing.T) {
	if _, err := encodeCurrency("XYZ"); err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeCurrency_Unknown(t *testing.T) {
	if _, err := decodeCurrency(0); err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeHex16(t *testing.T) {
	good := "0123456789abcdef0123456789abcdef"
	b, err := decodeHex16(good)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(b) != 16 {
		t.Fatalf("len: %d", len(b))
	}

	if _, err := decodeHex16("short"); err == nil {
		t.Fatal("want length error")
	}
	if _, err := decodeHex16(strings.Repeat("g", 32)); err == nil {
		t.Fatal("want hex decode error")
	}
}

func TestIsDuplicateKeyErr_Variants(t *testing.T) {
	if !isDuplicateKeyErr(stringErr("Error 1062: Duplicate entry")) {
		t.Fatal("Error 1062 should match")
	}
	if !isDuplicateKeyErr(stringErr("Duplicate entry foo for key bar")) {
		t.Fatal("Duplicate entry should match")
	}
	if isDuplicateKeyErr(nil) {
		t.Fatal("nil must not match")
	}
	if isDuplicateKeyErr(stringErr("Error 1213: deadlock")) {
		t.Fatal("non-1062 must not match")
	}
}

type stringErr string

func (s stringErr) Error() string { return string(s) }

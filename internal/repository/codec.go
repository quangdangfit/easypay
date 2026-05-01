package repository

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/quangdangfit/easypay/internal/domain"
)

// codec.go encodes the app-layer (string) types to the on-disk binary /
// integer representations used by the v2 sharded schema, and vice-versa.
//
// Status, payment_method, and currency_code are integer-coded to keep rows
// fixed-width and small. The lookup tables here are the source of truth;
// keep them in sync with migration 003's column comments.

// --- status -----------------------------------------------------------------

const (
	statusCreated  uint8 = 0
	statusPending  uint8 = 1
	statusPaid     uint8 = 2
	statusFailed   uint8 = 3
	statusExpired  uint8 = 4
	statusRefunded uint8 = 5
)

func encodeStatus(s domain.OrderStatus) (uint8, error) {
	switch s {
	case domain.OrderStatusCreated:
		return statusCreated, nil
	case domain.OrderStatusPending:
		return statusPending, nil
	case domain.OrderStatusPaid:
		return statusPaid, nil
	case domain.OrderStatusFailed:
		return statusFailed, nil
	case domain.OrderStatusExpired:
		return statusExpired, nil
	case domain.OrderStatusRefunded:
		return statusRefunded, nil
	}
	return 0, fmt.Errorf("encode status: unknown %q", s)
}

func decodeStatus(v uint8) (domain.OrderStatus, error) {
	switch v {
	case statusCreated:
		return domain.OrderStatusCreated, nil
	case statusPending:
		return domain.OrderStatusPending, nil
	case statusPaid:
		return domain.OrderStatusPaid, nil
	case statusFailed:
		return domain.OrderStatusFailed, nil
	case statusExpired:
		return domain.OrderStatusExpired, nil
	case statusRefunded:
		return domain.OrderStatusRefunded, nil
	}
	return "", fmt.Errorf("decode status: unknown %d", v)
}

// --- payment_method ---------------------------------------------------------

const (
	methodCard         uint8 = 0
	methodCryptoEth    uint8 = 1
	methodWallet       uint8 = 2
	methodBankTransfer uint8 = 3
	methodBNPLKlarna   uint8 = 4
	methodBNPLAfterpay uint8 = 5
	methodBNPLAffirm   uint8 = 6
	methodSEPA         uint8 = 7
	methodACH          uint8 = 8
	methodUnknown      uint8 = 9
)

var methodToCode = map[string]uint8{
	"card":            methodCard,
	"crypto_eth":      methodCryptoEth,
	"wallet":          methodWallet,
	"bank_transfer":   methodBankTransfer,
	"klarna":          methodBNPLKlarna,
	"afterpay":        methodBNPLAfterpay,
	"affirm":          methodBNPLAffirm,
	"sepa_debit":      methodSEPA,
	"us_bank_account": methodACH,
}

var codeToMethod = map[uint8]string{
	methodCard:         "card",
	methodCryptoEth:    "crypto_eth",
	methodWallet:       "wallet",
	methodBankTransfer: "bank_transfer",
	methodBNPLKlarna:   "klarna",
	methodBNPLAfterpay: "afterpay",
	methodBNPLAffirm:   "affirm",
	methodSEPA:         "sepa_debit",
	methodACH:          "us_bank_account",
	methodUnknown:      "unknown",
}

func encodeMethod(s string) uint8 {
	if v, ok := methodToCode[strings.ToLower(s)]; ok {
		return v
	}
	return methodUnknown
}

func decodeMethod(v uint8) string {
	if s, ok := codeToMethod[v]; ok {
		return s
	}
	return "unknown"
}

// --- currency ---------------------------------------------------------------

// ISO 4217 numeric. Add to this list when introducing new fiat support; the
// migration column is SMALLINT UNSIGNED so any 0..65535 code fits.
var currencyToCode = map[string]uint16{
	"USD": 840,
	"EUR": 978,
	"GBP": 826,
	"JPY": 392,
	"CNY": 156,
	"AUD": 36,
	"CAD": 124,
	"CHF": 756,
	"HKD": 344,
	"IDR": 360,
	"INR": 356,
	"KRW": 410,
	"MYR": 458,
	"NZD": 554,
	"PHP": 608,
	"SGD": 702,
	"THB": 764,
	"TWD": 901,
	"VND": 704,
}

var codeToCurrency = func() map[uint16]string {
	m := make(map[uint16]string, len(currencyToCode))
	for k, v := range currencyToCode {
		m[v] = k
	}
	return m
}()

func encodeCurrency(s string) (uint16, error) {
	v, ok := currencyToCode[strings.ToUpper(s)]
	if !ok {
		return 0, fmt.Errorf("encode currency: unknown %q", s)
	}
	return v, nil
}

func decodeCurrency(v uint16) (string, error) {
	s, ok := codeToCurrency[v]
	if !ok {
		return "", fmt.Errorf("decode currency: unknown numeric %d", v)
	}
	return s, nil
}

// --- id encoding ------------------------------------------------------------

func decodeHex16(s string) ([]byte, error) {
	if len(s) != 32 {
		return nil, fmt.Errorf("hex16: want 32 chars, got %d", len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("hex16: %w", err)
	}
	return b, nil
}

func decodeHex12(s string) ([]byte, error) {
	if len(s) != 24 {
		return nil, fmt.Errorf("hex12: want 24 chars, got %d", len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("hex12: %w", err)
	}
	return b, nil
}

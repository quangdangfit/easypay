// Package checkouttoken signs and verifies short-lived tokens embedded in
// hosted-checkout URLs (e.g. https://pay.example/pay/<order_id>?t=<token>).
//
// Token format: <expiry_unix>.<hex(hmac_sha256(order_id+"."+expiry, secret))>
//
// Reasons:
//   - prevents enumeration of order_ids (an attacker can't probe random ids)
//   - bounds replay window (default 24h, matches Stripe Session expiry)
//   - lets us safely expose order_id in URLs without leaking customer PII
package checkouttoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	ErrMalformed = errors.New("checkout token malformed")
	ErrExpired   = errors.New("checkout token expired")
	ErrSignature = errors.New("checkout token signature mismatch")
)

// Sign returns a token bound to orderID with the given lifetime.
func Sign(secret, orderID string, lifetime time.Duration) string {
	expiry := time.Now().Add(lifetime).Unix()
	return signWithExpiry(secret, orderID, expiry)
}

// Verify validates the token against orderID. Returns nil on success or one
// of the typed errors above on failure.
func Verify(secret, orderID, token string) error {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return ErrMalformed
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return ErrMalformed
	}
	if time.Now().Unix() > expiry {
		return ErrExpired
	}
	expected := signWithExpiry(secret, orderID, expiry)
	if !hmac.Equal([]byte(expected), []byte(token)) {
		return ErrSignature
	}
	return nil
}

func signWithExpiry(secret, orderID string, expiry int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%s.%d", orderID, expiry)
	return fmt.Sprintf("%d.%s", expiry, hex.EncodeToString(mac.Sum(nil)))
}

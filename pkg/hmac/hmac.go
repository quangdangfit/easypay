package hmac

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// Sign returns the lowercase hex HMAC-SHA256 of payload using secret.
func Sign(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify returns true iff the provided hex signature matches HMAC-SHA256(payload, secret).
// Comparison is constant-time. Hex casing is ignored.
func Verify(secret string, payload []byte, hexSignature string) bool {
	expected, err := hex.DecodeString(hexSignature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hmac.Equal(expected, mac.Sum(nil))
}

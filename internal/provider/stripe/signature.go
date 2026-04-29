package stripe

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// Stripe's recommended replay tolerance.
	defaultTolerance = 5 * time.Minute
)

var (
	ErrSignatureMissing  = errors.New("stripe-signature header missing")
	ErrSignatureFormat   = errors.New("stripe-signature malformed")
	ErrSignatureMismatch = errors.New("stripe signature mismatch")
	ErrSignatureExpired  = errors.New("stripe signature timestamp outside tolerance")
)

// VerifyAndConstructEvent parses and verifies a Stripe webhook payload using
// the format: `Stripe-Signature: t=<unix>,v1=<hex>[,v0=<hex>]`.
//
// The signed payload is `{t}.{rawBody}` and signed with HMAC-SHA256(secret).
// We accept any v1 entry that matches; we reject v0-only signatures.
//
// Returns a parsed Event with raw object JSON ready for downstream routing.
func VerifyAndConstructEvent(payload []byte, sigHeader, secret string) (*Event, error) {
	return verifyWithTolerance(payload, sigHeader, secret, defaultTolerance, time.Now)
}

func verifyWithTolerance(payload []byte, sigHeader, secret string, tolerance time.Duration, now func() time.Time) (*Event, error) {
	if sigHeader == "" {
		return nil, ErrSignatureMissing
	}
	if secret == "" {
		return nil, errors.New("stripe webhook secret not configured")
	}

	var ts int64
	v1Sigs := make([]string, 0, 2)
	for _, part := range strings.Split(sigHeader, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			return nil, ErrSignatureFormat
		}
		switch kv[0] {
		case "t":
			n, err := strconv.ParseInt(kv[1], 10, 64)
			if err != nil {
				return nil, ErrSignatureFormat
			}
			ts = n
		case "v1":
			v1Sigs = append(v1Sigs, kv[1])
		}
	}
	if ts == 0 || len(v1Sigs) == 0 {
		return nil, ErrSignatureFormat
	}
	if abs64(now().Unix()-ts) > int64(tolerance.Seconds()) {
		return nil, ErrSignatureExpired
	}

	signedPayload := fmt.Sprintf("%d.%s", ts, payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	expected := mac.Sum(nil)

	matched := false
	for _, sig := range v1Sigs {
		got, err := hex.DecodeString(sig)
		if err != nil {
			continue
		}
		if hmac.Equal(got, expected) {
			matched = true
			break
		}
	}
	if !matched {
		return nil, ErrSignatureMismatch
	}

	// Extract the parts we need without dragging in stripe.Event (which carries
	// many helpers we don't use). We re-marshal `data.object` so callers can
	// unmarshal into their own typed struct.
	var raw struct {
		ID         string          `json:"id"`
		Type       string          `json:"type"`
		Created    int64           `json:"created"`
		APIVersion string          `json:"api_version"`
		Data       struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("decode event: %w", err)
	}
	return &Event{
		ID:         raw.ID,
		Type:       raw.Type,
		Created:    raw.Created,
		APIVersion: raw.APIVersion,
		Data:       []byte(raw.Data.Object),
	}, nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// SignPayload is exposed for tests and the local Stripe mock.
func SignPayload(payload []byte, secret string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d.%s", ts, payload)))
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

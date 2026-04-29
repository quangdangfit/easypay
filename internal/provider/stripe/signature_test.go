package stripe

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const testSecret = "whsec_test_abcdef"

func TestVerify_AcceptsValid(t *testing.T) {
	payload := []byte(`{"id":"evt_1","type":"payment_intent.succeeded","data":{"object":{"id":"pi_1","amount":1000}}}`)
	now := time.Now().Unix()
	hdr := SignPayload(payload, testSecret, now)
	ev, err := verifyWithTolerance(payload, hdr, testSecret, 5*time.Minute, time.Now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.ID != "evt_1" || ev.Type != "payment_intent.succeeded" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestVerify_RejectsTamperedPayload(t *testing.T) {
	payload := []byte(`{"id":"evt_1","data":{"object":{"id":"pi_1"}}}`)
	hdr := SignPayload(payload, testSecret, time.Now().Unix())
	tampered := []byte(strings.Replace(string(payload), "pi_1", "pi_X", 1))
	if _, err := verifyWithTolerance(tampered, hdr, testSecret, 5*time.Minute, time.Now); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}
}

func TestVerify_RejectsExpired(t *testing.T) {
	payload := []byte(`{"id":"evt_1","data":{"object":{}}}`)
	old := time.Now().Add(-10 * time.Minute).Unix()
	hdr := SignPayload(payload, testSecret, old)
	if _, err := verifyWithTolerance(payload, hdr, testSecret, 5*time.Minute, time.Now); !errors.Is(err, ErrSignatureExpired) {
		t.Fatalf("expected expired error, got %v", err)
	}
}

func TestVerify_RejectsMissingHeader(t *testing.T) {
	if _, err := verifyWithTolerance([]byte("{}"), "", testSecret, time.Minute, time.Now); !errors.Is(err, ErrSignatureMissing) {
		t.Fatalf("expected missing header, got %v", err)
	}
}

func TestVerify_RejectsMalformedHeader(t *testing.T) {
	if _, err := verifyWithTolerance([]byte("{}"), "garbage", testSecret, time.Minute, time.Now); !errors.Is(err, ErrSignatureFormat) {
		t.Fatalf("expected format error, got %v", err)
	}
}

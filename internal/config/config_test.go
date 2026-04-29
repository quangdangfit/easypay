package config

import (
	"strings"
	"testing"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestLoad_HappyPath(t *testing.T) {
	setEnv(t, map[string]string{
		"DB_DSN":                "user:pass@tcp(localhost:3306)/payments",
		"HMAC_SECRET":           "this-is-at-least-16-chars",
		"STRIPE_SECRET_KEY":     "sk_test_x",
		"STRIPE_WEBHOOK_SECRET": "whsec_x",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.App.Port != 8080 {
		t.Errorf("default port: %d", cfg.App.Port)
	}
	if cfg.Stripe.Mode != "live" {
		t.Errorf("default mode: %s", cfg.Stripe.Mode)
	}
	if cfg.App.PublicBaseURL != "http://localhost:8080" {
		t.Errorf("default public base: %s", cfg.App.PublicBaseURL)
	}
	if !strings.HasSuffix(cfg.App.CheckoutDefaultSuccessURL, "/checkout/success") {
		t.Errorf("default success url: %s", cfg.App.CheckoutDefaultSuccessURL)
	}
}

func TestLoad_FakeStripeBypassesKeys(t *testing.T) {
	setEnv(t, map[string]string{
		"DB_DSN":      "user:pass@tcp(x)/y",
		"HMAC_SECRET": "this-is-at-least-16-chars",
		"STRIPE_MODE": "fake",
	})
	if _, err := Load(); err != nil {
		t.Fatalf("fake mode shouldn't require keys: %v", err)
	}
}

func TestLoad_RejectsShortHMAC(t *testing.T) {
	setEnv(t, map[string]string{
		"DB_DSN":                "user:pass@tcp(x)/y",
		"HMAC_SECRET":           "tooshort",
		"STRIPE_SECRET_KEY":     "sk_test_x",
		"STRIPE_WEBHOOK_SECRET": "whsec_x",
	})
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "HMAC_SECRET") {
		t.Fatalf("want HMAC error, got %v", err)
	}
}

func TestLoad_RejectsLiveStripeWithoutKeys(t *testing.T) {
	setEnv(t, map[string]string{
		"DB_DSN":      "user:pass@tcp(x)/y",
		"HMAC_SECRET": "this-is-at-least-16-chars",
		"STRIPE_MODE": "live",
	})
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "STRIPE_SECRET_KEY") {
		t.Fatalf("want stripe key error, got %v", err)
	}
}

func TestLoad_RejectsUnknownMode(t *testing.T) {
	setEnv(t, map[string]string{
		"DB_DSN":      "user:pass@tcp(x)/y",
		"HMAC_SECRET": "this-is-at-least-16-chars",
		"STRIPE_MODE": "bogus",
	})
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "STRIPE_MODE") {
		t.Fatalf("want mode error, got %v", err)
	}
}

func TestNonNegUint64(t *testing.T) {
	cases := []struct {
		in   int
		want uint64
	}{
		{0, 0}, {12, 12}, {-1, 0}, {-99, 0},
	}
	for _, c := range cases {
		if got := nonNegUint64(c.in); got != c.want {
			t.Errorf("nonNegUint64(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestGetenvIntFallback(t *testing.T) {
	t.Setenv("X_INT_TEST", "")
	if v := getenvInt("X_INT_TEST", 42); v != 42 {
		t.Errorf("empty fallback: %d", v)
	}
	t.Setenv("X_INT_TEST", "not-a-number")
	if v := getenvInt("X_INT_TEST", 7); v != 7 {
		t.Errorf("bad-int fallback: %d", v)
	}
	t.Setenv("X_INT_TEST", "99")
	if v := getenvInt("X_INT_TEST", 0); v != 99 {
		t.Errorf("parsed: %d", v)
	}
}

func TestGetenvBool(t *testing.T) {
	t.Setenv("X_BOOL", "true")
	if !getenvBool("X_BOOL", false) {
		t.Error("true")
	}
	t.Setenv("X_BOOL", "garbage")
	if getenvBool("X_BOOL", false) != false {
		t.Error("bad fallback")
	}
}

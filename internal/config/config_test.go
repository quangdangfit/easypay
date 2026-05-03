package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeYAML drops a config file in t.TempDir and returns its path.
func writeYAML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const happyYAML = `
db:
  dsn: "user:pass@tcp(localhost:3306)/payments"
security:
  hmac_secret: "this-is-at-least-16-chars"
stripe:
  secret_key: "sk_test_x"
  webhook_secret: "whsec_x"
`

func TestLoad_HappyPath(t *testing.T) {
	cfg, err := Load(writeYAML(t, happyYAML))
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
	if cfg.App.CheckoutTokenTTL != 24*time.Hour {
		t.Errorf("default checkout token ttl: %s", cfg.App.CheckoutTokenTTL)
	}
	if cfg.Security.HMACTimestampSkew != 5*time.Minute {
		t.Errorf("default hmac skew: %s", cfg.Security.HMACTimestampSkew)
	}
}

func TestLoad_FakeStripeBypassesKeys(t *testing.T) {
	yaml := `
db:
  dsn: "user:pass@tcp(x)/y"
security:
  hmac_secret: "this-is-at-least-16-chars"
stripe:
  mode: fake
`
	if _, err := Load(writeYAML(t, yaml)); err != nil {
		t.Fatalf("fake mode shouldn't require keys: %v", err)
	}
}

func TestLoad_RejectsShortHMAC(t *testing.T) {
	yaml := `
db:
  dsn: "user:pass@tcp(x)/y"
security:
  hmac_secret: "tooshort"
stripe:
  secret_key: sk_test_x
  webhook_secret: whsec_x
`
	_, err := Load(writeYAML(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "hmac_secret") {
		t.Fatalf("want HMAC error, got %v", err)
	}
}

func TestLoad_RejectsLiveStripeWithoutKeys(t *testing.T) {
	yaml := `
db:
  dsn: "user:pass@tcp(x)/y"
security:
  hmac_secret: "this-is-at-least-16-chars"
stripe:
  mode: live
`
	_, err := Load(writeYAML(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "stripe.secret_key") {
		t.Fatalf("want stripe key error, got %v", err)
	}
}

func TestLoad_RejectsUnknownMode(t *testing.T) {
	yaml := `
db:
  dsn: "user:pass@tcp(x)/y"
security:
  hmac_secret: "this-is-at-least-16-chars"
stripe:
  mode: bogus
`
	_, err := Load(writeYAML(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "stripe.mode") {
		t.Fatalf("want mode error, got %v", err)
	}
}

func TestLoad_RejectsMissingDSN(t *testing.T) {
	yaml := `
security:
  hmac_secret: "this-is-at-least-16-chars"
stripe:
  mode: fake
`
	_, err := Load(writeYAML(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "db.dsn") {
		t.Fatalf("want dsn error, got %v", err)
	}
}

func TestLoad_RejectsUnknownField(t *testing.T) {
	yaml := `
db:
  dsn: "user:pass@tcp(x)/y"
  bogus_field: 42
security:
  hmac_secret: "this-is-at-least-16-chars"
stripe:
  mode: fake
`
	_, err := Load(writeYAML(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "bogus_field") {
		t.Fatalf("want unknown-field error, got %v", err)
	}
}

func TestLoad_ParsesDurationsAndOverrides(t *testing.T) {
	yaml := `
app:
  port: 9090
  public_base_url: "https://pay.example.com/"
  checkout_token_ttl: 30m
  logical_shard_count: 32
db:
  dsn: "user:pass@tcp(x)/y"
  max_open_conns: 200
kafka:
  brokers:
    - kafka-1:9092
    - kafka-2:9092
security:
  hmac_secret: "this-is-at-least-16-chars"
  hmac_timestamp_skew: 10m
stripe:
  mode: fake
`
	cfg, err := Load(writeYAML(t, yaml))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.App.Port != 9090 {
		t.Errorf("port: %d", cfg.App.Port)
	}
	if cfg.App.PublicBaseURL != "https://pay.example.com" {
		t.Errorf("trailing slash not trimmed: %s", cfg.App.PublicBaseURL)
	}
	if cfg.App.CheckoutTokenTTL != 30*time.Minute {
		t.Errorf("token ttl: %s", cfg.App.CheckoutTokenTTL)
	}
	if cfg.App.LogicalShardCount != 32 {
		t.Errorf("shard count: %d", cfg.App.LogicalShardCount)
	}
	if cfg.DB.MaxOpenConns != 200 {
		t.Errorf("max open conns: %d", cfg.DB.MaxOpenConns)
	}
	if len(cfg.Kafka.Brokers) != 2 || cfg.Kafka.Brokers[0] != "kafka-1:9092" {
		t.Errorf("brokers: %v", cfg.Kafka.Brokers)
	}
	if cfg.Security.HMACTimestampSkew != 10*time.Minute {
		t.Errorf("skew: %s", cfg.Security.HMACTimestampSkew)
	}
}

func TestLoad_RejectsBadDuration(t *testing.T) {
	yaml := `
app:
  checkout_token_ttl: "not-a-duration"
db:
  dsn: "user:pass@tcp(x)/y"
security:
  hmac_secret: "this-is-at-least-16-chars"
stripe:
  mode: fake
`
	_, err := Load(writeYAML(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "checkout_token_ttl") {
		t.Fatalf("want duration error, got %v", err)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("want read error, got %v", err)
	}
}

func TestLoad_EmptyPath(t *testing.T) {
	_, err := Load("")
	if err == nil {
		t.Fatal("want error for empty path")
	}
}

func TestNonNegUint64(t *testing.T) {
	cases := []struct {
		in   int64
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

func TestShardCount(t *testing.T) {
	cases := []struct {
		in   int
		want uint8
	}{
		{0, 1}, {-5, 1}, {1, 1}, {16, 16}, {255, 255}, {500, 255},
	}
	for _, c := range cases {
		if got := shardCount(c.in); got != c.want {
			t.Errorf("shardCount(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

package logger

import (
	"context"
	"testing"
)

func TestRequestIDRoundtrip(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-1")
	if got := RequestID(ctx); got != "req-1" {
		t.Fatalf("got %q, want req-1", got)
	}
}

func TestRequestIDDefaultEmpty(t *testing.T) {
	if RequestID(context.Background()) != "" {
		t.Fatal("expected empty for fresh context")
	}
}

func TestWithMerchantAndOrder(t *testing.T) {
	ctx := WithMerchantID(WithOrderID(context.Background(), "ord-1"), "M-1")
	// L should not panic and With(ctx) should work.
	_ = L()
	if l := With(ctx); l == nil {
		t.Fatal("With returned nil")
	}
}

func TestInitLevels(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error", "weird"} {
		Init(lvl)
		if L() == nil {
			t.Fatalf("nil logger after Init(%q)", lvl)
		}
	}
}

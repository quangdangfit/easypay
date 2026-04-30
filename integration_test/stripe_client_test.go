//go:build integration

package integration

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	stripego "github.com/stripe/stripe-go/v76"

	"github.com/quangdangfit/easypay/internal/provider/stripe"
)

// withStripeBackend points the stripe-go SDK at the given test server and
// returns a Client + cleanup. Mirrors internal/provider/stripe/impl_test.go's
// helper but uses only exported surface.
func withStripeBackend(t *testing.T, ts *httptest.Server) (stripe.Client, func()) {
	t.Helper()
	prev := stripego.GetBackend(stripego.APIBackend)
	backend := stripego.GetBackendWithConfig(stripego.APIBackend, &stripego.BackendConfig{
		URL:               stripego.String(ts.URL),
		MaxNetworkRetries: stripego.Int64(0),
	})
	stripego.SetBackend(stripego.APIBackend, backend)
	c := stripe.NewClient("sk_test_int", "whsec_test", "2024-06-20")
	return c, func() {
		stripego.SetBackend(stripego.APIBackend, prev)
	}
}

// TestStripeClient_ErrorMatrix exercises the realClient against an httptest
// Stripe stub for each error category we map. We assert ProviderError.Category
// because that's what the handler layer switches on for HTTP mapping.
func TestStripeClient_ErrorMatrix(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantCat    string
		wantMatchN bool // true → wraps net/timeout (not stripe.Error)
	}{
		{
			name: "card_declined → category=card",
			// Stripe declines surface as 402 with type=card_error.
			status:  http.StatusPaymentRequired,
			body:    `{"error":{"type":"card_error","code":"card_declined","message":"Your card was declined."}}`,
			wantCat: "card",
		},
		{
			name:    "invalid_request → category=invalid_request",
			status:  http.StatusBadRequest,
			body:    `{"error":{"type":"invalid_request_error","message":"Missing required param: amount"}}`,
			wantCat: "invalid_request",
		},
		{
			name: "rate_limit → category=rate_limit",
			// 429 is what gets remapped to rate_limit even when type=api_error.
			status:  http.StatusTooManyRequests,
			body:    `{"error":{"type":"api_error","message":"Too many requests"}}`,
			wantCat: "rate_limit",
		},
		{
			name:    "5xx api_error → category=api",
			status:  http.StatusServiceUnavailable,
			body:    `{"error":{"type":"api_error","message":"Stripe is having a moment"}}`,
			wantCat: "api",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer ts.Close()
			c, restore := withStripeBackend(t, ts)
			defer restore()

			_, err := c.CreatePaymentIntent(context.Background(), stripe.CreatePaymentIntentRequest{
				Amount:             1500,
				Currency:           "usd",
				PaymentMethodTypes: []string{"card"},
			}, "idem-err-"+tc.name)

			if err == nil {
				t.Fatalf("expected error")
			}
			var perr *stripe.ProviderError
			if !errors.As(err, &perr) {
				t.Fatalf("expected *stripe.ProviderError, got %T: %v", err, err)
			}
			if perr.Category != tc.wantCat {
				t.Errorf("category: got %q want %q", perr.Category, tc.wantCat)
			}
		})
	}
}

// TestStripeClient_NetworkTimeout verifies that a request which never returns
// is mapped to category=network (so the handler can decide on 504 vs retry).
func TestStripeClient_NetworkTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than the caller's context timeout.
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	c, restore := withStripeBackend(t, ts)
	defer restore()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.CreatePaymentIntent(ctx, stripe.CreatePaymentIntentRequest{
		Amount: 100, Currency: "usd", PaymentMethodTypes: []string{"card"},
	}, "idem-timeout")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var perr *stripe.ProviderError
	if !errors.As(err, &perr) {
		t.Fatalf("expected *stripe.ProviderError, got %T: %v", err, err)
	}
	if perr.Category != "network" {
		t.Errorf("category: got %q want network", perr.Category)
	}
}

// TestStripeClient_MalformedJSON ensures a non-JSON 200 response surfaces as
// a usable error rather than panicking.
func TestStripeClient_MalformedJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id": this is not json`))
	}))
	defer ts.Close()
	c, restore := withStripeBackend(t, ts)
	defer restore()

	_, err := c.CreatePaymentIntent(context.Background(), stripe.CreatePaymentIntentRequest{
		Amount: 100, Currency: "usd", PaymentMethodTypes: []string{"card"},
	}, "idem-malformed")
	if err == nil {
		t.Fatal("expected error from malformed JSON")
	}
}

// TestStripeIdempotencyKey verifies the SDK forwards our Idempotency-Key
// header on state-mutating calls. Stripe's server-side replay is what makes
// the merchant idempotency contract end-to-end safe; if the header doesn't
// reach Stripe, retries can double-charge.
func TestStripeIdempotencyKey(t *testing.T) {
	var calls atomic.Int64
	var lastIdemKey atomic.Value // string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		lastIdemKey.Store(r.Header.Get("Idempotency-Key"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// A minimal valid PaymentIntent body.
		_, _ = w.Write([]byte(`{
			"id": "pi_idem_1",
			"object": "payment_intent",
			"client_secret": "pi_idem_1_secret",
			"amount": 1500,
			"currency": "usd",
			"status": "requires_payment_method"
		}`))
	}))
	defer ts.Close()
	c, restore := withStripeBackend(t, ts)
	defer restore()

	const idem = "merchant-A:txn-001"
	pi1, err := c.CreatePaymentIntent(context.Background(), stripe.CreatePaymentIntentRequest{
		Amount: 1500, Currency: "usd", PaymentMethodTypes: []string{"card"},
	}, idem)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if got, _ := lastIdemKey.Load().(string); got != idem {
		t.Fatalf("idempotency-key header not forwarded: got %q want %q", got, idem)
	}

	pi2, err := c.CreatePaymentIntent(context.Background(), stripe.CreatePaymentIntentRequest{
		Amount: 1500, Currency: "usd", PaymentMethodTypes: []string{"card"},
	}, idem)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	// We rely on the upstream stub (Stripe) to replay the same response — the
	// SDK does no client-side dedup. So both calls hit the server (the stub
	// returns the same body) and the responses match.
	if pi1.ID != pi2.ID || pi1.ClientSecret != pi2.ClientSecret {
		t.Errorf("responses differ across same idempotency key: %#v vs %#v", pi1, pi2)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 round-trips (no client-side dedup), got %d", calls.Load())
	}
}

// TestStripeClient_HappyPath sanity-checks the full request/response decode
// against a representative success body. Belt-and-braces alongside the unit
// tests, since this exercises the same wire path the production handler uses.
func TestStripeClient_HappyPath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "cs_test_happy",
			"object": "checkout.session",
			"url": "https://checkout.stripe.com/c/pay/cs_test_happy",
			"status": "open",
			"amount_total": 2500,
			"currency": "usd",
			"payment_intent": {"id": "pi_happy", "object": "payment_intent", "client_secret": "pi_happy_secret"}
		}`))
	}))
	defer ts.Close()
	c, restore := withStripeBackend(t, ts)
	defer restore()

	out, err := c.CreateCheckoutSession(context.Background(), stripe.CreateCheckoutRequest{
		Amount:             2500,
		Currency:           "usd",
		PaymentMethodTypes: []string{"card"},
		ClientReferenceID:  "ord-happy",
		Metadata:           map[string]string{"order_id": "ord-happy"},
	}, "idem-happy")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.HasPrefix(out.URL, "https://checkout.stripe.com/") || out.PaymentIntentID != "pi_happy" {
		t.Fatalf("unexpected response: %#v", out)
	}
}

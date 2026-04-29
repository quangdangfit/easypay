package stripe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	stripego "github.com/stripe/stripe-go/v76"
)

// withStubBackend points stripe-go at the given test server and returns a
// realClient configured for that backend. Caller MUST defer the cleanup func
// to restore the global backend.
func withStubBackend(t *testing.T, ts *httptest.Server) (*realClient, func()) {
	t.Helper()
	prev := stripego.GetBackend(stripego.APIBackend)
	backend := stripego.GetBackendWithConfig(stripego.APIBackend, &stripego.BackendConfig{
		URL:               stripego.String(ts.URL),
		MaxNetworkRetries: stripego.Int64(0),
	})
	stripego.SetBackend(stripego.APIBackend, backend)
	c := NewClient("sk_test_x", "whsec_test", "2024-06-20").(*realClient)
	return c, func() {
		stripego.SetBackend(stripego.APIBackend, prev)
	}
}

// stubServer returns an httptest server that always responds with the given
// status code and body for any request.
func stubServer(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestRealClient_CreateCheckoutSession(t *testing.T) {
	ts := stubServer(200, `{
		"id": "cs_test_1",
		"object": "checkout.session",
		"url": "https://checkout.stripe.com/c/pay/cs_test_1",
		"status": "open",
		"amount_total": 1500,
		"currency": "usd",
		"payment_intent": {"id": "pi_1", "object": "payment_intent", "client_secret": "pi_1_secret"}
	}`)
	defer ts.Close()
	c, restore := withStubBackend(t, ts)
	defer restore()

	out, err := c.CreateCheckoutSession(context.Background(), CreateCheckoutRequest{
		Amount:             1500,
		Currency:           "usd",
		PaymentMethodTypes: []string{"card"},
		CustomerEmail:      "user@example.com",
		SuccessURL:         "https://m/success",
		CancelURL:          "https://m/cancel",
		ClientReferenceID:  "ORD-1",
		Metadata:           map[string]string{"order_id": "ORD-1"},
	}, "idem-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.ID != "cs_test_1" {
		t.Errorf("ID=%q", out.ID)
	}
	if out.PaymentIntentID != "pi_1" {
		t.Errorf("PI ID=%q", out.PaymentIntentID)
	}
	if out.ClientSecret != "pi_1_secret" {
		t.Errorf("ClientSecret=%q", out.ClientSecret)
	}
	if out.URL == "" {
		t.Error("empty URL")
	}
}

func TestRealClient_CreateCheckoutSession_StripeError(t *testing.T) {
	ts := stubServer(402, `{"error": {"type": "card_error", "code": "card_declined", "message": "Your card was declined."}}`)
	defer ts.Close()
	c, restore := withStubBackend(t, ts)
	defer restore()

	_, err := c.CreateCheckoutSession(context.Background(), CreateCheckoutRequest{Amount: 1500, Currency: "usd"}, "")
	if err == nil {
		t.Fatal("expected error")
	}
	var perr *ProviderError
	if !errors.As(err, &perr) {
		t.Fatalf("not a ProviderError: %v", err)
	}
	if perr.Category != "card" {
		t.Errorf("Category=%q", perr.Category)
	}
}

func TestRealClient_CreatePaymentIntent(t *testing.T) {
	ts := stubServer(200, `{
		"id": "pi_2",
		"object": "payment_intent",
		"client_secret": "pi_2_secret",
		"amount": 2000,
		"currency": "usd",
		"status": "requires_payment_method",
		"metadata": {"order_id": "ORD-2"}
	}`)
	defer ts.Close()
	c, restore := withStubBackend(t, ts)
	defer restore()

	out, err := c.CreatePaymentIntent(context.Background(), CreatePaymentIntentRequest{
		Amount:             2000,
		Currency:           "usd",
		PaymentMethodTypes: []string{"card"},
		CustomerEmail:      "user@example.com",
		Description:        "test order",
		Metadata:           map[string]string{"order_id": "ORD-2"},
	}, "idem-2")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.ID != "pi_2" {
		t.Errorf("ID=%q", out.ID)
	}
	if out.Status != "requires_payment_method" {
		t.Errorf("Status=%q", out.Status)
	}
	if out.Metadata["order_id"] != "ORD-2" {
		t.Errorf("metadata: %v", out.Metadata)
	}
}

func TestRealClient_CreatePaymentIntent_RateLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"too many"}}`))
	}))
	defer ts.Close()
	c, restore := withStubBackend(t, ts)
	defer restore()

	_, err := c.CreatePaymentIntent(context.Background(), CreatePaymentIntentRequest{Amount: 1, Currency: "usd"}, "")
	var perr *ProviderError
	if !errors.As(err, &perr) {
		t.Fatalf("not ProviderError: %v", err)
	}
	if perr.Category != "rate_limit" {
		t.Errorf("Category=%q", perr.Category)
	}
}

func TestRealClient_GetPaymentIntent(t *testing.T) {
	ts := stubServer(200, `{
		"id": "pi_3",
		"object": "payment_intent",
		"amount": 1500,
		"currency": "usd",
		"status": "succeeded",
		"latest_charge": {"id": "ch_3", "object": "charge"}
	}`)
	defer ts.Close()
	c, restore := withStubBackend(t, ts)
	defer restore()

	pi, err := c.GetPaymentIntent(context.Background(), "pi_3")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if pi.ID != "pi_3" {
		t.Errorf("ID=%q", pi.ID)
	}
	if pi.ChargeID != "ch_3" {
		t.Errorf("ChargeID=%q", pi.ChargeID)
	}
}

func TestRealClient_GetPaymentIntent_NotFound(t *testing.T) {
	ts := stubServer(404, `{"error":{"type":"invalid_request_error","message":"No such PI"}}`)
	defer ts.Close()
	c, restore := withStubBackend(t, ts)
	defer restore()

	_, err := c.GetPaymentIntent(context.Background(), "pi_missing")
	var perr *ProviderError
	if !errors.As(err, &perr) {
		t.Fatalf("not ProviderError: %v", err)
	}
	if perr.Category != "invalid_request" {
		t.Errorf("Category=%q", perr.Category)
	}
}

func TestRealClient_GetCheckoutSession(t *testing.T) {
	ts := stubServer(200, `{
		"id": "cs_4",
		"object": "checkout.session",
		"url": "https://checkout/cs_4",
		"status": "complete",
		"amount_total": 1500,
		"currency": "usd",
		"payment_intent": {"id": "pi_4", "object": "payment_intent", "client_secret": "pi_4_secret"}
	}`)
	defer ts.Close()
	c, restore := withStubBackend(t, ts)
	defer restore()

	cs, err := c.GetCheckoutSession(context.Background(), "cs_4")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cs.PaymentIntentID != "pi_4" {
		t.Errorf("PI ID=%q", cs.PaymentIntentID)
	}
	if cs.Status != "complete" {
		t.Errorf("Status=%q", cs.Status)
	}
}

func TestRealClient_CreateRefund(t *testing.T) {
	ts := stubServer(200, `{
		"id": "re_1",
		"object": "refund",
		"amount": 500,
		"currency": "usd",
		"status": "succeeded",
		"payment_intent": {"id": "pi_5", "object": "payment_intent"},
		"charge": {"id": "ch_5", "object": "charge"}
	}`)
	defer ts.Close()
	c, restore := withStubBackend(t, ts)
	defer restore()

	out, err := c.CreateRefund(context.Background(), CreateRefundRequest{
		PaymentIntentID: "pi_5",
		Amount:          500,
		Reason:          "requested_by_customer",
		Metadata:        map[string]string{"order_id": "ORD-5"},
	}, "idem-5")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.ID != "re_1" {
		t.Errorf("ID=%q", out.ID)
	}
	if out.PaymentIntentID != "pi_5" {
		t.Errorf("PI ID=%q", out.PaymentIntentID)
	}
	if out.ChargeID != "ch_5" {
		t.Errorf("ChargeID=%q", out.ChargeID)
	}
}

func TestRealClient_CreateRefund_FullAmount(t *testing.T) {
	// Amount=0 → no Amount param sent. Just verify happy path with no Amount.
	ts := stubServer(200, `{"id":"re_2","object":"refund","amount":1500,"currency":"usd","status":"succeeded"}`)
	defer ts.Close()
	c, restore := withStubBackend(t, ts)
	defer restore()

	_, err := c.CreateRefund(context.Background(), CreateRefundRequest{PaymentIntentID: "pi_x"}, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
}

func TestRealClient_VerifyWebhookSignature_UsesConfiguredSecret(t *testing.T) {
	c := NewClient("sk_test_x", "whsec_default", "").(*realClient)
	payload := []byte(`{"id":"evt_1","type":"payment_intent.succeeded","created":1,"data":{"object":{"id":"pi_x"}}}`)
	ts := time.Now().Unix()
	sig := SignPayload(payload, "whsec_default", ts)

	ev, err := c.VerifyWebhookSignature(payload, sig, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ev.ID != "evt_1" {
		t.Errorf("ID=%q", ev.ID)
	}
}

func TestRealClient_VerifyWebhookSignature_ExplicitSecretOverrides(t *testing.T) {
	c := NewClient("sk_test_x", "whsec_default", "").(*realClient)
	payload := []byte(`{"id":"evt_2","type":"x","created":1,"data":{"object":{}}}`)
	ts := time.Now().Unix()
	sig := SignPayload(payload, "whsec_explicit", ts)

	ev, err := c.VerifyWebhookSignature(payload, sig, "whsec_explicit")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ev.ID != "evt_2" {
		t.Errorf("ID=%q", ev.ID)
	}
}

func TestMapPaymentIntent_Nil(t *testing.T) {
	if got := mapPaymentIntent(nil); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestMapStripeError_NetworkPath(t *testing.T) {
	// Non-stripe error → network category.
	err := mapStripeError("op", errors.New("dial tcp: connection refused"))
	var perr *ProviderError
	if !errors.As(err, &perr) {
		t.Fatalf("not ProviderError")
	}
	if perr.Category != "network" {
		t.Errorf("Category=%q", perr.Category)
	}
	if perr.Op != "op" {
		t.Errorf("Op=%q", perr.Op)
	}
	if !strings.Contains(perr.Error(), "network") {
		t.Errorf("Error()=%q", perr.Error())
	}
}

func TestMapStripeError_IdempotencyAndAPI(t *testing.T) {
	for _, tc := range []struct {
		stripeType stripego.ErrorType
		want       string
	}{
		{stripego.ErrorTypeAPI, "api"},
		{stripego.ErrorTypeIdempotency, "idempotency"},
	} {
		serr := &stripego.Error{Type: tc.stripeType}
		err := mapStripeError("op", serr)
		var perr *ProviderError
		if !errors.As(err, &perr) {
			t.Fatalf("not ProviderError for %s", tc.stripeType)
		}
		if perr.Category != tc.want {
			t.Errorf("Type=%s Category=%q want=%q", tc.stripeType, perr.Category, tc.want)
		}
	}
}

func TestNilIfEmpty(t *testing.T) {
	if nilIfEmpty("") != nil {
		t.Error("empty string should yield nil")
	}
	got := nilIfEmpty("x")
	if got == nil || *got != "x" {
		t.Errorf("got=%v", got)
	}
}

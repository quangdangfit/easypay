package stripe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// fakeClient implements Client without any network calls. Useful for load
// testing the gateway itself without bumping into Stripe's rate limits, and
// for end-to-end tests that don't need a real Stripe account.
//
// Successful calls return synthetic IDs (cs_fake_..., pi_fake_..., re_fake_...).
// Webhook verification still uses the real HMAC implementation so signed
// payloads from local test fixtures continue to work.
type fakeClient struct{}

// NewFake returns a Client that fabricates Stripe responses locally.
func NewFake() Client { return &fakeClient{} }

func (f *fakeClient) CreateCheckoutSession(ctx context.Context, req CreateCheckoutRequest, idempotencyKey string) (*CheckoutSession, error) {
	id := "cs_fake_" + randHex(24)
	pi := "pi_fake_" + randHex(24)
	return &CheckoutSession{
		ID:              id,
		URL:             "https://checkout.fake.local/c/pay/" + id,
		PaymentIntentID: pi,
		ClientSecret:    pi + "_secret_" + randHex(16),
		Status:          "open",
		AmountTotal:     req.Amount,
		Currency:        strings.ToLower(req.Currency),
	}, nil
}

func (f *fakeClient) CreatePaymentIntent(ctx context.Context, req CreatePaymentIntentRequest, idempotencyKey string) (*PaymentIntent, error) {
	pi := "pi_fake_" + randHex(24)
	return &PaymentIntent{
		ID:           pi,
		ClientSecret: pi + "_secret_" + randHex(16),
		Amount:       req.Amount,
		Currency:     strings.ToLower(req.Currency),
		Status:       "requires_payment_method",
		Metadata:     req.Metadata,
	}, nil
}

func (f *fakeClient) GetPaymentIntent(ctx context.Context, id string) (*PaymentIntent, error) {
	return &PaymentIntent{
		ID:       id,
		Status:   "succeeded",
		Currency: "usd",
	}, nil
}

func (f *fakeClient) GetCheckoutSession(ctx context.Context, id string) (*CheckoutSession, error) {
	return &CheckoutSession{
		ID:     id,
		Status: "complete",
	}, nil
}

func (f *fakeClient) CreateRefund(ctx context.Context, req CreateRefundRequest, idempotencyKey string) (*Refund, error) {
	return &Refund{
		ID:              "re_fake_" + randHex(24),
		Amount:          req.Amount,
		Currency:        "usd",
		Status:          "succeeded",
		PaymentIntentID: req.PaymentIntentID,
	}, nil
}

func (f *fakeClient) VerifyWebhookSignature(payload []byte, sigHeader, secret string) (*Event, error) {
	return VerifyAndConstructEvent(payload, sigHeader, secret)
}

func randHex(n int) string {
	b := make([]byte, n/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

package stripe

import (
	"context"
	"errors"
)

// stubClient is a no-op Client used when the real Stripe wiring isn't available
// yet (e.g. early-phase smoke tests). All mutating calls return ErrNotConfigured.
type stubClient struct{}

func NewStub() Client { return &stubClient{} }

var ErrNotConfigured = errors.New("stripe client not configured")

func (s *stubClient) CreateCheckoutSession(ctx context.Context, req CreateCheckoutRequest, idempotencyKey string) (*CheckoutSession, error) {
	return nil, ErrNotConfigured
}

func (s *stubClient) CreatePaymentIntent(ctx context.Context, req CreatePaymentIntentRequest, idempotencyKey string) (*PaymentIntent, error) {
	return nil, ErrNotConfigured
}

func (s *stubClient) GetPaymentIntent(ctx context.Context, id string) (*PaymentIntent, error) {
	return nil, ErrNotConfigured
}

func (s *stubClient) GetCheckoutSession(ctx context.Context, id string) (*CheckoutSession, error) {
	return nil, ErrNotConfigured
}

func (s *stubClient) CreateRefund(ctx context.Context, req CreateRefundRequest, idempotencyKey string) (*Refund, error) {
	return nil, ErrNotConfigured
}

func (s *stubClient) VerifyWebhookSignature(payload []byte, sigHeader, secret string) (*Event, error) {
	return nil, ErrNotConfigured
}

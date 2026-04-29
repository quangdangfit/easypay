package stripe

import "context"

// Client is the Stripe-facing port consumed by the service layer.
// Real implementation lives alongside this file (impl.go) using stripe-go;
// tests inject a fake satisfying this interface.
type Client interface {
	CreateCheckoutSession(ctx context.Context, req CreateCheckoutRequest, idempotencyKey string) (*CheckoutSession, error)
	CreatePaymentIntent(ctx context.Context, req CreatePaymentIntentRequest, idempotencyKey string) (*PaymentIntent, error)
	GetPaymentIntent(ctx context.Context, id string) (*PaymentIntent, error)
	GetCheckoutSession(ctx context.Context, id string) (*CheckoutSession, error)
	CreateRefund(ctx context.Context, req CreateRefundRequest, idempotencyKey string) (*Refund, error)
	VerifyWebhookSignature(payload []byte, sigHeader, secret string) (*Event, error)
}

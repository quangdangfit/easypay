package service

import "context"

// Payments is the port consumed by the merchant-facing payment handler.
// It hides PaymentService internals (cache, stripe, kafka publisher) behind
// a thin orchestration surface.
type Payments interface {
	Create(ctx context.Context, in CreatePaymentInput) (*CreatePaymentResult, error)
}

// Webhooks is the port consumed by the Stripe-callback handler and the
// merchant-facing refund handler.
type Webhooks interface {
	Process(ctx context.Context, payload []byte, sigHeader string) error
	CreateRefund(ctx context.Context, in RefundInput) (*RefundResult, error)
}

// Checkouts is the port consumed by the public hosted-checkout handler.
// It returns a redirect URL for an order, lazily creating the upstream
// session on first use.
type Checkouts interface {
	Resolve(ctx context.Context, orderID string) (string, error)
}

// Reconciler runs as a background loop and recovers stuck orders.
type Reconciler interface {
	Run(ctx context.Context) error
}

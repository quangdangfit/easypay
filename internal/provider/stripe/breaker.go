package stripe

import (
	"context"
	"errors"
	"time"

	"github.com/sony/gobreaker"
)

// breakerClient wraps a Client with a circuit breaker so that a failing
// Stripe doesn't drag down the rest of the gateway.
//
// Trip rule: 50%+ failure rate over a rolling 10s window with at least 20
// samples. While open, calls return ErrCircuitOpen immediately for 30s, then
// half-open allows up to 100 probe requests.
type breakerClient struct {
	inner Client
	cb    *gobreaker.CircuitBreaker
}

var ErrCircuitOpen = errors.New("stripe circuit breaker open")

func NewBreakerClient(inner Client, name string) Client {
	cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        name,
		MaxRequests: 100,
		Interval:    10 * time.Second,
		Timeout:     30 * time.Second,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.Requests >= 20 && float64(c.TotalFailures)/float64(c.Requests) > 0.5
		},
		IsSuccessful: func(err error) bool {
			// Card declines and invalid-request errors aren't infrastructure
			// failures — they shouldn't trip the breaker.
			if err == nil {
				return true
			}
			var pe *ProviderError
			if errors.As(err, &pe) {
				switch pe.Category {
				case "card", "invalid_request":
					return true
				}
			}
			return false
		},
	})
	return &breakerClient{inner: inner, cb: cb}
}

func (b *breakerClient) wrap(fn func() (any, error)) (any, error) {
	v, err := b.cb.Execute(fn)
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		return nil, ErrCircuitOpen
	}
	return v, err
}

func (b *breakerClient) CreateCheckoutSession(ctx context.Context, req CreateCheckoutRequest, idem string) (*CheckoutSession, error) {
	v, err := b.wrap(func() (any, error) { return b.inner.CreateCheckoutSession(ctx, req, idem) })
	if err != nil {
		return nil, err
	}
	return v.(*CheckoutSession), nil
}

func (b *breakerClient) CreatePaymentIntent(ctx context.Context, req CreatePaymentIntentRequest, idem string) (*PaymentIntent, error) {
	v, err := b.wrap(func() (any, error) { return b.inner.CreatePaymentIntent(ctx, req, idem) })
	if err != nil {
		return nil, err
	}
	return v.(*PaymentIntent), nil
}

func (b *breakerClient) GetPaymentIntent(ctx context.Context, id string) (*PaymentIntent, error) {
	v, err := b.wrap(func() (any, error) { return b.inner.GetPaymentIntent(ctx, id) })
	if err != nil {
		return nil, err
	}
	return v.(*PaymentIntent), nil
}

func (b *breakerClient) GetCheckoutSession(ctx context.Context, id string) (*CheckoutSession, error) {
	v, err := b.wrap(func() (any, error) { return b.inner.GetCheckoutSession(ctx, id) })
	if err != nil {
		return nil, err
	}
	return v.(*CheckoutSession), nil
}

func (b *breakerClient) CreateRefund(ctx context.Context, req CreateRefundRequest, idem string) (*Refund, error) {
	v, err := b.wrap(func() (any, error) { return b.inner.CreateRefund(ctx, req, idem) })
	if err != nil {
		return nil, err
	}
	return v.(*Refund), nil
}

func (b *breakerClient) VerifyWebhookSignature(payload []byte, sigHeader, secret string) (*Event, error) {
	// Verification is pure CPU; no need to wrap.
	return b.inner.VerifyWebhookSignature(payload, sigHeader, secret)
}

// State returns the current breaker state ("closed", "half-open", "open"),
// useful for /metrics or /readyz dashboards.
func (b *breakerClient) State() string {
	return b.cb.State().String()
}

package stripe

import (
	"context"
	"errors"
	"testing"
)

// failClient toggles between success/error to drive the breaker.
type failClient struct {
	err   error
	calls int
}

func (f *failClient) CreateCheckoutSession(ctx context.Context, req CreateCheckoutRequest, idem string) (*CheckoutSession, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &CheckoutSession{ID: "cs_x"}, nil
}
func (f *failClient) CreatePaymentIntent(ctx context.Context, req CreatePaymentIntentRequest, idem string) (*PaymentIntent, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &PaymentIntent{ID: "pi_x"}, nil
}
func (f *failClient) GetPaymentIntent(ctx context.Context, id string) (*PaymentIntent, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &PaymentIntent{ID: id}, nil
}
func (f *failClient) GetCheckoutSession(ctx context.Context, id string) (*CheckoutSession, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &CheckoutSession{ID: id}, nil
}
func (f *failClient) CreateRefund(ctx context.Context, req CreateRefundRequest, idem string) (*Refund, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &Refund{ID: "re_x"}, nil
}
func (f *failClient) VerifyWebhookSignature(payload []byte, sigHeader, secret string) (*Event, error) {
	return nil, errors.New("verify not used")
}

func TestBreaker_PassesThroughOnSuccess(t *testing.T) {
	inner := &failClient{}
	c := NewBreakerClient(inner, "test")
	for i := 0; i < 5; i++ {
		if _, err := c.CreateCheckoutSession(context.Background(), CreateCheckoutRequest{}, "idem"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if inner.calls != 5 {
		t.Fatalf("inner calls: %d", inner.calls)
	}
}

func TestBreaker_TripsAfterFailures(t *testing.T) {
	netErr := &ProviderError{Op: "x", Category: "network", Err: errors.New("conn refused")}
	inner := &failClient{err: netErr}
	c := NewBreakerClient(inner, "test")

	// Drive 25 failures (>= 20 threshold, 100% failure rate triggers trip).
	for i := 0; i < 25; i++ {
		_, _ = c.CreateCheckoutSession(context.Background(), CreateCheckoutRequest{}, "idem")
	}

	// Once the breaker is open, subsequent calls should not reach inner.
	prevCalls := inner.calls
	for i := 0; i < 10; i++ {
		_, err := c.CreateCheckoutSession(context.Background(), CreateCheckoutRequest{}, "idem")
		if !errors.Is(err, ErrCircuitOpen) {
			continue
		}
	}
	if inner.calls != prevCalls {
		// Some calls may still slip through during HalfOpen probes — but
		// definitely not all 10.
		if inner.calls-prevCalls >= 10 {
			t.Fatalf("breaker didn't gate calls (delta %d)", inner.calls-prevCalls)
		}
	}
}

func TestBreaker_AllMethodsPassThroughOnSuccess(t *testing.T) {
	inner := &failClient{}
	c := NewBreakerClient(inner, "passthrough")
	ctx := context.Background()
	if _, err := c.CreatePaymentIntent(ctx, CreatePaymentIntentRequest{}, "i"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetPaymentIntent(ctx, "pi_x"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetCheckoutSession(ctx, "cs_x"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateRefund(ctx, CreateRefundRequest{}, "i"); err != nil {
		t.Fatal(err)
	}
	_, _ = c.VerifyWebhookSignature(nil, "", "")
}

func TestBreaker_AllMethodsReturnOpenWhenTripped(t *testing.T) {
	inner := &failClient{err: &ProviderError{Op: "x", Category: "network", Err: errors.New("conn")}}
	c := NewBreakerClient(inner, "tripped")
	for i := 0; i < 30; i++ {
		_, _ = c.CreateCheckoutSession(context.Background(), CreateCheckoutRequest{}, "i")
	}
	if _, err := c.CreatePaymentIntent(context.Background(), CreatePaymentIntentRequest{}, "i"); !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("CreatePaymentIntent: %v", err)
	}
	if _, err := c.GetPaymentIntent(context.Background(), "pi"); !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("GetPaymentIntent: %v", err)
	}
	if _, err := c.GetCheckoutSession(context.Background(), "cs"); !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("GetCheckoutSession: %v", err)
	}
	if _, err := c.CreateRefund(context.Background(), CreateRefundRequest{}, "i"); !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("CreateRefund: %v", err)
	}
}

func TestBreaker_CardErrorsDoNotTrip(t *testing.T) {
	cardErr := &ProviderError{Op: "x", Category: "card", Err: errors.New("declined")}
	inner := &failClient{err: cardErr}
	c := NewBreakerClient(inner, "test")
	for i := 0; i < 30; i++ {
		_, _ = c.CreateCheckoutSession(context.Background(), CreateCheckoutRequest{}, "idem")
	}
	// Card declines marked successful by IsSuccessful — breaker stays closed.
	// Make a "fresh" call: should still hit inner.
	_, _ = c.CreateCheckoutSession(context.Background(), CreateCheckoutRequest{}, "idem")
	if inner.calls != 31 {
		t.Fatalf("breaker tripped on card errors; calls=%d", inner.calls)
	}
}

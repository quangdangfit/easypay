//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"

	"github.com/quangdangfit/easypay/internal/provider/stripe"
)

// MockStripe is an in-process implementation of stripe.Client for integration
// tests. It records calls so tests can assert on them, supports forced errors,
// and replays the same response for a given idempotency key.
type MockStripe struct {
	mu sync.Mutex

	// configured behaviour
	NextCheckoutErr error
	NextPIErr       error

	// recorded calls
	IdempotencyKeys    []string
	CheckoutCount      int
	PaymentIntentCount int
	RefundCount        int

	// idempotent replay store
	checkoutByKey map[string]*stripe.CheckoutSession
	piByKey       map[string]*stripe.PaymentIntent
}

func NewMockStripe() *MockStripe {
	return &MockStripe{
		checkoutByKey: map[string]*stripe.CheckoutSession{},
		piByKey:       map[string]*stripe.PaymentIntent{},
	}
}

func (m *MockStripe) CreateCheckoutSession(ctx context.Context, req stripe.CreateCheckoutRequest, idem string) (*stripe.CheckoutSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.IdempotencyKeys = append(m.IdempotencyKeys, idem)
	if m.NextCheckoutErr != nil {
		err := m.NextCheckoutErr
		m.NextCheckoutErr = nil
		return nil, err
	}
	if cached, ok := m.checkoutByKey[idem]; ok {
		return cached, nil
	}
	m.CheckoutCount++
	out := &stripe.CheckoutSession{
		ID:              "cs_test_" + idem,
		URL:             "https://mock-checkout/" + idem,
		PaymentIntentID: "pi_test_" + idem,
		ClientSecret:    "pi_test_" + idem + "_secret_xyz",
		Status:          "open",
		AmountTotal:     req.Amount,
		Currency:        req.Currency,
	}
	m.checkoutByKey[idem] = out
	return out, nil
}

func (m *MockStripe) CreatePaymentIntent(ctx context.Context, req stripe.CreatePaymentIntentRequest, idem string) (*stripe.PaymentIntent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.IdempotencyKeys = append(m.IdempotencyKeys, idem)
	if m.NextPIErr != nil {
		err := m.NextPIErr
		m.NextPIErr = nil
		return nil, err
	}
	if cached, ok := m.piByKey[idem]; ok {
		return cached, nil
	}
	m.PaymentIntentCount++
	out := &stripe.PaymentIntent{
		ID:           "pi_test_" + idem,
		ClientSecret: "pi_test_" + idem + "_secret",
		Amount:       req.Amount,
		Currency:     req.Currency,
		Status:       "requires_payment_method",
		Metadata:     req.Metadata,
	}
	m.piByKey[idem] = out
	return out, nil
}

func (m *MockStripe) GetPaymentIntent(ctx context.Context, id string) (*stripe.PaymentIntent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pi := range m.piByKey {
		if pi.ID == id {
			pi.Status = "succeeded"
			return pi, nil
		}
	}
	return &stripe.PaymentIntent{ID: id, Status: "succeeded", Amount: 1500, Currency: "usd"}, nil
}

func (m *MockStripe) GetCheckoutSession(ctx context.Context, id string) (*stripe.CheckoutSession, error) {
	for _, c := range m.checkoutByKey {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, errors.New("not found")
}

func (m *MockStripe) CreateRefund(ctx context.Context, req stripe.CreateRefundRequest, idem string) (*stripe.Refund, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RefundCount++
	return &stripe.Refund{
		ID:              "re_test_" + idem,
		Amount:          req.Amount,
		Currency:        "usd",
		Status:          "succeeded",
		PaymentIntentID: req.PaymentIntentID,
	}, nil
}

func (m *MockStripe) VerifyWebhookSignature(payload []byte, sigHeader, secret string) (*stripe.Event, error) {
	return stripe.VerifyAndConstructEvent(payload, sigHeader, secret)
}

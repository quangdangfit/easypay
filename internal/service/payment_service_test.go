package service

import (
	"context"
	"testing"
	"time"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
)

type fakeIdem struct {
	store map[string][]byte
}

func (f *fakeIdem) Check(ctx context.Context, key string) (bool, []byte, error) {
	if f.store == nil {
		return false, nil, nil
	}
	v, ok := f.store[key]
	return ok, v, nil
}

func (f *fakeIdem) Set(ctx context.Context, key string, response []byte, ttl time.Duration) error {
	if f.store == nil {
		f.store = map[string][]byte{}
	}
	f.store[key] = response
	return nil
}

type fakeStripe struct {
	calls int
}

func (f *fakeStripe) CreateCheckoutSession(ctx context.Context, req stripe.CreateCheckoutRequest, idem string) (*stripe.CheckoutSession, error) {
	f.calls++
	return &stripe.CheckoutSession{
		ID:              "cs_test_123",
		URL:             "https://checkout.stripe.com/cs_test_123",
		PaymentIntentID: "pi_test_123",
		ClientSecret:    "pi_test_123_secret_xyz",
	}, nil
}
func (f *fakeStripe) CreatePaymentIntent(ctx context.Context, req stripe.CreatePaymentIntentRequest, idem string) (*stripe.PaymentIntent, error) {
	return nil, nil
}
func (f *fakeStripe) GetPaymentIntent(ctx context.Context, id string) (*stripe.PaymentIntent, error) {
	return nil, nil
}
func (f *fakeStripe) GetCheckoutSession(ctx context.Context, id string) (*stripe.CheckoutSession, error) {
	return nil, nil
}
func (f *fakeStripe) CreateRefund(ctx context.Context, req stripe.CreateRefundRequest, idem string) (*stripe.Refund, error) {
	return nil, nil
}
func (f *fakeStripe) VerifyWebhookSignature(payload []byte, sigHeader, secret string) (*stripe.Event, error) {
	return nil, nil
}

type fakePublisher struct {
	events    []kafka.PaymentEvent
	confirmed []kafka.PaymentConfirmedEvent
}

func (f *fakePublisher) PublishPaymentEvent(ctx context.Context, e kafka.PaymentEvent) error {
	f.events = append(f.events, e)
	return nil
}
func (f *fakePublisher) PublishPaymentConfirmed(ctx context.Context, e kafka.PaymentConfirmedEvent) error {
	f.confirmed = append(f.confirmed, e)
	return nil
}
func (f *fakePublisher) Close() error { return nil }

func newSvc() (*PaymentService, *fakeStripe, *fakePublisher, *fakeIdem) {
	idem := &fakeIdem{}
	stripeC := &fakeStripe{}
	pub := &fakePublisher{}
	svc := NewPaymentService(idem, stripeC, pub, "USD", "0xCONTRACT", 11155111)
	return svc, stripeC, pub, idem
}

func TestCreate_HappyPath(t *testing.T) {
	svc, stripeC, pub, _ := newSvc()
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}
	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, TransactionID: "TXN-1", Amount: 1500, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.OrderID == "" || res.CheckoutURL == "" {
		t.Fatalf("missing fields in result: %+v", res)
	}
	if stripeC.calls != 1 {
		t.Fatalf("stripe should be called once, got %d", stripeC.calls)
	}
	if len(pub.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(pub.events))
	}
}

func TestCreate_IdempotentDuplicate(t *testing.T) {
	svc, stripeC, pub, _ := newSvc()
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}
	in := CreatePaymentInput{Merchant: merchant, TransactionID: "TXN-DUP", Amount: 1000, Currency: "USD"}

	first, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if first.OrderID != second.OrderID {
		t.Fatalf("idempotent calls returned different orders: %s vs %s", first.OrderID, second.OrderID)
	}
	if stripeC.calls != 1 {
		t.Fatalf("stripe should be called only once across duplicates, got %d", stripeC.calls)
	}
	if len(pub.events) != 1 {
		t.Fatalf("expected 1 event for duplicate, got %d", len(pub.events))
	}
}

func TestCreate_CryptoPath(t *testing.T) {
	svc, stripeC, _, _ := newSvc()
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}
	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, TransactionID: "TXN-CRYPTO", Amount: 5000, Currency: "USD", Method: "crypto",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.CryptoPayload == nil {
		t.Fatalf("expected crypto payload")
	}
	if stripeC.calls != 0 {
		t.Fatalf("stripe must not be called on crypto path")
	}
}

func TestCreate_RejectsInvalid(t *testing.T) {
	svc, _, _, _ := newSvc()
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}
	cases := []CreatePaymentInput{
		{Merchant: merchant, TransactionID: "", Amount: 1},
		{Merchant: merchant, TransactionID: "T", Amount: 0},
		{Merchant: nil, TransactionID: "T", Amount: 1},
	}
	for i, c := range cases {
		if _, err := svc.Create(context.Background(), c); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
}

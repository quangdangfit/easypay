package service

import (
	"context"
	"testing"

	"github.com/quangdangfit/easypay/internal/domain"
)

func newSvc(t *testing.T) (Payments, *stripeStub, *eventCapture, *idemStore) {
	t.Helper()
	idem := newIdemStore(t)
	stripeC := newStripeStub(t)
	pub := newEventCapture(t)
	svc := NewPaymentService(idem.mock, stripeC.mock, pub.mock, nil, PaymentServiceOptions{
		DefaultCurrency: "USD",
		CryptoContract:  "0xCONTRACT",
		CryptoChainID:   11155111,
		// LazyCheckout off → eager Stripe path (existing tests assume this)
	})
	return svc, stripeC, pub, idem
}

func TestCreate_HappyPath(t *testing.T) {
	svc, stripeC, pub, _ := newSvc(t)
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
	if stripeC.createCalls != 1 {
		t.Fatalf("stripe should be called once, got %d", stripeC.createCalls)
	}
	if len(pub.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(pub.events))
	}
}

func TestCreate_IdempotentDuplicate(t *testing.T) {
	svc, stripeC, pub, _ := newSvc(t)
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
	if stripeC.createCalls != 1 {
		t.Fatalf("stripe should be called only once across duplicates, got %d", stripeC.createCalls)
	}
	if len(pub.events) != 1 {
		t.Fatalf("expected 1 event for duplicate, got %d", len(pub.events))
	}
}

func TestCreate_CryptoPath(t *testing.T) {
	svc, stripeC, _, _ := newSvc(t)
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
	if stripeC.createCalls != 0 {
		t.Fatalf("stripe must not be called on crypto path")
	}
}

func TestCreate_LazyCheckoutDoesNotHitStripe(t *testing.T) {
	idem := newIdemStore(t)
	stripeC := newStripeStub(t)
	pub := newEventCapture(t)
	pending := newPendingStore(t)
	svc := NewPaymentService(idem.mock, stripeC.mock, pub.mock, pending.mock, PaymentServiceOptions{
		DefaultCurrency: "USD",
		LazyCheckout:    true,
		PublicBaseURL:   "https://pay.example",
		CheckoutSecret:  "sec",
	})
	merchant := &domain.Merchant{MerchantID: "M1"}
	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, TransactionID: "TXN-LAZY", Amount: 1500, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if stripeC.createCalls != 0 {
		t.Fatalf("expected 0 stripe calls, got %d", stripeC.createCalls)
	}
	if !contains(res.CheckoutURL, "https://pay.example/pay/") {
		t.Fatalf("checkout url: %q", res.CheckoutURL)
	}
	if !contains(res.CheckoutURL, "?t=") {
		t.Fatalf("expected token in url: %q", res.CheckoutURL)
	}
	if pending.store[res.OrderID] == nil {
		t.Fatal("expected pending order snapshot")
	}
}

func TestCreate_LazyWithoutSecretSkipsToken(t *testing.T) {
	idem := newIdemStore(t)
	stripeC := newStripeStub(t)
	pub := newEventCapture(t)
	pending := newPendingStore(t)
	svc := NewPaymentService(idem.mock, stripeC.mock, pub.mock, pending.mock, PaymentServiceOptions{
		LazyCheckout:  true,
		PublicBaseURL: "https://pay.example",
	})
	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, TransactionID: "TXN-1", Amount: 1, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if contains(res.CheckoutURL, "?t=") {
		t.Fatalf("expected no token, got %q", res.CheckoutURL)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestCreate_RejectsInvalid(t *testing.T) {
	svc, _, _, _ := newSvc(t)
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

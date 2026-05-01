package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/quangdangfit/easypay/internal/domain"
)

const testTxnSecret = "unit-test-transaction-id-secret"

func newSvc(t *testing.T) (Payments, *stripeStub, *orderStore) {
	t.Helper()
	stripeC := newStripeStub(t)
	store := newOrderStore(t)
	svc := NewPaymentService(stripeC.mock, store.mock, PaymentServiceOptions{
		DefaultCurrency:     "USD",
		CryptoContract:      "0xCONTRACT",
		CryptoChainID:       11155111,
		TransactionIDSecret: testTxnSecret,
		// LazyCheckout off → eager Stripe path.
	})
	return svc, stripeC, store
}

func TestCreate_HappyPath(t *testing.T) {
	svc, stripeC, store := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}
	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, MerchantOrderID: "ORDER-1", Amount: 1500, Currency: "USD",
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
	if store.updateCheckouts != 1 {
		t.Fatalf("expected one UpdateCheckout, got %d", store.updateCheckouts)
	}
	// Row must be persisted before we returned.
	if _, ok := store.byID[res.OrderID]; !ok {
		t.Fatal("expected row in store after Create")
	}
}

func TestCreate_IdempotentDuplicate(t *testing.T) {
	svc, stripeC, _ := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}
	in := CreatePaymentInput{Merchant: merchant, MerchantOrderID: "ORDER-DUP", Amount: 1000, Currency: "USD"}

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
	if first.TransactionID != second.TransactionID {
		t.Fatalf("transaction_id should be stable across retries")
	}
	if stripeC.createCalls != 1 {
		t.Fatalf("stripe should be called only once across duplicates, got %d", stripeC.createCalls)
	}
}

func TestCreate_CryptoPath(t *testing.T) {
	svc, stripeC, _ := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}
	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, MerchantOrderID: "ORDER-CRYPTO", Amount: 5000, Currency: "USD", Method: "crypto",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.CryptoPayload == nil {
		t.Fatalf("expected crypto payload")
	}
	if res.CryptoPayload.OrderID != res.OrderID {
		t.Fatalf("crypto order_id mismatch: %s vs %s", res.CryptoPayload.OrderID, res.OrderID)
	}
	if stripeC.createCalls != 0 {
		t.Fatalf("stripe must not be called on crypto path")
	}
}

func TestCreate_LazyCheckoutDoesNotHitStripe(t *testing.T) {
	stripeC := newStripeStub(t)
	store := newOrderStore(t)
	svc := NewPaymentService(stripeC.mock, store.mock, PaymentServiceOptions{
		DefaultCurrency:     "USD",
		LazyCheckout:        true,
		PublicBaseURL:       "https://pay.example",
		CheckoutSecret:      "sec",
		TransactionIDSecret: testTxnSecret,
	})
	merchant := &domain.Merchant{MerchantID: "M1"}
	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, MerchantOrderID: "ORDER-LAZY", Amount: 1500, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if stripeC.createCalls != 0 {
		t.Fatalf("expected 0 stripe calls, got %d", stripeC.createCalls)
	}
	if !strings.HasPrefix(res.CheckoutURL, "https://pay.example/pay/") {
		t.Fatalf("checkout url: %q", res.CheckoutURL)
	}
	if !strings.Contains(res.CheckoutURL, "?t=") {
		t.Fatalf("expected token in url: %q", res.CheckoutURL)
	}
	if _, ok := store.byID[res.OrderID]; !ok {
		t.Fatal("expected order persisted in lazy mode")
	}
}

func TestCreate_LazyWithoutSecretSkipsToken(t *testing.T) {
	stripeC := newStripeStub(t)
	store := newOrderStore(t)
	svc := NewPaymentService(stripeC.mock, store.mock, PaymentServiceOptions{
		DefaultCurrency:     "USD",
		LazyCheckout:        true,
		PublicBaseURL:       "https://pay.example",
		TransactionIDSecret: testTxnSecret,
	})
	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, MerchantOrderID: "ORDER-1", Amount: 1, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strings.Contains(res.CheckoutURL, "?t=") {
		t.Fatalf("expected no token, got %q", res.CheckoutURL)
	}
}

func TestCreate_DeterministicIDs(t *testing.T) {
	svc1, _, _ := newSvc(t)
	svc2, _, _ := newSvc(t) // separate svc, separate store
	merchant := &domain.Merchant{MerchantID: "M1"}
	in := CreatePaymentInput{Merchant: merchant, MerchantOrderID: "STABLE", Amount: 1500, Currency: "USD"}

	a, err := svc1.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	b, err := svc2.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	if a.OrderID != b.OrderID {
		t.Fatalf("order_id should be stable for same input across services: %s vs %s", a.OrderID, b.OrderID)
	}
	if a.TransactionID != b.TransactionID {
		t.Fatalf("transaction_id should be stable: %s vs %s", a.TransactionID, b.TransactionID)
	}
	if err := domain.ValidateOrderID(a.OrderID); err != nil {
		t.Fatalf("derived order_id invalid: %v", err)
	}
}

func TestCreate_DerivedIDs_DifferentInputs(t *testing.T) {
	svc, _, _ := newSvc(t)
	a, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, MerchantOrderID: "A", Amount: 1, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	b, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, MerchantOrderID: "B", Amount: 1, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	c, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: &domain.Merchant{MerchantID: "M2"}, MerchantOrderID: "A", Amount: 1, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("c: %v", err)
	}
	if a.OrderID == b.OrderID || a.OrderID == c.OrderID || b.OrderID == c.OrderID {
		t.Fatalf("collision: a=%s b=%s c=%s", a.OrderID, b.OrderID, c.OrderID)
	}
}

// TestCreate_ConflictOnAmountMismatch ensures merchants cannot reuse a
// merchant_order_id with different material payment data — that would
// silently override or duplicate state otherwise.
func TestCreate_ConflictOnAmountMismatch(t *testing.T) {
	svc, _, _ := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1"}
	in := CreatePaymentInput{
		Merchant: merchant, MerchantOrderID: "CONFLICT", Amount: 1500, Currency: "USD",
	}
	if _, err := svc.Create(context.Background(), in); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Same key, different amount.
	in.Amount = 9999
	_, err := svc.Create(context.Background(), in)
	if !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("expected ErrTransactionConflict, got %v", err)
	}
}

func TestCreate_ConflictOnCurrencyMismatch(t *testing.T) {
	svc, _, _ := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1"}
	in := CreatePaymentInput{
		Merchant: merchant, MerchantOrderID: "CCY", Amount: 1500, Currency: "USD",
	}
	if _, err := svc.Create(context.Background(), in); err != nil {
		t.Fatalf("first: %v", err)
	}
	in.Currency = "EUR"
	_, err := svc.Create(context.Background(), in)
	if !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("expected ErrTransactionConflict, got %v", err)
	}
}

func TestCreate_ConflictOnMethodSwitch(t *testing.T) {
	svc, _, _ := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1"}
	in := CreatePaymentInput{
		Merchant: merchant, MerchantOrderID: "METHOD", Amount: 1000, Currency: "USD", Method: "crypto",
	}
	if _, err := svc.Create(context.Background(), in); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Same key, but now Stripe path (method omitted).
	in.Method = ""
	_, err := svc.Create(context.Background(), in)
	if !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("expected ErrTransactionConflict on method switch, got %v", err)
	}
}

// TestCreate_ConcurrentCollapsesToSingleRow drives 1k goroutines through
// Create with the same MerchantOrderID. The MySQL UNIQUE on (merchant_id,
// transaction_id) — modelled by the in-memory store here — must collapse
// every duplicate onto a single row, and every caller must observe the
// same response.
func TestCreate_ConcurrentCollapsesToSingleRow(t *testing.T) {
	svc, stripeC, store := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1"}
	in := CreatePaymentInput{Merchant: merchant, MerchantOrderID: "BURST", Amount: 2000, Currency: "USD"}

	const N = 100
	results := make([]*CreatePaymentResult, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.Create(context.Background(), in)
		}(i)
	}
	wg.Wait()

	// At most one row in the store.
	store.mu.Lock()
	rowCount := len(store.byTxn)
	store.mu.Unlock()
	if rowCount != 1 {
		t.Fatalf("expected exactly one persisted row, got %d", rowCount)
	}
	// Every caller saw the same order_id; no errors.
	first := results[0]
	if first == nil {
		t.Fatalf("first result nil; err=%v", errs[0])
	}
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("call %d errored: %v", i, errs[i])
		}
		if r.OrderID != first.OrderID {
			t.Fatalf("call %d order_id %s != %s", i, r.OrderID, first.OrderID)
		}
	}
	// Stripe is called by the winner only. Losers reconstruct from the
	// persisted row.
	if stripeC.createCalls != 1 {
		t.Fatalf("expected exactly one Stripe call, got %d", stripeC.createCalls)
	}
}

func TestCreate_RejectsInvalid(t *testing.T) {
	svc, _, _ := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}
	cases := []CreatePaymentInput{
		{Merchant: merchant, MerchantOrderID: "", Amount: 1},
		{Merchant: merchant, MerchantOrderID: "T", Amount: 0},
		{Merchant: nil, MerchantOrderID: "T", Amount: 1},
	}
	for i, c := range cases {
		if _, err := svc.Create(context.Background(), c); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
}

func TestCreate_MissingTransactionSecretFails(t *testing.T) {
	stripeC := newStripeStub(t)
	store := newOrderStore(t)
	svc := NewPaymentService(stripeC.mock, store.mock, PaymentServiceOptions{
		DefaultCurrency: "USD",
		// no TransactionIDSecret
	})
	_, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, MerchantOrderID: "X", Amount: 1, Currency: "USD",
	})
	if err == nil {
		t.Fatal("expected error when TransactionIDSecret missing")
	}
}

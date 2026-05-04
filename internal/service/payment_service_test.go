package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/quangdangfit/easypay/internal/domain"
)

func newSvc(t *testing.T) (Payments, *stripeStub, *txStore) {
	t.Helper()
	stripeC := newStripeStub(t)
	store := newTxStore(t)
	svc := NewPaymentService(stripeC.mock, store.mock, PaymentServiceOptions{
		DefaultCurrency: "USD",
		CryptoContract:  "0xCONTRACT",
		CryptoChainID:   11155111,
		// LazyCheckout off → eager Stripe path.
	})
	return svc, stripeC, store
}

func TestCreate_HappyPath(t *testing.T) {
	svc, stripeC, store := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}
	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, OrderID: "ORDER-1", Amount: 1500, Currency: "USD",
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
	if _, ok := store.byID[merchant.MerchantID+":"+res.OrderID]; !ok {
		t.Fatal("expected row in store after Create")
	}
}

func TestCreate_IdempotentDuplicate(t *testing.T) {
	svc, stripeC, _ := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}
	in := CreatePaymentInput{Merchant: merchant, OrderID: "ORDER-DUP", Amount: 1000, Currency: "USD"}

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
		Merchant: merchant, OrderID: "ORDER-CRYPTO", Amount: 5000, Currency: "USD", Method: "crypto",
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
	store := newTxStore(t)
	svc := NewPaymentService(stripeC.mock, store.mock, PaymentServiceOptions{
		DefaultCurrency: "USD",
		LazyCheckout:    true,
		PublicBaseURL:   "https://pay.example",
		CheckoutSecret:  "sec",
	})
	merchant := &domain.Merchant{MerchantID: "M1"}
	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, OrderID: "ORDER-LAZY", Amount: 1500, Currency: "USD",
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
	if _, ok := store.byID[merchant.MerchantID+":"+res.OrderID]; !ok {
		t.Fatal("expected order persisted in lazy mode")
	}
}

func TestCreate_LazyWithoutSecretSkipsToken(t *testing.T) {
	stripeC := newStripeStub(t)
	store := newTxStore(t)
	svc := NewPaymentService(stripeC.mock, store.mock, PaymentServiceOptions{
		DefaultCurrency: "USD",
		LazyCheckout:    true,
		PublicBaseURL:   "https://pay.example",
	})
	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, OrderID: "ORDER-1", Amount: 1, Currency: "USD",
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
	in := CreatePaymentInput{Merchant: merchant, OrderID: "STABLE", Amount: 1500, Currency: "USD"}

	a, err := svc1.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	b, err := svc2.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	if a.OrderID != b.OrderID {
		t.Fatalf("order_id should be stable (echo of input): %s vs %s", a.OrderID, b.OrderID)
	}
	if a.TransactionID != b.TransactionID {
		t.Fatalf("transaction_id should be stable: %s vs %s", a.TransactionID, b.TransactionID)
	}
	if err := domain.ValidateOrderID(a.OrderID); err != nil {
		t.Fatalf("order_id invalid: %v", err)
	}
}

// TransactionID is derived from (merchant_id, order_id) so different inputs
// must yield different transaction_ids. (OrderID is now merchant-supplied and
// just echoes back, so we no longer compare it across cases.)
func TestCreate_DerivedTransactionID_DifferentInputs(t *testing.T) {
	svc, _, _ := newSvc(t)
	a, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, OrderID: "A", Amount: 1, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	b, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, OrderID: "B", Amount: 1, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	c, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: &domain.Merchant{MerchantID: "M2"}, OrderID: "A", Amount: 1, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("c: %v", err)
	}
	if a.TransactionID == b.TransactionID ||
		a.TransactionID == c.TransactionID ||
		b.TransactionID == c.TransactionID {
		t.Fatalf("transaction_id collision: a=%s b=%s c=%s", a.TransactionID, b.TransactionID, c.TransactionID)
	}
}

// TestCreate_ConflictOnAmountMismatch ensures merchants cannot reuse an
// order_id with different material payment data — that would silently
// override or duplicate state otherwise.
func TestCreate_ConflictOnAmountMismatch(t *testing.T) {
	svc, _, _ := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1"}
	in := CreatePaymentInput{
		Merchant: merchant, OrderID: "CONFLICT", Amount: 1500, Currency: "USD",
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
		Merchant: merchant, OrderID: "CCY", Amount: 1500, Currency: "USD",
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
		Merchant: merchant, OrderID: "METHOD", Amount: 1000, Currency: "USD", Method: "crypto",
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
// Create with the same OrderID. The MySQL UNIQUE on (merchant_id,
// transaction_id) — modelled by the in-memory store here — must collapse
// every duplicate onto a single row, and every caller must observe the
// same response.
func TestCreate_ConcurrentCollapsesToSingleRow(t *testing.T) {
	svc, stripeC, store := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1"}
	in := CreatePaymentInput{Merchant: merchant, OrderID: "BURST", Amount: 2000, Currency: "USD"}

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
		{Merchant: merchant, OrderID: "", Amount: 1},
		{Merchant: merchant, OrderID: "T", Amount: 0},
		{Merchant: nil, OrderID: "T", Amount: 1},
	}
	for i, c := range cases {
		if _, err := svc.Create(context.Background(), c); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
}

func TestCreate_RejectsInvalidOrderID(t *testing.T) {
	svc, _, _ := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}
	for _, bad := range []string{"has space", "with#hash", strings.Repeat("a", 65)} {
		_, err := svc.Create(context.Background(), CreatePaymentInput{
			Merchant: merchant, OrderID: bad, Amount: 1, Currency: "USD",
		})
		if !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("OrderID=%q want ErrInvalidRequest, got %v", bad, err)
		}
	}
}

func TestCreate_RejectsZeroAmount(t *testing.T) {
	svc, _, _ := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}
	_, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, OrderID: "order-1", Amount: 0, Currency: "USD",
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest for zero amount, got %v", err)
	}
}

func TestCreate_RejectsNegativeAmount(t *testing.T) {
	svc, _, _ := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}
	_, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, OrderID: "order-1", Amount: -100, Currency: "USD",
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest for negative amount, got %v", err)
	}
}

func TestCreate_WithEmptyCurrency_UsesDefault(t *testing.T) {
	svc, stripeC, _ := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}
	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, OrderID: "order-1", Amount: 1500, Currency: "",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.OrderID == "" {
		t.Fatal("expected successful creation with default currency")
	}
	if stripeC.createCalls != 1 {
		t.Fatalf("stripe should be called once, got %d", stripeC.createCalls)
	}
}

func TestCreate_ReconstructResult_CryptoPayment(t *testing.T) {
	svc, _, store := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}

	// Create crypto payment first
	_, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, OrderID: "crypto-1", Amount: 1000, Currency: "USD", Method: "crypto",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Now simulate idempotent retry - should reconstruct from stored row
	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, OrderID: "crypto-1", Amount: 1000, Currency: "USD", Method: "crypto",
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if res.CryptoPayload == nil {
		t.Fatal("expected crypto payload on retry")
	}
	if store.updateCheckouts != 0 {
		t.Fatalf("should not update checkout for crypto, got %d", store.updateCheckouts)
	}
}

func TestCreate_ReconstructResult_WithSessionID(t *testing.T) {
	svc, _, store := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}

	// Create payment
	res1, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, OrderID: "session-1", Amount: 2000, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Simulate update with session ID
	store.byID["M1:session-1"].StripeSessionID = "cs_simulated"

	// Retry - should use stored session ID
	res2, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, OrderID: "session-1", Amount: 2000, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	// Should match original response
	if res1.OrderID != res2.OrderID {
		t.Fatalf("order_id mismatch")
	}
	if !strings.Contains(res2.CheckoutURL, "cs_simulated") {
		t.Fatalf("checkout_url should use session_id, got %q", res2.CheckoutURL)
	}
}

func TestCreate_ReconstructResult_LazyMode(t *testing.T) {
	stripeC := newStripeStub(t)
	store := newTxStore(t)
	svc := NewPaymentService(stripeC.mock, store.mock, PaymentServiceOptions{
		DefaultCurrency: "USD",
		LazyCheckout:    true,
		PublicBaseURL:   "https://pay.example.com",
		CheckoutSecret:  "test-secret",
	})
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s"}

	// Create lazy payment first
	res1, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, OrderID: "lazy-1", Amount: 3000, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(res1.CheckoutURL, "/pay/") {
		t.Fatalf("lazy should use hosted URL, got %q", res1.CheckoutURL)
	}

	// Retry - should reconstruct lazy URL
	res2, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, OrderID: "lazy-1", Amount: 3000, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if res1.CheckoutURL != res2.CheckoutURL {
		t.Fatalf("lazy url should be stable, got %q vs %q", res1.CheckoutURL, res2.CheckoutURL)
	}
}

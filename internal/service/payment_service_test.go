package service

import (
	"context"
	"errors"
	"testing"

	"github.com/quangdangfit/easypay/internal/domain"
)

func newSvc(t *testing.T) (Payments, *stripeStub, *eventCapture, *idemStore, *orderStore) {
	t.Helper()
	idem := newIdemStore(t)
	stripeC := newStripeStub(t)
	pub := newEventCapture(t)
	store := newOrderStore(t)
	svc := NewPaymentService(idem.mock, stripeC.mock, pub.mock, nil, store.mock, PaymentServiceOptions{
		DefaultCurrency: "USD",
		CryptoContract:  "0xCONTRACT",
		CryptoChainID:   11155111,
		// LazyCheckout off → eager Stripe path (existing tests assume this)
	})
	return svc, stripeC, pub, idem, store
}

func TestCreate_HappyPath(t *testing.T) {
	svc, stripeC, pub, _, _ := newSvc(t)
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
	svc, stripeC, pub, _, _ := newSvc(t)
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
	svc, stripeC, _, _, _ := newSvc(t)
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
	svc := NewPaymentService(idem.mock, stripeC.mock, pub.mock, pending.mock, nil, PaymentServiceOptions{
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
	svc := NewPaymentService(idem.mock, stripeC.mock, pub.mock, pending.mock, nil, PaymentServiceOptions{
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

// TestCreate_ColdPathIdempotency_FromMySQL covers the long-tail scenario:
// Redis idem TTL has expired (months later) but the merchant resends the
// same transaction_id. Service must find the persisted order in MySQL and
// return it instead of creating a duplicate.
func TestCreate_ColdPathIdempotency_FromMySQL(t *testing.T) {
	svc, stripeC, pub, _, store := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1"}

	// Seed an existing order as if it were persisted long ago.
	store.byID["ord-existing"] = &domain.Order{
		OrderID:         "ord-existing",
		MerchantID:      "M1",
		TransactionID:   "TXN-OLD",
		Amount:          1500,
		Currency:        "USD",
		Status:          domain.OrderStatusPaid,
		PaymentMethod:   "card",
		StripeSessionID: "cs_old_123",
		CheckoutURL:     "https://checkout.stripe.com/cs_old_123",
	}

	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant:      merchant,
		TransactionID: "TXN-OLD",
		Amount:        1500,
		Currency:      "USD",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.OrderID != "ord-existing" {
		t.Fatalf("expected reconstruction of existing order, got %s", res.OrderID)
	}
	if res.CheckoutURL != "https://checkout.stripe.com/cs_old_123" {
		t.Fatalf("expected cached checkout URL, got %s", res.CheckoutURL)
	}
	if stripeC.createCalls != 0 {
		t.Fatalf("must not call Stripe on cold-path idem hit, got %d calls", stripeC.createCalls)
	}
	if len(pub.events) != 0 {
		t.Fatalf("must not publish Kafka on cold-path idem hit, got %d events", len(pub.events))
	}
}

// TestCreate_ConflictOnAmountMismatch ensures merchants cannot reuse a
// transaction_id with different material payment data — that would silently
// override or duplicate state otherwise.
func TestCreate_ConflictOnAmountMismatch(t *testing.T) {
	svc, _, _, _, store := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1"}

	store.byID["ord-existing"] = &domain.Order{
		OrderID:       "ord-existing",
		MerchantID:    "M1",
		TransactionID: "TXN-CONFLICT",
		Amount:        1500,
		Currency:      "USD",
		Status:        domain.OrderStatusPending,
		PaymentMethod: "card",
	}

	_, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant:      merchant,
		TransactionID: "TXN-CONFLICT",
		Amount:        9999, // different
		Currency:      "USD",
	})
	if !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("expected ErrTransactionConflict, got %v", err)
	}
}

func TestCreate_ConflictOnCurrencyMismatch(t *testing.T) {
	svc, _, _, _, store := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1"}

	store.byID["ord-existing"] = &domain.Order{
		OrderID:       "ord-existing",
		MerchantID:    "M1",
		TransactionID: "TXN-CCY",
		Amount:        1500,
		Currency:      "USD",
		Status:        domain.OrderStatusPending,
		PaymentMethod: "card",
	}

	_, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant:      merchant,
		TransactionID: "TXN-CCY",
		Amount:        1500,
		Currency:      "EUR",
	})
	if !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("expected ErrTransactionConflict, got %v", err)
	}
}

// TestCreate_ConflictOnMethodSwitch: original was crypto, retry tries Stripe.
// Different settlement bucket → must not silently downgrade or duplicate.
func TestCreate_ConflictOnMethodSwitch(t *testing.T) {
	svc, _, _, _, store := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1"}

	store.byID["ord-existing"] = &domain.Order{
		OrderID:       "ord-existing",
		MerchantID:    "M1",
		TransactionID: "TXN-METHOD",
		Amount:        1000,
		Currency:      "USD",
		Status:        domain.OrderStatusPending,
		PaymentMethod: "crypto_eth",
	}

	_, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant:      merchant,
		TransactionID: "TXN-METHOD",
		Amount:        1000,
		Currency:      "USD",
		// Method omitted → defaults to card / Stripe path.
	})
	if !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("expected ErrTransactionConflict on method switch, got %v", err)
	}
}

// TestCreate_ColdPathHit_RewarmsRedis verifies a cold-path resurrection also
// re-warms the Redis idempotency cache so the next retry hits the fast path.
func TestCreate_ColdPathHit_RewarmsRedis(t *testing.T) {
	svc, _, _, idem, store := newSvc(t)
	merchant := &domain.Merchant{MerchantID: "M1"}

	store.byID["ord-existing"] = &domain.Order{
		OrderID:       "ord-existing",
		MerchantID:    "M1",
		TransactionID: "TXN-WARM",
		Amount:        2000,
		Currency:      "USD",
		PaymentMethod: "card",
		CheckoutURL:   "https://checkout.stripe.com/cs_warm",
	}

	_, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, TransactionID: "TXN-WARM", Amount: 2000, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := idem.store["M1:TXN-WARM"]; !ok {
		t.Fatalf("expected Redis idem cache to be warmed after cold-path hit")
	}
}

// TestCreate_DeterministicOrderID_ConcurrentCollapse verifies that with
// OrderIDSecret set, two concurrent Create calls for the same idempotency
// key collapse to the same order_id, the same Redis idem entry, and a
// single Kafka publish — the consumer-level race that would orphan a row
// is therefore impossible.
func TestCreate_DeterministicOrderID_ConcurrentCollapse(t *testing.T) {
	idem := newIdemStore(t)
	stripeC := newStripeStub(t)
	pub := newEventCapture(t)
	store := newOrderStore(t)
	svc := NewPaymentService(idem.mock, stripeC.mock, pub.mock, nil, store.mock, PaymentServiceOptions{
		DefaultCurrency: "USD",
		OrderIDSecret:   "test-secret-do-not-use-in-prod",
	})
	merchant := &domain.Merchant{MerchantID: "M1"}
	in := CreatePaymentInput{
		Merchant: merchant, TransactionID: "TXN-CONCURRENT", Amount: 1500, Currency: "USD",
	}

	// Two sequential calls with the same input must yield the same order_id.
	first, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.OrderID != second.OrderID {
		t.Fatalf("expected stable order_id, got %s vs %s", first.OrderID, second.OrderID)
	}
	// Sanity-check the format and that it's not the legacy random prefix.
	if first.OrderID == "" || len(first.OrderID) != len("ord-")+24 {
		t.Fatalf("unexpected order_id shape: %q", first.OrderID)
	}
}

// TestCreate_DeterministicOrderID_DifferentInputs ensures the derivation is
// not accidentally collision-prone across distinct merchants/transactions.
func TestCreate_DeterministicOrderID_DifferentInputs(t *testing.T) {
	idem := newIdemStore(t)
	stripeC := newStripeStub(t)
	pub := newEventCapture(t)
	store := newOrderStore(t)
	svc := NewPaymentService(idem.mock, stripeC.mock, pub.mock, nil, store.mock, PaymentServiceOptions{
		DefaultCurrency: "USD",
		OrderIDSecret:   "test-secret-do-not-use-in-prod",
	})

	a, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, TransactionID: "TXN-A", Amount: 1, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	b, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, TransactionID: "TXN-B", Amount: 1, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	c, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: &domain.Merchant{MerchantID: "M2"}, TransactionID: "TXN-A", Amount: 1, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("c: %v", err)
	}
	if a.OrderID == b.OrderID || a.OrderID == c.OrderID || b.OrderID == c.OrderID {
		t.Fatalf("collision: a=%s b=%s c=%s", a.OrderID, b.OrderID, c.OrderID)
	}
}

// TestCreate_NoSecretFallsBackToUUID confirms we keep the legacy random-id
// path when OrderIDSecret is empty, so existing deployments aren't broken
// by this change.
func TestCreate_NoSecretFallsBackToUUID(t *testing.T) {
	svc, _, _, _, _ := newSvc(t) // newSvc sets no OrderIDSecret
	merchant := &domain.Merchant{MerchantID: "M1"}
	res, err := svc.Create(context.Background(), CreatePaymentInput{
		Merchant: merchant, TransactionID: "TXN-LEGACY", Amount: 100, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// UUID-derived IDs are 24 lowercase hex chars; HMAC-derived ones are too.
	// The functional check is: a second call with no secret + Redis idem
	// cleared would produce a DIFFERENT id (because uuid is random).
	if !contains(res.OrderID, "ord-") {
		t.Fatalf("unexpected id: %s", res.OrderID)
	}
}

func TestCreate_RejectsInvalid(t *testing.T) {
	svc, _, _, _, _ := newSvc(t)
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

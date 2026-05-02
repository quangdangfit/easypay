//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/internal/service"
)

// TestCreateOrder_HappyPath drives the sync write path against real MySQL:
// after Create returns, the row must already be in the right shard table
// with the Stripe artefacts persisted.
func TestCreateOrder_HappyPath(t *testing.T) {
	env := SetupEnv(t)
	defer env.Cleanup(t)

	stripeMock := NewMockStripe()
	orderRepo := repository.NewOrderRepository(env.DB)

	svc := service.NewPaymentService(stripeMock, orderRepo, service.PaymentServiceOptions{
		DefaultCurrency: "USD",
		CryptoContract:  "0xCONTRACT",
		CryptoChainID:   11155111,
	})

	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s", RateLimit: 100}
	res, err := svc.Create(context.Background(), service.CreatePaymentInput{
		Merchant: merchant, OrderID: "ORDER-INT-1", Amount: 1500, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.OrderID == "" || res.CheckoutURL == "" {
		t.Fatalf("missing fields: %+v", res)
	}

	// Row must exist immediately after Create returns — sync write.
	o, err := orderRepo.GetByMerchantOrderID(context.Background(), merchant.MerchantID, res.OrderID)
	if err != nil {
		t.Fatalf("get order after Create: %v", err)
	}
	if o.Amount != 1500 || o.Currency != "USD" {
		t.Fatalf("persisted row mismatch: %+v", o)
	}
	if o.StripeSessionID == "" || o.StripePaymentIntentID == "" {
		t.Fatalf("Stripe artefacts not persisted: %+v", o)
	}
	if o.MerchantID != merchant.MerchantID {
		t.Fatalf("merchant_id round-trip mismatch: got %q want %q", o.MerchantID, merchant.MerchantID)
	}
}

// TestIdempotency_DuplicateOrder verifies the (merchant_id, transaction_id)
// UNIQUE collapses retries onto a single row with a single Stripe call.
func TestIdempotency_DuplicateOrder(t *testing.T) {
	env := SetupEnv(t)
	defer env.Cleanup(t)

	stripeMock := NewMockStripe()
	orderRepo := repository.NewOrderRepository(env.DB)

	svc := service.NewPaymentService(stripeMock, orderRepo, service.PaymentServiceOptions{
		DefaultCurrency: "USD",
		CryptoContract:  "0xCONTRACT",
		CryptoChainID:   11155111,
	})
	merchant := &domain.Merchant{MerchantID: "M_IDEM", SecretKey: "s", RateLimit: 100}
	in := service.CreatePaymentInput{
		Merchant: merchant, OrderID: "ORDER-DUPE", Amount: 999, Currency: "USD",
	}

	first, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.OrderID != second.OrderID {
		t.Fatalf("ids differ across idempotent calls: %s vs %s", first.OrderID, second.OrderID)
	}
	if first.TransactionID != second.TransactionID {
		t.Fatalf("transaction_id differs across idempotent calls")
	}
	if stripeMock.CheckoutCount != 1 {
		t.Fatalf("Stripe should be called once for duplicates, got %d", stripeMock.CheckoutCount)
	}
}

// TestConcurrentCreate_Real exercises the strong consistency guarantee:
// 32 goroutines hammer Create with the same OrderID; the MySQL
// UNIQUE on (merchant_id, transaction_id) ensures exactly one INSERT
// wins, the others fall back to GetByTransactionID and reconstruct the
// winner's response.
func TestConcurrentCreate_Real(t *testing.T) {
	env := SetupEnv(t)
	defer env.Cleanup(t)

	stripeMock := NewMockStripe()
	orderRepo := repository.NewOrderRepository(env.DB)

	svc := service.NewPaymentService(stripeMock, orderRepo, service.PaymentServiceOptions{
		DefaultCurrency: "USD",
	})
	merchant := &domain.Merchant{MerchantID: "M_CONC"}
	in := service.CreatePaymentInput{
		Merchant: merchant, OrderID: "ORDER-CONC", Amount: 2500, Currency: "USD",
	}

	const N = 32
	results := make([]*service.CreatePaymentResult, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.Create(context.Background(), in)
		}(i)
	}
	close(start)
	wg.Wait()

	var fatal atomic.Int32
	for i, err := range errs {
		if err != nil && !errors.Is(err, service.ErrTransactionConflict) {
			t.Errorf("worker %d: %v", i, err)
			fatal.Add(1)
		}
	}
	if fatal.Load() > 0 {
		t.FailNow()
	}
	first := results[0]
	for i, r := range results {
		if r == nil {
			t.Fatalf("worker %d: nil result", i)
		}
		if r.OrderID != first.OrderID {
			t.Fatalf("worker %d order_id %s != %s", i, r.OrderID, first.OrderID)
		}
	}

	// Exactly one row in DB.
	got, err := orderRepo.GetByMerchantOrderID(context.Background(), merchant.MerchantID, first.OrderID)
	if err != nil {
		t.Fatalf("read winner row: %v", err)
	}
	if got.Status != domain.OrderStatusCreated {
		t.Fatalf("status: got %s want created", got.Status)
	}
}

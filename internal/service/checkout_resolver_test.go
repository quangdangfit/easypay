package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/quangdangfit/easypay/internal/cache"
	"github.com/quangdangfit/easypay/internal/domain"
	stripemock "github.com/quangdangfit/easypay/internal/mocks/stripe"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
)

const (
	tMerchant = "M1"
	tOrder    = "ord-1"
)

func tKey() string { return tMerchant + ":" + tOrder }

// resolverDeps holds the wired dependencies so tests can inspect state.
type resolverDeps struct {
	repo    *txStore
	stripeC *stripeStub
}

func newResolverWithDeps(t *testing.T, opts ...func(*CheckoutResolverOptions)) (Checkouts, *resolverDeps) {
	t.Helper()
	d := &resolverDeps{
		repo:    newTxStore(t),
		stripeC: newStripeStub(t),
	}
	o := CheckoutResolverOptions{
		Stripe:            d.stripeC.mock,
		Repo:              d.repo.mock,
		Merchants:         stubMerchants(t, 0),
		Locker:            successLocker(t),
		URLCache:          cache.NewURLCache(8, 5*time.Second),
		Bucket:            allowingBucket(t),
		DefaultSuccessURL: "https://merchant/success",
		DefaultCancelURL:  "https://merchant/cancel",
	}
	for _, f := range opts {
		f(&o)
	}
	return NewCheckoutResolver(o), d
}

func TestResolve_DBHotPath_ReconstructsFromSession(t *testing.T) {
	r, d := newResolverWithDeps(t)
	d.repo.byID[tKey()] = &domain.Transaction{MerchantID: tMerchant, OrderID: tOrder, StripeSessionID: "cs_old"}
	url, err := r.Resolve(context.Background(), tMerchant, tOrder)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if url != "https://checkout.stripe.com/c/pay/cs_old" {
		t.Fatalf("expected reconstructed url, got %q", url)
	}
	if d.stripeC.createCalls != 0 {
		t.Fatalf("must not call Stripe when session_id is already persisted, got %d", d.stripeC.createCalls)
	}
}

func TestResolve_LazyOrderTriggersStripeCreate(t *testing.T) {
	// Row exists but no Stripe session yet — resolver creates one.
	r, d := newResolverWithDeps(t)
	d.repo.byID[tKey()] = &domain.Transaction{MerchantID: tMerchant, OrderID: tOrder, Amount: 1500, Currency: "USD"}
	url, err := r.Resolve(context.Background(), tMerchant, tOrder)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if url != "https://checkout.stripe.com/cs_test_123" {
		t.Fatalf("expected fresh stripe url, got %q", url)
	}
	if d.stripeC.createCalls != 1 {
		t.Fatalf("expected 1 stripe call, got %d", d.stripeC.createCalls)
	}
	if d.repo.updateCheckouts != 1 {
		t.Fatalf("expected UpdateCheckout to persist, got %d", d.repo.updateCheckouts)
	}
}

func TestResolve_NotFoundWhenRowMissing(t *testing.T) {
	r, _ := newResolverWithDeps(t)
	_, err := r.Resolve(context.Background(), tMerchant, "ord-none")
	if !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("want ErrOrderNotFound, got %v", err)
	}
}

func TestResolve_RateLimitReturnsUnavailable(t *testing.T) {
	r, d := newResolverWithDeps(t, func(o *CheckoutResolverOptions) {
		o.Bucket = blockingBucket(t)
	})
	d.repo.byID[tKey()] = &domain.Transaction{MerchantID: tMerchant, OrderID: tOrder, Amount: 1500, Currency: "USD"}
	_, err := r.Resolve(context.Background(), tMerchant, tOrder)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestResolve_BreakerOpenReturnsUnavailable(t *testing.T) {
	// Custom Stripe mock: CreateCheckoutSession returns ErrCircuitOpen.
	openMock := stripemock.NewMockClient(gomock.NewController(t))
	openMock.EXPECT().CreateCheckoutSession(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, stripe.ErrCircuitOpen).AnyTimes()

	r, d := newResolverWithDeps(t, func(o *CheckoutResolverOptions) {
		o.Stripe = openMock
	})
	d.repo.byID[tKey()] = &domain.Transaction{MerchantID: tMerchant, OrderID: tOrder, Amount: 1500, Currency: "USD"}
	_, err := r.Resolve(context.Background(), tMerchant, tOrder)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestResolveAfterLock_ReturnsURLWhenPresent(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{MerchantID: tMerchant, OrderID: tOrder, StripeSessionID: "cs_x"})
	r := NewCheckoutResolver(CheckoutResolverOptions{
		Repo:     repo.mock,
		URLCache: cache.NewURLCache(8, time.Second),
	}).(*checkoutResolver)
	url, err := r.resolveAfterLock(context.Background(), 0, tMerchant, tOrder, tKey())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if url != "https://checkout.stripe.com/c/pay/cs_x" {
		t.Fatalf("url=%q", url)
	}
}

func TestResolveAfterLock_NotReadyWhenMissing(t *testing.T) {
	r := NewCheckoutResolver(CheckoutResolverOptions{
		Repo:     newTxStore(t).mock,
		URLCache: cache.NewURLCache(8, time.Second),
	}).(*checkoutResolver)
	_, err := r.resolveAfterLock(context.Background(), 0, tMerchant, "ord-none", tMerchant+":ord-none")
	if !errors.Is(err, ErrOrderNotReady) {
		t.Fatalf("got %v", err)
	}
}

func TestResolve_MerchantNotFound(t *testing.T) {
	r, _ := newResolverWithDeps(t, func(o *CheckoutResolverOptions) {
		o.Merchants = stubMerchantsError(t)
	})
	_, err := r.Resolve(context.Background(), "MISSING", tOrder)
	if !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("want ErrOrderNotFound for missing merchant, got %v", err)
	}
}

func TestResolveAfterLock_NotReadyWhenSessionMissing(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{MerchantID: tMerchant, OrderID: tOrder, Amount: 1}) // no StripeSessionID
	r := NewCheckoutResolver(CheckoutResolverOptions{Repo: repo.mock}).(*checkoutResolver)
	_, err := r.resolveAfterLock(context.Background(), 0, tMerchant, tOrder, tKey())
	if !errors.Is(err, ErrOrderNotReady) {
		t.Fatalf("got %v", err)
	}
}

func TestResolve_LocalLRUSecondHit(t *testing.T) {
	r, d := newResolverWithDeps(t)
	d.repo.byID[tKey()] = &domain.Transaction{MerchantID: tMerchant, OrderID: tOrder, StripeSessionID: "cs_old"}
	_, _ = r.Resolve(context.Background(), tMerchant, tOrder)
	// Second call should hit the in-process cache; we delete the DB row to
	// prove the cache is what serves the response.
	delete(d.repo.byID, tKey())
	url, err := r.Resolve(context.Background(), tMerchant, tOrder)
	if err != nil || url != "https://checkout.stripe.com/c/pay/cs_old" {
		t.Fatalf("expected lru hit; url=%q err=%v", url, err)
	}
}

func TestResolve_LockAcquireFailureRetries(t *testing.T) {
	// When lock acquisition fails, the code does a 150ms sleep and retries via resolveAfterLock.
	// For the retry to succeed, the order must have a StripeSessionID (placed there by another pod).
	r, d := newResolverWithDeps(t, func(o *CheckoutResolverOptions) {
		o.Locker = failingLocker(t)
	})
	d.repo.byID[tKey()] = &domain.Transaction{MerchantID: tMerchant, OrderID: tOrder, Amount: 1500, Currency: "USD", StripeSessionID: "cs_created_by_other"}
	url, err := r.Resolve(context.Background(), tMerchant, tOrder)
	if err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if url != "https://checkout.stripe.com/c/pay/cs_created_by_other" {
		t.Fatalf("expected session from other pod, got %q", url)
	}
}

func TestResolve_GetOrderError(t *testing.T) {
	// Simulate GetByMerchantOrderID error on initial lookup
	r, _ := newResolverWithDeps(t)
	// Don't populate the repo, so GetByMerchantOrderID will return ErrNotFound
	_, err := r.Resolve(context.Background(), tMerchant, "ord-missing")
	if !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("expected ErrOrderNotFound, got %v", err)
	}
}

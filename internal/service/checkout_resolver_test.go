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

// resolverDeps holds the wired dependencies so tests can inspect state.
type resolverDeps struct {
	repo    *orderStore
	pending *pendingStore
	stripeC *stripeStub
}

func newResolverWithDeps(t *testing.T, opts ...func(*CheckoutResolverOptions)) (Checkouts, *resolverDeps) {
	t.Helper()
	d := &resolverDeps{
		repo:    newOrderStore(t),
		pending: newPendingStore(t),
		stripeC: newStripeStub(t),
	}
	o := CheckoutResolverOptions{
		Stripe:            d.stripeC.mock,
		Repo:              d.repo.mock,
		Pending:           d.pending.mock,
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

func TestResolve_DBHotPath(t *testing.T) {
	r, d := newResolverWithDeps(t)
	d.repo.byID["ord-1"] = &domain.Order{OrderID: "ord-1", CheckoutURL: "https://stripe/cached", StripeSessionID: "cs_old"}
	url, err := r.Resolve(context.Background(), "ord-1")
	if err != nil || url != "https://stripe/cached" {
		t.Fatalf("url=%q err=%v", url, err)
	}
}

func TestResolve_LazyURLNotReturnedAsHit(t *testing.T) {
	// DB row has a self-hosted URL but no Stripe session yet — must not be
	// returned as a cache hit (would cause redirect loop).
	r, d := newResolverWithDeps(t)
	d.repo.byID["ord-1"] = &domain.Order{OrderID: "ord-1", CheckoutURL: "http://localhost:8080/pay/ord-1", StripeSessionID: ""}
	url, err := r.Resolve(context.Background(), "ord-1")
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

func TestResolve_FallsBackToPendingSnapshot(t *testing.T) {
	r, d := newResolverWithDeps(t)
	d.pending.store["ord-1"] = &cache.PendingOrder{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD"}
	url, err := r.Resolve(context.Background(), "ord-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if url != "https://checkout.stripe.com/cs_test_123" {
		t.Fatalf("got %q", url)
	}
	if d.stripeC.createCalls != 1 {
		t.Fatalf("expected 1 stripe call, got %d", d.stripeC.createCalls)
	}
}

func TestResolve_NotReadyWhenBothMiss(t *testing.T) {
	r, _ := newResolverWithDeps(t)
	_, err := r.Resolve(context.Background(), "ord-none")
	if !errors.Is(err, ErrOrderNotReady) {
		t.Fatalf("want ErrOrderNotReady, got %v", err)
	}
}

func TestResolve_RateLimitReturnsUnavailable(t *testing.T) {
	r, d := newResolverWithDeps(t, func(o *CheckoutResolverOptions) {
		o.Bucket = blockingBucket(t)
	})
	d.pending.store["ord-1"] = &cache.PendingOrder{OrderID: "ord-1"}
	_, err := r.Resolve(context.Background(), "ord-1")
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
	d.pending.store["ord-1"] = &cache.PendingOrder{OrderID: "ord-1"}
	_, err := r.Resolve(context.Background(), "ord-1")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestResolveAfterLock_ReturnsURLWhenPresent(t *testing.T) {
	repo := newOrderStore(t, &domain.Order{OrderID: "ord-1", CheckoutURL: "https://stripe/x", StripeSessionID: "cs_x"})
	r := NewCheckoutResolver(CheckoutResolverOptions{
		Repo:     repo.mock,
		URLCache: cache.NewURLCache(8, time.Second),
	}).(*checkoutResolver)
	url, err := r.resolveAfterLock(context.Background(), "ord-1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if url != "https://stripe/x" {
		t.Fatalf("url=%q", url)
	}
}

func TestResolveAfterLock_NotReadyWhenMissing(t *testing.T) {
	r := NewCheckoutResolver(CheckoutResolverOptions{
		Repo:     newOrderStore(t).mock,
		URLCache: cache.NewURLCache(8, time.Second),
	}).(*checkoutResolver)
	_, err := r.resolveAfterLock(context.Background(), "ord-none")
	if !errors.Is(err, ErrOrderNotReady) {
		t.Fatalf("got %v", err)
	}
}

func TestResolveAfterLock_NotReadyWhenURLMissing(t *testing.T) {
	repo := newOrderStore(t, &domain.Order{OrderID: "ord-1", StripeSessionID: "cs_x"}) // no CheckoutURL
	r := NewCheckoutResolver(CheckoutResolverOptions{Repo: repo.mock}).(*checkoutResolver)
	_, err := r.resolveAfterLock(context.Background(), "ord-1")
	if !errors.Is(err, ErrOrderNotReady) {
		t.Fatalf("got %v", err)
	}
}

func TestResolve_LocalLRUSecondHit(t *testing.T) {
	r, d := newResolverWithDeps(t)
	d.repo.byID["ord-1"] = &domain.Order{OrderID: "ord-1", CheckoutURL: "https://stripe/cached", StripeSessionID: "cs_old"}
	_, _ = r.Resolve(context.Background(), "ord-1")
	// Second call should hit the in-process cache; we delete the DB row to
	// prove the cache is what serves the response.
	delete(d.repo.byID, "ord-1")
	url, err := r.Resolve(context.Background(), "ord-1")
	if err != nil || url != "https://stripe/cached" {
		t.Fatalf("expected lru hit; url=%q err=%v", url, err)
	}
}

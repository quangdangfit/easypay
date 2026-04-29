package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/quangdangfit/easypay/internal/cache"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
	"github.com/quangdangfit/easypay/internal/repository"
)

// --- fakes ---

type fakeOrderRepo struct {
	byID map[string]*domain.Order
	// counts how many times CreateSession was called
	updateCheckoutCalls int
}

func (f *fakeOrderRepo) Create(ctx context.Context, o *domain.Order) error {
	if f.byID == nil {
		f.byID = map[string]*domain.Order{}
	}
	f.byID[o.OrderID] = o
	return nil
}
func (f *fakeOrderRepo) GetByOrderID(ctx context.Context, id string) (*domain.Order, error) {
	if o, ok := f.byID[id]; ok {
		return o, nil
	}
	return nil, repository.ErrNotFound
}
func (f *fakeOrderRepo) GetByPaymentIntentID(ctx context.Context, pi string) (*domain.Order, error) {
	return nil, repository.ErrNotFound
}
func (f *fakeOrderRepo) UpdateStatus(ctx context.Context, id string, st domain.OrderStatus, pi string) error {
	if o, ok := f.byID[id]; ok {
		o.Status = st
		if pi != "" {
			o.StripePaymentIntentID = pi
		}
		return nil
	}
	return repository.ErrNotFound
}
func (f *fakeOrderRepo) UpdateCheckout(ctx context.Context, id, sid, pi, url string) error {
	f.updateCheckoutCalls++
	if o, ok := f.byID[id]; ok {
		o.StripeSessionID = sid
		o.StripePaymentIntentID = pi
		o.CheckoutURL = url
		return nil
	}
	return repository.ErrNotFound
}
func (f *fakeOrderRepo) BatchCreate(ctx context.Context, orders []*domain.Order) error {
	for _, o := range orders {
		_ = f.Create(ctx, o)
	}
	return nil
}
func (f *fakeOrderRepo) GetPendingBefore(ctx context.Context, before time.Time, limit int) ([]*domain.Order, error) {
	return nil, nil
}

type fakePending struct {
	store map[string]*cache.PendingOrder
}

func (f *fakePending) Put(ctx context.Context, o *cache.PendingOrder, ttl time.Duration) error {
	if f.store == nil {
		f.store = map[string]*cache.PendingOrder{}
	}
	f.store[o.OrderID] = o
	return nil
}
func (f *fakePending) Get(ctx context.Context, id string) (*cache.PendingOrder, error) {
	if o, ok := f.store[id]; ok {
		return o, nil
	}
	return nil, cache.ErrPendingOrderNotFound
}

type fakeLocker struct{ refused bool }

func (f *fakeLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (cache.Lock, error) {
	if f.refused {
		return nil, cache.ErrLockNotAcquired
	}
	return &fakeLock{}, nil
}

type fakeLock struct{}

func (l *fakeLock) Release(ctx context.Context) error { return nil }

type fakeBucket struct{ allowed bool }

func (b *fakeBucket) Allow(ctx context.Context) error {
	if b.allowed {
		return nil
	}
	return cache.ErrRateLimited
}

type stripeClientFake struct {
	calls int
	err   error
}

func (s *stripeClientFake) CreateCheckoutSession(ctx context.Context, req stripe.CreateCheckoutRequest, idem string) (*stripe.CheckoutSession, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &stripe.CheckoutSession{ID: "cs_x", URL: "https://stripe/x", PaymentIntentID: "pi_x"}, nil
}
func (s *stripeClientFake) CreatePaymentIntent(ctx context.Context, r stripe.CreatePaymentIntentRequest, idem string) (*stripe.PaymentIntent, error) {
	return nil, nil
}
func (s *stripeClientFake) GetPaymentIntent(ctx context.Context, id string) (*stripe.PaymentIntent, error) {
	return nil, nil
}
func (s *stripeClientFake) GetCheckoutSession(ctx context.Context, id string) (*stripe.CheckoutSession, error) {
	return nil, nil
}
func (s *stripeClientFake) CreateRefund(ctx context.Context, r stripe.CreateRefundRequest, idem string) (*stripe.Refund, error) {
	return nil, nil
}
func (s *stripeClientFake) VerifyWebhookSignature(p []byte, h, sec string) (*stripe.Event, error) {
	return nil, nil
}

// --- helpers ---

func newResolver(repo *fakeOrderRepo, pending *fakePending, sc stripe.Client, opts ...func(*CheckoutResolverOptions)) (Checkouts, *fakeBucket) {
	bucket := &fakeBucket{allowed: true}
	o := CheckoutResolverOptions{
		Stripe:            sc,
		Repo:              repo,
		Pending:           pending,
		Locker:            &fakeLocker{},
		URLCache:          cache.NewURLCache(8, 5*time.Second),
		Bucket:            bucket,
		DefaultSuccessURL: "https://merchant/success",
		DefaultCancelURL:  "https://merchant/cancel",
	}
	for _, f := range opts {
		f(&o)
	}
	return NewCheckoutResolver(o), bucket
}

// --- tests ---

func TestResolve_DBHotPath(t *testing.T) {
	repo := &fakeOrderRepo{}
	repo.byID = map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", CheckoutURL: "https://stripe/cached", StripeSessionID: "cs_old"},
	}
	r, _ := newResolver(repo, &fakePending{}, &stripeClientFake{})
	url, err := r.Resolve(context.Background(), "ORD-1")
	if err != nil || url != "https://stripe/cached" {
		t.Fatalf("url=%q err=%v", url, err)
	}
}

func TestResolve_LazyURLNotReturnedAsHit(t *testing.T) {
	// DB row has a self-hosted URL but no Stripe session yet — must not be
	// returned as a cache hit (would cause redirect loop).
	repo := &fakeOrderRepo{}
	repo.byID = map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", CheckoutURL: "http://localhost:8080/pay/ORD-1", StripeSessionID: ""},
	}
	sc := &stripeClientFake{}
	r, _ := newResolver(repo, &fakePending{}, sc)
	url, err := r.Resolve(context.Background(), "ORD-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if url != "https://stripe/x" {
		t.Fatalf("expected fresh stripe url, got %q", url)
	}
	if sc.calls != 1 {
		t.Fatalf("expected 1 stripe call, got %d", sc.calls)
	}
	if repo.updateCheckoutCalls != 1 {
		t.Fatalf("expected UpdateCheckout to persist, got %d", repo.updateCheckoutCalls)
	}
}

func TestResolve_FallsBackToPendingSnapshot(t *testing.T) {
	pending := &fakePending{}
	_ = pending.Put(context.Background(), &cache.PendingOrder{
		OrderID: "ORD-1", MerchantID: "M1", Amount: 1500, Currency: "USD",
	}, time.Hour)
	sc := &stripeClientFake{}
	r, _ := newResolver(&fakeOrderRepo{}, pending, sc)
	url, err := r.Resolve(context.Background(), "ORD-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if url != "https://stripe/x" {
		t.Fatalf("got %q", url)
	}
	if sc.calls != 1 {
		t.Fatalf("expected 1 stripe call, got %d", sc.calls)
	}
}

func TestResolve_NotReadyWhenBothMiss(t *testing.T) {
	r, _ := newResolver(&fakeOrderRepo{}, &fakePending{}, &stripeClientFake{})
	_, err := r.Resolve(context.Background(), "ORD-NONE")
	if !errors.Is(err, ErrOrderNotReady) {
		t.Fatalf("want ErrOrderNotReady, got %v", err)
	}
}

func TestResolve_RateLimitReturnsUnavailable(t *testing.T) {
	pending := &fakePending{}
	_ = pending.Put(context.Background(), &cache.PendingOrder{OrderID: "ORD-1"}, time.Hour)

	r, bucket := newResolver(&fakeOrderRepo{}, pending, &stripeClientFake{})
	bucket.allowed = false
	_, err := r.Resolve(context.Background(), "ORD-1")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestResolve_BreakerOpenReturnsUnavailable(t *testing.T) {
	pending := &fakePending{}
	_ = pending.Put(context.Background(), &cache.PendingOrder{OrderID: "ORD-1"}, time.Hour)

	sc := &stripeClientFake{err: stripe.ErrCircuitOpen}
	r, _ := newResolver(&fakeOrderRepo{}, pending, sc)
	_, err := r.Resolve(context.Background(), "ORD-1")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestResolveAfterLock_ReturnsURLWhenPresent(t *testing.T) {
	repo := &fakeOrderRepo{byID: map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", CheckoutURL: "https://stripe/x", StripeSessionID: "cs_x"},
	}}
	r := NewCheckoutResolver(CheckoutResolverOptions{
		Repo:     repo,
		URLCache: cache.NewURLCache(8, time.Second),
	}).(*checkoutResolver)
	url, err := r.resolveAfterLock(context.Background(), "ORD-1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if url != "https://stripe/x" {
		t.Fatalf("url=%q", url)
	}
}

func TestResolveAfterLock_NotReadyWhenMissing(t *testing.T) {
	r := NewCheckoutResolver(CheckoutResolverOptions{
		Repo:     &fakeOrderRepo{},
		URLCache: cache.NewURLCache(8, time.Second),
	}).(*checkoutResolver)
	_, err := r.resolveAfterLock(context.Background(), "ORD-NONE")
	if !errors.Is(err, ErrOrderNotReady) {
		t.Fatalf("got %v", err)
	}
}

func TestResolveAfterLock_NotReadyWhenURLMissing(t *testing.T) {
	repo := &fakeOrderRepo{byID: map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", StripeSessionID: "cs_x"}, // no CheckoutURL
	}}
	r := NewCheckoutResolver(CheckoutResolverOptions{Repo: repo}).(*checkoutResolver)
	_, err := r.resolveAfterLock(context.Background(), "ORD-1")
	if !errors.Is(err, ErrOrderNotReady) {
		t.Fatalf("got %v", err)
	}
}

func TestResolve_LocalLRUSecondHit(t *testing.T) {
	repo := &fakeOrderRepo{}
	repo.byID = map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", CheckoutURL: "https://stripe/cached", StripeSessionID: "cs_old"},
	}
	r, _ := newResolver(repo, &fakePending{}, &stripeClientFake{})
	_, _ = r.Resolve(context.Background(), "ORD-1")
	// Second call should hit the in-process cache; we delete the DB row to
	// prove the cache is what serves the response.
	delete(repo.byID, "ORD-1")
	url, err := r.Resolve(context.Background(), "ORD-1")
	if err != nil || url != "https://stripe/cached" {
		t.Fatalf("expected lru hit; url=%q err=%v", url, err)
	}
}

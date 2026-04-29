package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/quangdangfit/easypay/internal/api/middleware"
	"github.com/quangdangfit/easypay/internal/cache"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
	"github.com/quangdangfit/easypay/internal/repository"
)

// CheckoutResolver lazily creates a Stripe Checkout Session the first time a
// user opens the public hosted checkout URL.
//
// Reliability mechanics in priority order:
//  1. In-process URL cache (sub-ms) — covers retry/reload/double-click.
//  2. MySQL row → cached URL (ms).
//  3. Redis pending-order snapshot (ms) — covers consumer lag right after POST.
//  4. Distributed lock on order_id — concurrent first-clicks across pods don't
//     all create Stripe sessions; the loser of the race re-reads.
//  5. Token bucket rate limiter (Redis) — caps Stripe call rate per pod
//     fleet, e.g. 80 rps under Stripe's 100 rps default.
//  6. Circuit breaker (gobreaker) — if Stripe is degraded, fail fast with
//     ErrCircuitOpen so the user sees a polished "try again" page.
type CheckoutResolver struct {
	stripe   stripe.Client
	repo     repository.OrderRepository
	pending  cache.PendingOrderStore
	locker   *cache.Locker
	urlCache *cache.URLCache
	bucket   *cache.TokenBucket
}

func NewCheckoutResolver(
	s stripe.Client,
	repo repository.OrderRepository,
	pending cache.PendingOrderStore,
	locker *cache.Locker,
	urlCache *cache.URLCache,
	bucket *cache.TokenBucket,
) *CheckoutResolver {
	return &CheckoutResolver{
		stripe:   s,
		repo:     repo,
		pending:  pending,
		locker:   locker,
		urlCache: urlCache,
		bucket:   bucket,
	}
}

var (
	ErrOrderNotReady = errors.New("order not yet available")
	ErrUnavailable   = errors.New("checkout temporarily unavailable")
)

// Resolve returns the Stripe Checkout URL for an order. Multi-tier with
// metrics and graceful degradation; see struct doc.
func (r *CheckoutResolver) Resolve(ctx context.Context, orderID string) (string, error) {
	// Tier 1: in-process LRU.
	if r.urlCache != nil {
		if url, ok := r.urlCache.Get(orderID); ok {
			middleware.CheckoutResolveResult.WithLabelValues("cached_local").Inc()
			return url, nil
		}
	}

	// Tier 2: MySQL row (covers normal post-consumer-commit case).
	t0 := time.Now()
	order, err := r.repo.GetByOrderID(ctx, orderID)
	middleware.CheckoutResolveDuration.WithLabelValues("db_lookup").Observe(time.Since(t0).Seconds())
	if err == nil && order != nil && order.CheckoutURL != "" && order.StripeSessionID != "" {
		r.cacheURL(orderID, order.CheckoutURL)
		middleware.CheckoutResolveResult.WithLabelValues("cached_db").Inc()
		return order.CheckoutURL, nil
	}
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		middleware.CheckoutResolveResult.WithLabelValues("failed").Inc()
		return "", fmt.Errorf("load order: %w", err)
	}

	// Tier 3: Redis snapshot fallback if consumer hasn't committed yet.
	var snap *cache.PendingOrder
	if order == nil {
		t0 := time.Now()
		snap, err = r.pending.Get(ctx, orderID)
		middleware.CheckoutResolveDuration.WithLabelValues("redis_snap").Observe(time.Since(t0).Seconds())
		if err != nil {
			middleware.CheckoutResolveResult.WithLabelValues("not_ready").Inc()
			return "", ErrOrderNotReady
		}
	}

	// Tier 4: distributed lock to coalesce concurrent first-clicks.
	t0 = time.Now()
	lock, err := r.locker.Acquire(ctx, "checkout:"+orderID, 10*time.Second)
	middleware.CheckoutResolveDuration.WithLabelValues("lock_acquire").Observe(time.Since(t0).Seconds())
	if err != nil {
		// Someone else is creating it — quick retry on the read path.
		time.Sleep(150 * time.Millisecond)
		return r.resolveAfterLock(ctx, orderID)
	}
	defer lock.Release(ctx)

	// Re-check after lock — same condition as the initial cache hit.
	// Both fields must be present, otherwise we may return a lazy URL that
	// points back at /pay/:id and create a redirect loop.
	if order != nil && order.CheckoutURL != "" && order.StripeSessionID != "" {
		r.cacheURL(orderID, order.CheckoutURL)
		return order.CheckoutURL, nil
	}
	if order == nil {
		if cached, _ := r.repo.GetByOrderID(ctx, orderID); cached != nil && cached.CheckoutURL != "" && cached.StripeSessionID != "" {
			r.cacheURL(orderID, cached.CheckoutURL)
			return cached.CheckoutURL, nil
		}
	}

	// Tier 5: rate limit Stripe calls across the fleet.
	if r.bucket != nil {
		if err := r.bucket.Allow(ctx); err != nil {
			middleware.StripeRateLimited.Inc()
			middleware.CheckoutResolveResult.WithLabelValues("rate_limited").Inc()
			return "", ErrUnavailable
		}
	}

	// Tier 6: Stripe call (already wrapped by circuit breaker).
	t0 = time.Now()
	session, err := r.createSession(ctx, order, snap)
	middleware.CheckoutResolveDuration.WithLabelValues("stripe_create").Observe(time.Since(t0).Seconds())
	if err != nil {
		if errors.Is(err, stripe.ErrCircuitOpen) {
			middleware.CheckoutResolveResult.WithLabelValues("breaker_open").Inc()
			return "", ErrUnavailable
		}
		middleware.CheckoutResolveResult.WithLabelValues("failed").Inc()
		return "", err
	}

	// Persist + warm cache.
	if order != nil {
		_ = r.repo.UpdateCheckout(ctx, orderID, session.ID, session.PaymentIntentID, session.URL)
	}
	r.cacheURL(orderID, session.URL)
	middleware.CheckoutResolveResult.WithLabelValues("created").Inc()
	return session.URL, nil
}

func (r *CheckoutResolver) cacheURL(orderID, url string) {
	if r.urlCache != nil {
		r.urlCache.Put(orderID, url)
	}
}

func (r *CheckoutResolver) resolveAfterLock(ctx context.Context, orderID string) (string, error) {
	order, err := r.repo.GetByOrderID(ctx, orderID)
	if err == nil && order != nil && order.CheckoutURL != "" && order.StripeSessionID != "" {
		r.cacheURL(orderID, order.CheckoutURL)
		return order.CheckoutURL, nil
	}
	return "", ErrOrderNotReady
}

func (r *CheckoutResolver) createSession(ctx context.Context, order *domain.Order, snap *cache.PendingOrder) (*stripe.CheckoutSession, error) {
	var req stripe.CreateCheckoutRequest
	switch {
	case order != nil:
		req = stripe.CreateCheckoutRequest{
			Amount:             order.Amount,
			Currency:           strings.ToLower(order.Currency),
			PaymentMethodTypes: []string{"card"},
			ClientReferenceID:  order.OrderID,
			Metadata: map[string]string{
				"order_id":    order.OrderID,
				"merchant_id": order.MerchantID,
			},
		}
	case snap != nil:
		req = stripe.CreateCheckoutRequest{
			Amount:             snap.Amount,
			Currency:           strings.ToLower(snap.Currency),
			PaymentMethodTypes: []string{"card"},
			CustomerEmail:      snap.CustomerEmail,
			SuccessURL:         snap.SuccessURL,
			CancelURL:          snap.CancelURL,
			ClientReferenceID:  snap.OrderID,
			Metadata: map[string]string{
				"order_id":    snap.OrderID,
				"merchant_id": snap.MerchantID,
			},
		}
	default:
		return nil, ErrOrderNotReady
	}
	return r.stripe.CreateCheckoutSession(ctx, req, "checkout:"+req.ClientReferenceID)
}

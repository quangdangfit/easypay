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
	"github.com/quangdangfit/easypay/pkg/logger"
)

// CheckoutResolver lazily creates a Stripe Checkout Session the first time a
// user opens the public hosted-checkout URL.
//
// With sync write on POST /api/payments, the order row is always durable
// before the merchant ever sees an order_id — so the legacy "Redis pending
// snapshot" tier is gone. The remaining tiers, in priority order:
//  1. In-process URL cache (sub-ms) — covers retry/reload/double-click.
//  2. MySQL row → reconstruct URL from stripe_session_id (ms).
//  3. Distributed lock on order_id — concurrent first-clicks across pods
//     don't all create Stripe sessions; the loser of the race re-reads.
//  4. Token bucket rate limiter (Redis) — caps Stripe call rate per pod
//     fleet, e.g. 80 rps under Stripe's 100 rps default.
//  5. Stripe call (already wrapped by circuit breaker).
type checkoutResolver struct {
	stripe            stripe.Client
	repo              repository.OrderRepository
	merchants         repository.MerchantRepository
	locker            cache.Locker
	urlCache          cache.URLCache
	bucket            cache.TokenBucket
	defaultSuccessURL string
	defaultCancelURL  string
}

type CheckoutResolverOptions struct {
	Stripe stripe.Client
	Repo   repository.OrderRepository
	// Merchants resolves merchant_id → shard_index for routing the
	// transactions-table calls. The /pay/:merchant_id/:order_id endpoint
	// is unauthenticated, so we don't have a *domain.Merchant on hand —
	// the LRU-cached lookup is the cheap source of shard info.
	Merchants         repository.MerchantRepository
	Locker            cache.Locker
	URLCache          cache.URLCache
	Bucket            cache.TokenBucket
	DefaultSuccessURL string
	DefaultCancelURL  string
}

func NewCheckoutResolver(opts CheckoutResolverOptions) Checkouts {
	return &checkoutResolver{
		stripe:            opts.Stripe,
		repo:              opts.Repo,
		merchants:         opts.Merchants,
		locker:            opts.Locker,
		urlCache:          opts.URLCache,
		bucket:            opts.Bucket,
		defaultSuccessURL: opts.DefaultSuccessURL,
		defaultCancelURL:  opts.DefaultCancelURL,
	}
}

var (
	ErrOrderNotReady = errors.New("order not yet available")
	ErrUnavailable   = errors.New("checkout temporarily unavailable")
)

// stripeCheckoutURL reconstructs the public Stripe Checkout URL from a
// session_id. The stable form is "https://checkout.stripe.com/c/pay/<id>";
// Stripe's redirect tail (the #fingerprint) doesn't matter — the page loads
// from the session_id alone.
func stripeCheckoutURL(sessionID string) string {
	return "https://checkout.stripe.com/c/pay/" + sessionID
}

// Resolve returns the Stripe Checkout URL for an order. Multi-tier with
// metrics and graceful degradation; see struct doc.
func (r *checkoutResolver) Resolve(ctx context.Context, merchantID, orderID string) (string, error) {
	cacheKey := merchantID + ":" + orderID

	// Tier 1: in-process LRU.
	if r.urlCache != nil {
		if url, ok := r.urlCache.Get(cacheKey); ok {
			middleware.CheckoutResolveResult.WithLabelValues("cached_local").Inc()
			return url, nil
		}
	}

	// Resolve the merchant's logical shard so all transactions-table calls
	// in this request route to the right physical pool. Cached LRU hit on
	// the hot path; full DB read on cold start.
	merchant, err := r.merchants.GetByMerchantID(ctx, merchantID)
	if err != nil {
		if errors.Is(err, repository.ErrMerchantNotFound) {
			middleware.CheckoutResolveResult.WithLabelValues("not_ready").Inc()
			return "", ErrOrderNotReady
		}
		middleware.CheckoutResolveResult.WithLabelValues("failed").Inc()
		return "", fmt.Errorf("load merchant: %w", err)
	}
	shardIdx := merchant.ShardIndex

	// Tier 2: MySQL row. If the row has a stripe_session_id, reconstruct
	// the URL deterministically — no Stripe call needed.
	t0 := time.Now()
	order, err := r.repo.GetByMerchantOrderID(ctx, shardIdx, merchantID, orderID)
	middleware.CheckoutResolveDuration.WithLabelValues("db_lookup").Observe(time.Since(t0).Seconds())
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			middleware.CheckoutResolveResult.WithLabelValues("not_ready").Inc()
			return "", ErrOrderNotReady
		}
		middleware.CheckoutResolveResult.WithLabelValues("failed").Inc()
		return "", fmt.Errorf("load order: %w", err)
	}
	if order.StripeSessionID != "" {
		url := stripeCheckoutURL(order.StripeSessionID)
		r.cacheURL(cacheKey, url)
		middleware.CheckoutResolveResult.WithLabelValues("cached_db").Inc()
		return url, nil
	}

	// Tier 3 (lazy mode): order exists but Stripe session not yet created.
	// Acquire a per-order lock so concurrent first-clicks don't all hit
	// Stripe.
	t0 = time.Now()
	lock, err := r.locker.Acquire(ctx, "checkout:"+cacheKey, 10*time.Second)
	middleware.CheckoutResolveDuration.WithLabelValues("lock_acquire").Observe(time.Since(t0).Seconds())
	if err != nil {
		// Someone else is creating it — quick retry on the read path.
		time.Sleep(150 * time.Millisecond)
		return r.resolveAfterLock(ctx, shardIdx, merchantID, orderID, cacheKey)
	}
	defer func() { _ = lock.Release(ctx) }()

	// Re-check under lock — another pod may have just persisted the session.
	if cached, getErr := r.repo.GetByMerchantOrderID(ctx, shardIdx, merchantID, orderID); getErr == nil && cached != nil && cached.StripeSessionID != "" {
		url := stripeCheckoutURL(cached.StripeSessionID)
		r.cacheURL(cacheKey, url)
		return url, nil
	}

	// Tier 4: rate limit Stripe calls across the fleet.
	if r.bucket != nil {
		err := r.bucket.Allow(ctx)
		switch {
		case err == nil:
			// allowed
		case errors.Is(err, cache.ErrRateLimited):
			middleware.StripeRateLimited.Inc()
			middleware.CheckoutResolveResult.WithLabelValues("rate_limited").Inc()
			return "", ErrUnavailable
		default:
			// Redis-level error — fail open. Stripe SDK has its own client-side
			// retries; better to over-call than to brick checkout.
			logger.L().Warn("token bucket failed, allowing through", "err", err)
		}
	}

	// Tier 5: Stripe call (already wrapped by circuit breaker).
	t0 = time.Now()
	session, err := r.createSession(ctx, order)
	middleware.CheckoutResolveDuration.WithLabelValues("stripe_create").Observe(time.Since(t0).Seconds())
	if err != nil {
		if errors.Is(err, stripe.ErrCircuitOpen) {
			middleware.CheckoutResolveResult.WithLabelValues("breaker_open").Inc()
			return "", ErrUnavailable
		}
		middleware.CheckoutResolveResult.WithLabelValues("failed").Inc()
		return "", err
	}

	// Persist + warm cache. session.URL is what Stripe returned; we store
	// the session_id (not the URL) and reconstruct on subsequent reads.
	if err := r.repo.UpdateCheckout(ctx, shardIdx, merchantID, orderID, session.ID, session.PaymentIntentID); err != nil {
		logger.L().Warn("update checkout persist failed", "merchant_id", merchantID, "order_id", orderID, "err", err)
	}
	r.cacheURL(cacheKey, session.URL)
	middleware.CheckoutResolveResult.WithLabelValues("created").Inc()
	return session.URL, nil
}

func (r *checkoutResolver) cacheURL(cacheKey, url string) {
	if r.urlCache != nil {
		r.urlCache.Put(cacheKey, url)
	}
}

func (r *checkoutResolver) resolveAfterLock(ctx context.Context, shardIdx uint8, merchantID, orderID, cacheKey string) (string, error) {
	order, err := r.repo.GetByMerchantOrderID(ctx, shardIdx, merchantID, orderID)
	if err == nil && order != nil && order.StripeSessionID != "" {
		url := stripeCheckoutURL(order.StripeSessionID)
		r.cacheURL(cacheKey, url)
		return url, nil
	}
	return "", ErrOrderNotReady
}

func (r *checkoutResolver) createSession(ctx context.Context, order *domain.Order) (*stripe.CheckoutSession, error) {
	if order == nil {
		return nil, ErrOrderNotReady
	}
	req := stripe.CreateCheckoutRequest{
		Amount:             order.Amount,
		Currency:           strings.ToLower(order.Currency),
		PaymentMethodTypes: []string{"card"},
		ClientReferenceID:  order.OrderID,
		Metadata: map[string]string{
			"order_id":    order.OrderID,
			"merchant_id": order.MerchantID,
		},
	}
	// Stripe Checkout Sessions in `mode=payment` REQUIRE success_url. Fall
	// back to gateway defaults when the merchant didn't supply one.
	if req.SuccessURL == "" {
		req.SuccessURL = withOrderQuery(r.defaultSuccessURL, req.ClientReferenceID)
	}
	if req.CancelURL == "" && r.defaultCancelURL != "" {
		req.CancelURL = withOrderQuery(r.defaultCancelURL, req.ClientReferenceID)
	}
	// Idempotency-Key includes merchant_id since order_id is merchant-scoped.
	idemKey := "checkout:" + order.MerchantID + ":" + req.ClientReferenceID
	return r.stripe.CreateCheckoutSession(ctx, req, idemKey)
}

func withOrderQuery(base, orderID string) string {
	if base == "" {
		return ""
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "order_id=" + orderID
}

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/quangdangfit/easypay/internal/cache"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
	"github.com/quangdangfit/easypay/internal/repository"
)

// CheckoutResolver lazily creates a Stripe Checkout Session the first time a
// user opens the public hosted checkout URL. Subsequent hits return the
// cached session URL. A distributed lock ensures we don't create N Stripe
// sessions for N concurrent clicks on the same order.
type CheckoutResolver struct {
	stripe  stripe.Client
	repo    repository.OrderRepository
	pending cache.PendingOrderStore
	locker  *cache.Locker
}

func NewCheckoutResolver(s stripe.Client, repo repository.OrderRepository, pending cache.PendingOrderStore, locker *cache.Locker) *CheckoutResolver {
	return &CheckoutResolver{stripe: s, repo: repo, pending: pending, locker: locker}
}

var ErrOrderNotReady = errors.New("order not yet available")

// Resolve returns the Stripe Checkout URL for an order. If a session has
// already been created (either previously by Resolve or by an eager flow),
// the cached URL is returned. Otherwise a new session is minted via Stripe
// and persisted back to the order.
func (r *CheckoutResolver) Resolve(ctx context.Context, orderID string) (string, error) {
	// 1. Try the canonical store first.
	order, err := r.repo.GetByOrderID(ctx, orderID)
	if err == nil && order.CheckoutURL != "" && order.StripeSessionID != "" {
		return order.CheckoutURL, nil
	}
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return "", fmt.Errorf("load order: %w", err)
	}

	// 2. Fall back to the Redis snapshot if the consumer hasn't committed yet.
	var snap *cache.PendingOrder
	if order == nil {
		snap, err = r.pending.Get(ctx, orderID)
		if err != nil {
			return "", ErrOrderNotReady
		}
	}

	// 3. Distributed lock so concurrent clicks don't create duplicate sessions.
	lock, err := r.locker.Acquire(ctx, "checkout:"+orderID, 10*time.Second)
	if err != nil {
		// Someone else is creating it — short retry on the read path.
		time.Sleep(100 * time.Millisecond)
		return r.resolveAfterLock(ctx, orderID)
	}
	defer lock.Release(ctx)

	// Re-check after lock to avoid duplicate creation racing.
	if order != nil {
		if order.CheckoutURL != "" {
			return order.CheckoutURL, nil
		}
	} else if cached, _ := r.repo.GetByOrderID(ctx, orderID); cached != nil && cached.CheckoutURL != "" {
		return cached.CheckoutURL, nil
	}

	// 4. Create the Stripe session — idempotency key = order_id so multiple
	//    pods racing in across the lock TTL still converge to one session.
	session, err := r.createSession(ctx, order, snap)
	if err != nil {
		return "", err
	}

	// 5. Best-effort persist back to MySQL. If the row doesn't exist yet
	//    (consumer lag), we'll persist on a later read or via the webhook.
	if order != nil {
		_ = r.repo.UpdateCheckout(ctx, orderID, session.ID, session.PaymentIntentID, session.URL)
	}
	return session.URL, nil
}

func (r *CheckoutResolver) resolveAfterLock(ctx context.Context, orderID string) (string, error) {
	order, err := r.repo.GetByOrderID(ctx, orderID)
	if err == nil && order.CheckoutURL != "" {
		return order.CheckoutURL, nil
	}
	return "", ErrOrderNotReady
}

func (r *CheckoutResolver) createSession(ctx context.Context, order *domain.Order, snap *cache.PendingOrder) (*stripe.CheckoutSession, error) {
	var req stripe.CreateCheckoutRequest
	var merchantID string
	switch {
	case order != nil:
		req = stripe.CreateCheckoutRequest{
			Amount:             order.Amount,
			Currency:           strings.ToLower(order.Currency),
			PaymentMethodTypes: []string{"card"},
			SuccessURL:         "",
			CancelURL:          "",
			ClientReferenceID:  order.OrderID,
			Metadata: map[string]string{
				"order_id":    order.OrderID,
				"merchant_id": order.MerchantID,
			},
		}
		merchantID = order.MerchantID
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
		merchantID = snap.MerchantID
	default:
		return nil, ErrOrderNotReady
	}
	session, err := r.stripe.CreateCheckoutSession(ctx, req, "checkout:"+req.ClientReferenceID)
	if err != nil {
		return nil, fmt.Errorf("stripe create session for merchant %s: %w", merchantID, err)
	}
	return session, nil
}

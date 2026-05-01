package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/quangdangfit/easypay/internal/cache"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/pkg/checkouttoken"
)

var (
	ErrInvalidRequest    = errors.New("invalid request")
	ErrUnsupportedMethod = errors.New("unsupported payment method")
	// ErrTransactionConflict is returned when a (merchant_id, transaction_id)
	// already exists with a different amount, currency, or method. The merchant
	// is reusing the same idempotency key for a materially different payment,
	// which we refuse rather than silently override or duplicate.
	ErrTransactionConflict = errors.New("transaction_id conflicts with existing payment")
)

// CreatePaymentInput is what the HTTP handler hands to the service.
type CreatePaymentInput struct {
	Merchant           *domain.Merchant
	TransactionID      string
	Amount             int64
	Currency           string
	PaymentMethodTypes []string
	CustomerEmail      string
	SuccessURL         string
	CancelURL          string
	CallbackURL        string
	// Method = "crypto" routes to the blockchain checkout flow; otherwise Stripe.
	Method string
}

// CreatePaymentResult is what we return to the merchant.
type CreatePaymentResult struct {
	OrderID               string `json:"order_id"`
	TransactionID         string `json:"transaction_id"`
	StripeSessionID       string `json:"stripe_session_id,omitempty"`
	CheckoutURL           string `json:"checkout_url,omitempty"`
	StripePaymentIntentID string `json:"stripe_payment_intent_id,omitempty"`
	ClientSecret          string `json:"client_secret,omitempty"`
	Status                string `json:"status"`
	// Crypto path
	CryptoPayload *CryptoPayload `json:"crypto,omitempty"`
}

type CryptoPayload struct {
	ContractAddress string `json:"contract_address"`
	OrderID         string `json:"order_id"`
	Amount          int64  `json:"amount"`
	ChainID         int64  `json:"chain_id"`
}

// paymentService implements Payments.
type paymentService struct {
	idem      cache.IdempotencyChecker
	stripe    stripe.Client
	publisher kafka.EventPublisher
	pending   cache.PendingOrderStore
	repo      repository.OrderRepository
	currency  string
	// Crypto contract details
	cryptoContract string
	cryptoChainID  int64
	// Lazy checkout: if true, POST /api/payments returns a self-hosted URL
	// and the Stripe Session is created on first hit of /pay/:id. Lets the
	// merchant API scale beyond Stripe's per-account rate limits.
	lazyCheckout      bool
	publicBaseURL     string
	checkoutSecret    string        // signs /pay/:id?t=<token> URLs
	checkoutTokenTTL  time.Duration // how long a hosted-checkout URL is valid
	defaultSuccessURL string
	defaultCancelURL  string
	// orderIDSecret enables deterministic order_id derivation. See
	// PaymentServiceOptions.OrderIDSecret for the rationale.
	orderIDSecret string
}

type PaymentServiceOptions struct {
	DefaultCurrency   string
	CryptoContract    string
	CryptoChainID     int64
	LazyCheckout      bool
	PublicBaseURL     string
	CheckoutSecret    string
	CheckoutTokenTTL  time.Duration
	DefaultSuccessURL string
	DefaultCancelURL  string
	// OrderIDSecret, when non-empty, switches order_id generation from
	// random UUIDs to deterministic HMAC-SHA256(secret, merchant:txn).
	// Required for serializable concurrent-create semantics.
	OrderIDSecret string
}

func NewPaymentService(
	idem cache.IdempotencyChecker,
	stripeC stripe.Client,
	publisher kafka.EventPublisher,
	pending cache.PendingOrderStore,
	repo repository.OrderRepository,
	opts PaymentServiceOptions,
) Payments {
	ttl := opts.CheckoutTokenTTL
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	return &paymentService{
		idem:              idem,
		stripe:            stripeC,
		publisher:         publisher,
		pending:           pending,
		repo:              repo,
		currency:          opts.DefaultCurrency,
		cryptoContract:    opts.CryptoContract,
		cryptoChainID:     opts.CryptoChainID,
		lazyCheckout:      opts.LazyCheckout,
		publicBaseURL:     opts.PublicBaseURL,
		checkoutSecret:    opts.CheckoutSecret,
		checkoutTokenTTL:  ttl,
		defaultSuccessURL: opts.DefaultSuccessURL,
		defaultCancelURL:  opts.DefaultCancelURL,
		orderIDSecret:     opts.OrderIDSecret,
	}
}

func (s *paymentService) Create(ctx context.Context, in CreatePaymentInput) (*CreatePaymentResult, error) {
	if in.Merchant == nil {
		return nil, fmt.Errorf("%w: merchant required", ErrInvalidRequest)
	}
	if strings.TrimSpace(in.TransactionID) == "" {
		return nil, fmt.Errorf("%w: transaction_id required", ErrInvalidRequest)
	}
	if in.Amount <= 0 {
		return nil, fmt.Errorf("%w: amount must be > 0", ErrInvalidRequest)
	}
	if in.Currency == "" {
		in.Currency = s.currency
	}

	idemKey := in.Merchant.MerchantID + ":" + in.TransactionID
	normalizedCurrency := strings.ToUpper(in.Currency)
	requestedMethod := primaryMethod(in)

	// 1a. Hot-path idempotency: bloom + redis cached response (TTL 24h).
	if exists, cached, err := s.idem.Check(ctx, idemKey); err == nil && exists && cached != nil {
		var prev CreatePaymentResult
		if jsonErr := json.Unmarshal(cached, &prev); jsonErr == nil {
			return &prev, nil
		}
	}

	// 1b. Cold-path idempotency: MySQL by (merchant_id, transaction_id).
	// Covers the case where the Redis idem TTL has expired (e.g. merchant
	// resends the same transaction_id months later) — without this, we'd
	// generate a fresh order_id for an already-paid transaction, return a
	// checkout_url whose row never persists (UNIQUE violation at consumer),
	// and silently lose the payment when Stripe webhook arrives.
	if s.repo != nil {
		existing, err := s.repo.GetByMerchantTransaction(ctx, in.Merchant.MerchantID, in.TransactionID)
		switch {
		case err == nil && existing != nil:
			if !sameMaterialPayment(existing, in.Amount, normalizedCurrency, requestedMethod) {
				return nil, fmt.Errorf("%w: amount/currency/method mismatch with order %s",
					ErrTransactionConflict, existing.OrderID)
			}
			result := reconstructResult(existing, s.cryptoContract, s.cryptoChainID)
			// Re-warm Redis idem so subsequent retries within 24h hit fast path.
			if cached, mErr := json.Marshal(result); mErr == nil {
				_ = s.idem.Set(ctx, idemKey, cached, 24*time.Hour)
			}
			return result, nil
		case err != nil && !errors.Is(err, repository.ErrNotFound):
			return nil, fmt.Errorf("idem lookup: %w", err)
		}
	}

	orderID := s.deriveOrderID(in.Merchant.MerchantID, in.TransactionID)
	result := &CreatePaymentResult{
		OrderID:       orderID,
		TransactionID: in.TransactionID,
		Status:        "accepted",
	}

	switch {
	case strings.EqualFold(in.Method, "crypto"):
		result.CryptoPayload = &CryptoPayload{
			ContractAddress: s.cryptoContract,
			OrderID:         orderID,
			Amount:          in.Amount,
			ChainID:         s.cryptoChainID,
		}

	case s.lazyCheckout:
		// Lazy: skip Stripe entirely. The first hit on /pay/:id will create
		// the session. Stash a snapshot in Redis so the public handler can
		// resolve the order even before the consumer commits to MySQL.
		token := ""
		if s.checkoutSecret != "" {
			token = checkouttoken.Sign(s.checkoutSecret, orderID, s.checkoutTokenTTL)
		}
		if token != "" {
			result.CheckoutURL = s.publicBaseURL + "/pay/" + orderID + "?t=" + token
		} else {
			result.CheckoutURL = s.publicBaseURL + "/pay/" + orderID
		}
		if s.pending != nil {
			_ = s.pending.Put(ctx, &cache.PendingOrder{
				OrderID:       orderID,
				MerchantID:    in.Merchant.MerchantID,
				TransactionID: in.TransactionID,
				Amount:        in.Amount,
				Currency:      strings.ToUpper(in.Currency),
				PaymentMethod: primaryMethod(in),
				CustomerEmail: in.CustomerEmail,
				SuccessURL:    in.SuccessURL,
				CancelURL:     in.CancelURL,
				CreatedAt:     time.Now().UTC().Unix(),
			}, 24*time.Hour)
		}

	default:
		// Eager: synchronous Stripe Checkout Session creation.
		successURL := in.SuccessURL
		if successURL == "" {
			successURL = s.defaultSuccessURL
		}
		cancelURL := in.CancelURL
		if cancelURL == "" {
			cancelURL = s.defaultCancelURL
		}
		req := stripe.CreateCheckoutRequest{
			Amount:             in.Amount,
			Currency:           strings.ToLower(in.Currency),
			PaymentMethodTypes: defaultIfEmpty(in.PaymentMethodTypes, []string{"card"}),
			CustomerEmail:      in.CustomerEmail,
			SuccessURL:         successURL,
			CancelURL:          cancelURL,
			Metadata: map[string]string{
				"order_id":    orderID,
				"merchant_id": in.Merchant.MerchantID,
			},
			ClientReferenceID: orderID,
		}
		session, err := s.stripe.CreateCheckoutSession(ctx, req, idemKey)
		if err != nil {
			return nil, fmt.Errorf("stripe checkout: %w", err)
		}
		result.StripeSessionID = session.ID
		result.CheckoutURL = session.URL
		result.StripePaymentIntentID = session.PaymentIntentID
		result.ClientSecret = session.ClientSecret
	}

	// 3 (post-Stripe). Persist idempotency key + cached response.
	cached, _ := json.Marshal(result)
	_ = s.idem.Set(ctx, idemKey, cached, 24*time.Hour)

	// 5. Publish payment.events for the async consumer to batch-insert.
	//
	// We persist whatever URL was returned to the merchant — Stripe's URL in
	// eager mode, our /pay/:id URL in lazy mode. The resolver protects
	// against redirect loops by requiring `stripe_session_id` to be set
	// alongside `checkout_url` before treating the row as a cache hit.
	event := kafka.PaymentEvent{
		OrderID:               orderID,
		MerchantID:            in.Merchant.MerchantID,
		TransactionID:         in.TransactionID,
		Amount:                in.Amount,
		Currency:              strings.ToUpper(in.Currency),
		PaymentMethod:         primaryMethod(in),
		Status:                string(domain.OrderStatusPending),
		StripeSessionID:       result.StripeSessionID,
		StripePaymentIntentID: result.StripePaymentIntentID,
		CheckoutURL:           result.CheckoutURL,
		CallbackURL:           in.CallbackURL,
		CreatedAt:             time.Now().UTC().Unix(),
	}
	if err := s.publisher.PublishPaymentEvent(ctx, event); err != nil {
		return nil, fmt.Errorf("publish event: %w", err)
	}

	return result, nil
}

func primaryMethod(in CreatePaymentInput) string {
	if strings.EqualFold(in.Method, "crypto") {
		return "crypto_eth"
	}
	if len(in.PaymentMethodTypes) > 0 {
		return in.PaymentMethodTypes[0]
	}
	return "card"
}

func defaultIfEmpty(v, def []string) []string {
	if len(v) == 0 {
		return def
	}
	return v
}

func generateOrderID() string {
	return "ord-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
}

// deriveOrderID returns a stable order_id for a given (merchant_id,
// transaction_id) pair when an HMAC secret is configured. Two concurrent
// Create calls with the same idempotency key produce the same order_id, so
// Redis SETNX, Stripe's Idempotency-Key (`merchant_id:transaction_id`), and
// the MySQL uniq_merchant_txn index all collapse the duplicate writes onto
// the same identity — eliminating the orphan-row race that random UUIDs
// allow when Redis idem and MySQL both miss in the gap before the row is
// committed. When no secret is set, falls back to random UUIDs.
func (s *paymentService) deriveOrderID(merchantID, transactionID string) string {
	if s.orderIDSecret == "" {
		return generateOrderID()
	}
	h := hmac.New(sha256.New, []byte(s.orderIDSecret))
	h.Write([]byte(merchantID))
	h.Write([]byte{':'})
	h.Write([]byte(transactionID))
	// 12 bytes → 24 hex chars, matches the legacy random format width and
	// gives 96 bits of effective entropy under the secret.
	return "ord-" + hex.EncodeToString(h.Sum(nil)[:12])
}

// sameMaterialPayment returns true iff the persisted order matches the
// incoming request on the fields that define a payment's identity. We
// intentionally do NOT compare cosmetic fields (success_url, cancel_url,
// customer_email) — those may legitimately drift across retries.
func sameMaterialPayment(existing *domain.Order, amount int64, currency, method string) bool {
	if existing.Amount != amount {
		return false
	}
	if !strings.EqualFold(existing.Currency, currency) {
		return false
	}
	// Method comparison: only flag when the bucket changes (crypto vs Stripe).
	// Different Stripe payment_method_types within the card family are fine.
	wasCrypto := strings.HasPrefix(existing.PaymentMethod, "crypto_")
	wantCrypto := strings.HasPrefix(method, "crypto_")
	return wasCrypto == wantCrypto
}

// reconstructResult rebuilds the response we originally returned to the
// merchant from the persisted order. ClientSecret cannot be recovered (Stripe
// only returns it at session creation), so embedded-checkout retries past
// the Redis TTL must fall back to the hosted URL.
func reconstructResult(o *domain.Order, cryptoContract string, cryptoChainID int64) *CreatePaymentResult {
	res := &CreatePaymentResult{
		OrderID:               o.OrderID,
		TransactionID:         o.TransactionID,
		StripeSessionID:       o.StripeSessionID,
		CheckoutURL:           o.CheckoutURL,
		StripePaymentIntentID: o.StripePaymentIntentID,
		Status:                "accepted",
	}
	if strings.HasPrefix(o.PaymentMethod, "crypto_") {
		res.CryptoPayload = &CryptoPayload{
			ContractAddress: cryptoContract,
			OrderID:         o.OrderID,
			Amount:          o.Amount,
			ChainID:         cryptoChainID,
		}
	}
	return res
}

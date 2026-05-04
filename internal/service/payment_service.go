package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/pkg/checkouttoken"
)

var (
	ErrInvalidRequest    = errors.New("invalid request")
	ErrUnsupportedMethod = errors.New("unsupported payment method")
	// ErrTransactionConflict is returned when a (merchant_id, order_id)
	// already exists with a different amount, currency, or method. The
	// merchant is reusing the same idempotency key for a materially
	// different payment, which we refuse rather than silently override.
	ErrTransactionConflict = errors.New("order_id conflicts with existing payment")
)

// CreatePaymentInput is what the HTTP handler hands to the service.
//
// OrderID is the merchant's own idempotency key (their order/cart id).
// The service derives a 16-byte transaction_id from
// (merchant_id, order_id) using SHA-256, then INSERTs the row before
// returning. Two retries with the same OrderID always collapse to the
// same row via the MySQL UNIQUE constraint.
type CreatePaymentInput struct {
	Merchant           *domain.Merchant
	OrderID            string
	Amount             int64
	Currency           string
	PaymentMethodTypes []string
	CustomerEmail      string
	SuccessURL         string
	CancelURL          string
	// Method = "crypto" routes to the blockchain checkout flow; otherwise Stripe.
	Method string
}

// CreatePaymentResult is what we return to the merchant.
//
// CheckoutURL is reconstructed at response time from the persisted state
// (Stripe session_id for eager mode, signed token URL for lazy mode).
type CreatePaymentResult struct {
	OrderID               string         `json:"order_id"`
	TransactionID         string         `json:"transaction_id"`
	StripeSessionID       string         `json:"stripe_session_id,omitempty"`
	CheckoutURL           string         `json:"checkout_url,omitempty"`
	StripePaymentIntentID string         `json:"stripe_payment_intent_id,omitempty"`
	ClientSecret          string         `json:"client_secret,omitempty"`
	Status                string         `json:"status"`
	CryptoPayload         *CryptoPayload `json:"crypto,omitempty"`
}

type CryptoPayload struct {
	ContractAddress string `json:"contract_address"`
	OrderID         string `json:"order_id"`
	Amount          int64  `json:"amount"`
	ChainID         int64  `json:"chain_id"`
}

// paymentService implements Payments. The write path is a sync MySQL INSERT —
// no Redis idempotency cache, no pending snapshot, no Kafka fan-out. The
// MySQL UNIQUE on (merchant_id, transaction_id) is the single dedupe layer.
type paymentService struct {
	stripe stripe.Client
	repo   repository.TransactionRepository

	currency       string
	cryptoContract string
	cryptoChainID  int64

	// Lazy checkout: if true, POST /api/payments returns a self-hosted URL
	// and the Stripe Session is created on first hit of /pay/:id. Lets the
	// merchant API scale beyond Stripe's per-account rate limits.
	lazyCheckout     bool
	publicBaseURL    string
	checkoutSecret   string        // signs /pay/:id?t=<token> URLs
	checkoutTokenTTL time.Duration // how long a hosted-checkout URL is valid

	defaultSuccessURL string
	defaultCancelURL  string
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
}

func NewPaymentService(stripeC stripe.Client, repo repository.TransactionRepository, opts PaymentServiceOptions) Payments {
	ttl := opts.CheckoutTokenTTL
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	return &paymentService{
		stripe:            stripeC,
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
	}
}

func (s *paymentService) Create(ctx context.Context, in CreatePaymentInput) (*CreatePaymentResult, error) {
	if in.Merchant == nil {
		return nil, fmt.Errorf("%w: merchant required", ErrInvalidRequest)
	}
	if err := domain.ValidateOrderID(in.OrderID); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if in.Amount <= 0 {
		return nil, fmt.Errorf("%w: amount must be > 0", ErrInvalidRequest)
	}
	if in.Currency == "" {
		in.Currency = s.currency
	}
	normalizedCurrency := strings.ToUpper(in.Currency)
	requestedMethod := primaryMethod(in)

	txnID := DeriveTransactionID(in.Merchant.MerchantID, in.OrderID)

	row := &domain.Transaction{
		MerchantID:    in.Merchant.MerchantID,
		TransactionID: txnID,
		OrderID:       in.OrderID,
		Amount:        in.Amount,
		Currency:      normalizedCurrency,
		Status:        domain.TransactionStatusCreated,
		PaymentMethod: requestedMethod,
		ShardIndex:    in.Merchant.ShardIndex,
	}
	if err := s.repo.Insert(ctx, row); err != nil {
		if !errors.Is(err, repository.ErrDuplicateTransaction) {
			return nil, fmt.Errorf("insert order: %w", err)
		}
		// Loser of the (merchant_id, transaction_id) UNIQUE race. Find the
		// winner and return its response. Material-mismatch is a 409.
		existing, gErr := s.repo.GetByTransactionID(ctx, in.Merchant.ShardIndex, in.Merchant.MerchantID, txnID)
		if gErr != nil {
			return nil, fmt.Errorf("idem lookup: %w", gErr)
		}
		if !sameMaterialPayment(existing, in.Amount, normalizedCurrency, requestedMethod) {
			return nil, fmt.Errorf("%w: amount/currency/method mismatch with order %s",
				ErrTransactionConflict, existing.OrderID)
		}
		return s.reconstructResult(in.Merchant.MerchantID, existing), nil
	}

	// Successful insert: now do the chosen flow. Order is already durable;
	// any failure below leaves a `created`-state row that the reconciliation
	// cron will clean up.
	result := &CreatePaymentResult{
		OrderID:       in.OrderID,
		TransactionID: txnID,
		Status:        "accepted",
	}

	switch {
	case strings.EqualFold(in.Method, "crypto"):
		result.CryptoPayload = &CryptoPayload{
			ContractAddress: s.cryptoContract,
			OrderID:         in.OrderID,
			Amount:          in.Amount,
			ChainID:         s.cryptoChainID,
		}

	case s.lazyCheckout:
		result.CheckoutURL = s.lazyCheckoutURL(in.Merchant.MerchantID, in.OrderID)

	default:
		// Eager: synchronous Stripe Checkout Session creation.
		successURL := orFallback(in.SuccessURL, s.defaultSuccessURL)
		cancelURL := orFallback(in.CancelURL, s.defaultCancelURL)
		req := stripe.CreateCheckoutRequest{
			Amount:             in.Amount,
			Currency:           strings.ToLower(normalizedCurrency),
			PaymentMethodTypes: defaultIfEmpty(in.PaymentMethodTypes, []string{"card"}),
			CustomerEmail:      in.CustomerEmail,
			SuccessURL:         successURL,
			CancelURL:          cancelURL,
			Metadata: map[string]string{
				"order_id":    in.OrderID,
				"merchant_id": in.Merchant.MerchantID,
			},
			ClientReferenceID: in.OrderID,
		}
		// Stripe Idempotency-Key = transaction_id hex (32 chars). Same key
		// across all retries collapses on Stripe's side too.
		session, err := s.stripe.CreateCheckoutSession(ctx, req, txnID)
		if err != nil {
			return nil, fmt.Errorf("stripe checkout: %w", err)
		}
		result.StripeSessionID = session.ID
		result.CheckoutURL = session.URL
		result.StripePaymentIntentID = session.PaymentIntentID
		result.ClientSecret = session.ClientSecret
		// Persist Stripe artefacts so future retries / lookups can
		// reconstruct the URL without another Stripe call.
		if err := s.repo.UpdateCheckout(ctx, in.Merchant.ShardIndex, in.Merchant.MerchantID, in.OrderID, session.ID, session.PaymentIntentID); err != nil {
			return nil, fmt.Errorf("persist stripe session: %w", err)
		}
	}

	return result, nil
}

// DeriveTransactionID returns the deterministic 32-char hex transaction_id
// for a (merchant_id, order_id) pair. Two retries with the same input
// always derive the same id, which makes the MySQL UNIQUE on
// (merchant_id, transaction_id) collapse retries onto a single row.
//
// Note: this is a public hash, NOT a MAC — knowing the merchant id and
// order id is sufficient to compute the transaction id. That is by design:
// the transaction id is internal-but-not-secret, and the merchant already
// knows both inputs.
func DeriveTransactionID(merchantID, orderID string) string {
	h := sha256.New()
	h.Write([]byte(merchantID))
	h.Write([]byte{':'})
	h.Write([]byte(orderID))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// lazyCheckoutURL returns the self-hosted /pay/:merchant_id/:order_id URL
// we hand back in lazy-checkout mode. The actual Stripe Session is created
// the first time the URL is opened (see CheckoutResolver). The signed
// token binds the URL to (merchant_id, order_id) so attackers can't
// enumerate orders.
func (s *paymentService) lazyCheckoutURL(merchantID, orderID string) string {
	base := s.publicBaseURL + "/pay/" + merchantID + "/" + orderID
	if s.checkoutSecret == "" {
		return base
	}
	token := checkouttoken.Sign(s.checkoutSecret, merchantID, orderID, s.checkoutTokenTTL)
	if token == "" {
		return base
	}
	return base + "?t=" + token
}

// reconstructResult builds the response for an idempotent retry by reading
// the persisted order. ClientSecret cannot be recovered (Stripe only emits
// it at session creation), so embedded-checkout retries fall back to the
// hosted URL.
func (s *paymentService) reconstructResult(merchantID string, o *domain.Transaction) *CreatePaymentResult {
	res := &CreatePaymentResult{
		OrderID:               o.OrderID,
		TransactionID:         o.TransactionID,
		StripeSessionID:       o.StripeSessionID,
		StripePaymentIntentID: o.StripePaymentIntentID,
		Status:                "accepted",
	}
	switch {
	case strings.HasPrefix(o.PaymentMethod, "crypto"):
		res.CryptoPayload = &CryptoPayload{
			ContractAddress: s.cryptoContract,
			OrderID:         o.OrderID,
			Amount:          o.Amount,
			ChainID:         s.cryptoChainID,
		}
	case o.StripeSessionID != "":
		// Stripe Checkout URLs are deterministic from session_id. Avoids a
		// dropped column and a second GET round-trip.
		res.CheckoutURL = "https://checkout.stripe.com/c/pay/" + o.StripeSessionID
	default:
		// Lazy mode order that hasn't been opened yet — re-mint the token.
		res.CheckoutURL = s.lazyCheckoutURL(merchantID, o.OrderID)
	}
	return res
}

// sameMaterialPayment returns true iff the persisted order matches the
// incoming request on the fields that define a payment's identity. We
// intentionally do NOT compare cosmetic fields (success_url, cancel_url,
// customer_email) — those may legitimately drift across retries.
func sameMaterialPayment(existing *domain.Transaction, amount int64, currency, method string) bool {
	if existing.Amount != amount {
		return false
	}
	if !strings.EqualFold(existing.Currency, currency) {
		return false
	}
	// Only flag when the bucket changes (crypto vs Stripe). Different
	// Stripe payment_method_types within the card family are fine.
	wasCrypto := strings.HasPrefix(existing.PaymentMethod, "crypto")
	wantCrypto := strings.HasPrefix(method, "crypto")
	return wasCrypto == wantCrypto
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

func orFallback(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

package service

import (
	"context"
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
)

var (
	ErrInvalidRequest   = errors.New("invalid request")
	ErrUnsupportedMethod = errors.New("unsupported payment method")
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

type PaymentService struct {
	idem     cache.IdempotencyChecker
	stripe   stripe.Client
	publisher kafka.EventPublisher
	currency string
	// Crypto contract details
	cryptoContract string
	cryptoChainID  int64
}

func NewPaymentService(
	idem cache.IdempotencyChecker,
	stripeC stripe.Client,
	publisher kafka.EventPublisher,
	defaultCurrency string,
	cryptoContract string,
	cryptoChainID int64,
) *PaymentService {
	return &PaymentService{
		idem:           idem,
		stripe:         stripeC,
		publisher:      publisher,
		currency:       defaultCurrency,
		cryptoContract: cryptoContract,
		cryptoChainID:  cryptoChainID,
	}
}

func (s *PaymentService) Create(ctx context.Context, in CreatePaymentInput) (*CreatePaymentResult, error) {
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

	// 1. Idempotency: bloom + redis cached response.
	if exists, cached, err := s.idem.Check(ctx, idemKey); err == nil && exists && cached != nil {
		var prev CreatePaymentResult
		if jsonErr := json.Unmarshal(cached, &prev); jsonErr == nil {
			return &prev, nil
		}
	}

	orderID := generateOrderID()
	result := &CreatePaymentResult{
		OrderID:       orderID,
		TransactionID: in.TransactionID,
		Status:        "accepted",
	}

	if strings.EqualFold(in.Method, "crypto") {
		result.CryptoPayload = &CryptoPayload{
			ContractAddress: s.cryptoContract,
			OrderID:         orderID,
			Amount:          in.Amount,
			ChainID:         s.cryptoChainID,
		}
	} else {
		// 4. Call Stripe Checkout Session.
		req := stripe.CreateCheckoutRequest{
			Amount:             in.Amount,
			Currency:           strings.ToLower(in.Currency),
			PaymentMethodTypes: defaultIfEmpty(in.PaymentMethodTypes, []string{"card"}),
			CustomerEmail:      in.CustomerEmail,
			SuccessURL:         in.SuccessURL,
			CancelURL:          in.CancelURL,
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
	return "ORD-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
}

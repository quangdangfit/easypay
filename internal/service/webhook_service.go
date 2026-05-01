package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/pkg/logger"
)

var (
	ErrWebhookDuplicate = errors.New("duplicate webhook event")
	// ErrWebhookOrderMissing is a hard error: with sync write, a Stripe
	// webhook for an order_id we don't recognise should never happen unless
	// metadata was tampered with or the row was hand-deleted. We surface it
	// loudly so the caller returns 5xx and Stripe retries (and ops alerts).
	ErrWebhookOrderMissing = errors.New("webhook order_id not found in DB")
)

// webhookService implements Webhooks.
type webhookService struct {
	stripe        stripe.Client
	repo          repository.OrderRepository
	publisher     kafka.EventPublisher
	rc            *redis.Client
	webhookSecret string
}

func NewWebhookService(s stripe.Client, repo repository.OrderRepository, p kafka.EventPublisher, rc *redis.Client, webhookSecret string) Webhooks {
	return &webhookService{
		stripe:        s,
		repo:          repo,
		publisher:     p,
		rc:            rc,
		webhookSecret: webhookSecret,
	}
}

// Process verifies the signature, dedupes by event.id, cross-checks with Stripe
// for the *.succeeded family, then transitions order state and produces the
// confirmation event.
func (s *webhookService) Process(ctx context.Context, payload []byte, sigHeader string) error {
	event, err := s.stripe.VerifyWebhookSignature(payload, sigHeader, s.webhookSecret)
	if err != nil {
		return fmt.Errorf("verify signature: %w", err)
	}

	// Idempotency: SETNX on the Stripe event.id.
	dedupKey := "webhook:" + event.ID
	ok, err := s.rc.SetNX(ctx, dedupKey, "1", 24*time.Hour).Result()
	if err != nil {
		return fmt.Errorf("dedup setnx: %w", err)
	}
	if !ok {
		return ErrWebhookDuplicate
	}

	log := logger.With(ctx).With("event_id", event.ID, "event_type", event.Type)
	log.Info("processing stripe event")

	switch event.Type {
	case "payment_intent.succeeded", "checkout.session.completed":
		return s.handleSucceeded(ctx, event)
	case "payment_intent.payment_failed":
		return s.handleFailed(ctx, event)
	case "charge.refunded":
		return s.handleRefunded(ctx, event)
	case "charge.dispute.created":
		log.Warn("dispute opened — manual review required")
		return nil
	default:
		log.Debug("ignored event type")
		return nil
	}
}

// minimal subset of fields we need from Stripe object payloads.
type stripeObject struct {
	ID            string            `json:"id"`
	Object        string            `json:"object"`
	Amount        int64             `json:"amount"`
	AmountTotal   int64             `json:"amount_total"`
	Currency      string            `json:"currency"`
	Status        string            `json:"status"`
	Metadata      map[string]string `json:"metadata"`
	PaymentIntent string            `json:"payment_intent"`
	LatestCharge  string            `json:"latest_charge"`
	Charge        string            `json:"charge"`
	Refunded      bool              `json:"refunded"`
}

func decodeStripeObject(b []byte) (*stripeObject, error) {
	var o stripeObject
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, fmt.Errorf("decode object: %w", err)
	}
	return &o, nil
}

func (s *webhookService) handleSucceeded(ctx context.Context, e *stripe.Event) error {
	o, err := decodeStripeObject(e.Data)
	if err != nil {
		return err
	}
	orderID := o.Metadata["order_id"]
	if orderID == "" {
		return fmt.Errorf("event %s missing metadata.order_id", e.ID)
	}

	// Cross-check with Stripe — never trust the webhook body alone.
	piID := o.PaymentIntent
	if piID == "" && o.Object == "payment_intent" {
		piID = o.ID
	}
	if piID != "" {
		pi, err := s.stripe.GetPaymentIntent(ctx, piID)
		if err != nil {
			return fmt.Errorf("cross-check payment intent: %w", err)
		}
		if pi.Status != "succeeded" {
			return fmt.Errorf("cross-check: pi %s status=%s, not succeeded", pi.ID, pi.Status)
		}
	}

	if err := s.repo.UpdateStatus(ctx, orderID, domain.OrderStatusPaid, piID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrWebhookOrderMissing, orderID)
		}
		return fmt.Errorf("update order: %w", err)
	}

	order, err := s.repo.GetByOrderID(ctx, orderID)
	if err != nil {
		return fmt.Errorf("load order: %w", err)
	}
	confirmed := kafka.PaymentConfirmedEvent{
		OrderID:               order.OrderID,
		MerchantID:            order.MerchantID,
		Status:                string(domain.OrderStatusPaid),
		StripePaymentIntentID: order.StripePaymentIntentID,
		Amount:                order.Amount,
		Currency:              order.Currency,
		ConfirmedAt:           time.Now().UTC().Unix(),
	}
	return s.publisher.PublishPaymentConfirmed(ctx, confirmed)
}

func (s *webhookService) handleFailed(ctx context.Context, e *stripe.Event) error {
	o, err := decodeStripeObject(e.Data)
	if err != nil {
		return err
	}
	orderID := o.Metadata["order_id"]
	if orderID == "" {
		return fmt.Errorf("event %s missing metadata.order_id", e.ID)
	}
	piID := o.PaymentIntent
	if piID == "" && o.Object == "payment_intent" {
		piID = o.ID
	}
	if err := s.repo.UpdateStatus(ctx, orderID, domain.OrderStatusFailed, piID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrWebhookOrderMissing, orderID)
		}
		return fmt.Errorf("update order failed: %w", err)
	}
	return nil
}

func (s *webhookService) handleRefunded(ctx context.Context, e *stripe.Event) error {
	o, err := decodeStripeObject(e.Data)
	if err != nil {
		return err
	}
	piID := o.PaymentIntent
	if piID == "" {
		// Fall back to looking up by metadata.
		if id, ok := o.Metadata["payment_intent"]; ok {
			piID = id
		}
	}
	orderID := o.Metadata["order_id"]
	if orderID == "" && piID != "" {
		// charge.refunded events sometimes don't carry our metadata; look up
		// the order by payment_intent_id (slow fan-out across shards).
		if order, lookupErr := s.repo.GetByPaymentIntentID(ctx, piID); lookupErr == nil {
			orderID = order.OrderID
		}
	}
	if orderID == "" {
		return fmt.Errorf("event %s missing order_id and unresolvable", e.ID)
	}
	if err := s.repo.UpdateStatus(ctx, orderID, domain.OrderStatusRefunded, piID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrWebhookOrderMissing, orderID)
		}
		return fmt.Errorf("update order refunded: %w", err)
	}
	order, err := s.repo.GetByOrderID(ctx, orderID)
	if err != nil {
		return fmt.Errorf("load order: %w", err)
	}
	confirmed := kafka.PaymentConfirmedEvent{
		OrderID:               order.OrderID,
		MerchantID:            order.MerchantID,
		Status:                string(domain.OrderStatusRefunded),
		StripePaymentIntentID: order.StripePaymentIntentID,
		Amount:                order.Amount,
		Currency:              order.Currency,
		ConfirmedAt:           time.Now().UTC().Unix(),
	}
	return s.publisher.PublishPaymentConfirmed(ctx, confirmed)
}

// CreateRefund issues a Stripe refund and persists the resulting status change.
// Returns the Refund response body the handler should echo back to the merchant.
type RefundInput struct {
	Merchant *domain.Merchant
	OrderID  string
	Amount   int64
	Reason   string
	IdemKey  string
}

type RefundResult struct {
	OrderID  string `json:"order_id"`
	RefundID string `json:"refund_id"`
	Status   string `json:"status"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

func (s *webhookService) CreateRefund(ctx context.Context, in RefundInput) (*RefundResult, error) {
	if in.Merchant == nil || in.OrderID == "" {
		return nil, fmt.Errorf("merchant + order_id required")
	}
	order, err := s.repo.GetByOrderID(ctx, in.OrderID)
	if err != nil {
		return nil, fmt.Errorf("load order: %w", err)
	}
	if order.MerchantID != in.Merchant.MerchantID {
		return nil, fmt.Errorf("order does not belong to merchant")
	}
	if order.StripePaymentIntentID == "" {
		return nil, fmt.Errorf("order has no associated payment intent (crypto refunds not supported)")
	}
	idemKey := in.IdemKey
	if idemKey == "" {
		idemKey = "refund:" + order.OrderID
	}
	r, err := s.stripe.CreateRefund(ctx, stripe.CreateRefundRequest{
		PaymentIntentID: order.StripePaymentIntentID,
		Amount:          in.Amount,
		Reason:          strings.TrimSpace(in.Reason),
		Metadata: map[string]string{
			"order_id":    order.OrderID,
			"merchant_id": order.MerchantID,
		},
	}, idemKey)
	if err != nil {
		return nil, fmt.Errorf("stripe refund: %w", err)
	}
	return &RefundResult{
		OrderID:  order.OrderID,
		RefundID: r.ID,
		Status:   r.Status,
		Amount:   r.Amount,
		Currency: r.Currency,
	}, nil
}

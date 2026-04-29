package stripe

import (
	"context"
	"errors"
	"fmt"

	stripego "github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/checkout/session"
	"github.com/stripe/stripe-go/v76/paymentintent"
	"github.com/stripe/stripe-go/v76/refund"
)

// realClient implements Client using stripe-go SDK v76.
type realClient struct {
	apiKey        string
	webhookSecret string
	apiVersion    string
}

// NewClient configures the SDK and returns a real Stripe client.
// stripe-go uses a process-wide stripe.Key — we set it once here.
// The pinned API version of stripe-go is used as-is; the apiVersion arg is
// kept for documentation and future SDK upgrades.
func NewClient(apiKey, webhookSecret, apiVersion string) Client {
	stripego.Key = apiKey
	return &realClient{apiKey: apiKey, webhookSecret: webhookSecret, apiVersion: apiVersion}
}

func (c *realClient) CreateCheckoutSession(ctx context.Context, req CreateCheckoutRequest, idempotencyKey string) (*CheckoutSession, error) {
	pmTypes := make([]*string, 0, len(req.PaymentMethodTypes))
	for _, t := range req.PaymentMethodTypes {
		pmTypes = append(pmTypes, stripego.String(t))
	}
	params := &stripego.CheckoutSessionParams{
		Mode:               stripego.String(string(stripego.CheckoutSessionModePayment)),
		PaymentMethodTypes: pmTypes,
		LineItems: []*stripego.CheckoutSessionLineItemParams{
			{
				PriceData: &stripego.CheckoutSessionLineItemPriceDataParams{
					Currency:   stripego.String(req.Currency),
					UnitAmount: stripego.Int64(req.Amount),
					ProductData: &stripego.CheckoutSessionLineItemPriceDataProductDataParams{
						Name: stripego.String(fmt.Sprintf("Order %s", req.ClientReferenceID)),
					},
				},
				Quantity: stripego.Int64(1),
			},
		},
		SuccessURL:        nilIfEmpty(req.SuccessURL),
		CancelURL:         nilIfEmpty(req.CancelURL),
		ClientReferenceID: nilIfEmpty(req.ClientReferenceID),
	}
	if req.CustomerEmail != "" {
		params.CustomerEmail = stripego.String(req.CustomerEmail)
	}
	for k, v := range req.Metadata {
		params.AddMetadata(k, v)
	}
	if idempotencyKey != "" {
		params.SetIdempotencyKey(idempotencyKey)
	}
	params.Context = ctx

	s, err := session.New(params)
	if err != nil {
		return nil, mapStripeError("create checkout session", err)
	}
	out := &CheckoutSession{
		ID:          s.ID,
		URL:         s.URL,
		Status:      string(s.Status),
		AmountTotal: s.AmountTotal,
		Currency:    string(s.Currency),
	}
	if s.PaymentIntent != nil {
		out.PaymentIntentID = s.PaymentIntent.ID
		out.ClientSecret = s.PaymentIntent.ClientSecret
	}
	return out, nil
}

func (c *realClient) CreatePaymentIntent(ctx context.Context, req CreatePaymentIntentRequest, idempotencyKey string) (*PaymentIntent, error) {
	pmTypes := make([]*string, 0, len(req.PaymentMethodTypes))
	for _, t := range req.PaymentMethodTypes {
		pmTypes = append(pmTypes, stripego.String(t))
	}
	params := &stripego.PaymentIntentParams{
		Amount:             stripego.Int64(req.Amount),
		Currency:           stripego.String(req.Currency),
		PaymentMethodTypes: pmTypes,
	}
	if req.Description != "" {
		params.Description = stripego.String(req.Description)
	}
	if req.CustomerEmail != "" {
		params.ReceiptEmail = stripego.String(req.CustomerEmail)
	}
	for k, v := range req.Metadata {
		params.AddMetadata(k, v)
	}
	if idempotencyKey != "" {
		params.SetIdempotencyKey(idempotencyKey)
	}
	params.Context = ctx

	pi, err := paymentintent.New(params)
	if err != nil {
		return nil, mapStripeError("create payment intent", err)
	}
	return mapPaymentIntent(pi), nil
}

func (c *realClient) GetPaymentIntent(ctx context.Context, id string) (*PaymentIntent, error) {
	params := &stripego.PaymentIntentParams{}
	params.Context = ctx
	pi, err := paymentintent.Get(id, params)
	if err != nil {
		return nil, mapStripeError("get payment intent", err)
	}
	return mapPaymentIntent(pi), nil
}

func (c *realClient) GetCheckoutSession(ctx context.Context, id string) (*CheckoutSession, error) {
	params := &stripego.CheckoutSessionParams{}
	params.Context = ctx
	s, err := session.Get(id, params)
	if err != nil {
		return nil, mapStripeError("get checkout session", err)
	}
	out := &CheckoutSession{
		ID:          s.ID,
		URL:         s.URL,
		Status:      string(s.Status),
		AmountTotal: s.AmountTotal,
		Currency:    string(s.Currency),
	}
	if s.PaymentIntent != nil {
		out.PaymentIntentID = s.PaymentIntent.ID
		out.ClientSecret = s.PaymentIntent.ClientSecret
	}
	return out, nil
}

func (c *realClient) CreateRefund(ctx context.Context, req CreateRefundRequest, idempotencyKey string) (*Refund, error) {
	params := &stripego.RefundParams{
		PaymentIntent: stripego.String(req.PaymentIntentID),
	}
	if req.Amount > 0 {
		params.Amount = stripego.Int64(req.Amount)
	}
	if req.Reason != "" {
		params.Reason = stripego.String(req.Reason)
	}
	for k, v := range req.Metadata {
		params.AddMetadata(k, v)
	}
	if idempotencyKey != "" {
		params.SetIdempotencyKey(idempotencyKey)
	}
	params.Context = ctx

	r, err := refund.New(params)
	if err != nil {
		return nil, mapStripeError("create refund", err)
	}
	out := &Refund{
		ID:       r.ID,
		Amount:   r.Amount,
		Currency: string(r.Currency),
		Status:   string(r.Status),
	}
	if r.PaymentIntent != nil {
		out.PaymentIntentID = r.PaymentIntent.ID
	}
	if r.Charge != nil {
		out.ChargeID = r.Charge.ID
	}
	return out, nil
}

// VerifyWebhookSignature is provided by the dedicated signature.go file.
func (c *realClient) VerifyWebhookSignature(payload []byte, sigHeader, secret string) (*Event, error) {
	if secret == "" {
		secret = c.webhookSecret
	}
	return VerifyAndConstructEvent(payload, sigHeader, secret)
}

func mapPaymentIntent(pi *stripego.PaymentIntent) *PaymentIntent {
	if pi == nil {
		return nil
	}
	out := &PaymentIntent{
		ID:           pi.ID,
		ClientSecret: pi.ClientSecret,
		Amount:       pi.Amount,
		Currency:     string(pi.Currency),
		Status:       string(pi.Status),
		Metadata:     map[string]string{},
	}
	for k, v := range pi.Metadata {
		out.Metadata[k] = v
	}
	if pi.LatestCharge != nil {
		out.ChargeID = pi.LatestCharge.ID
	}
	return out
}

// ProviderError is returned for Stripe failures, with a category that callers
// can switch on to decide HTTP mapping or retry.
type ProviderError struct {
	Op       string
	Category string // "card", "rate_limit", "api", "network"
	Err      error
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s: %s: %v", e.Op, e.Category, e.Err)
}
func (e *ProviderError) Unwrap() error { return e.Err }

func mapStripeError(op string, err error) error {
	var serr *stripego.Error
	if errors.As(err, &serr) {
		cat := "api"
		switch serr.Type {
		case stripego.ErrorTypeCard:
			cat = "card"
		case stripego.ErrorTypeAPI:
			cat = "api"
		case stripego.ErrorTypeIdempotency:
			cat = "idempotency"
		case stripego.ErrorTypeInvalidRequest:
			cat = "invalid_request"
		}
		// Stripe surfaces rate limiting via HTTP 429 + Code, not a dedicated Type.
		if serr.HTTPStatusCode == 429 {
			cat = "rate_limit"
		}
		return &ProviderError{Op: op, Category: cat, Err: serr}
	}
	return &ProviderError{Op: op, Category: "network", Err: err}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return stripego.String(s)
}

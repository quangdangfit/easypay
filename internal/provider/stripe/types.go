package stripe

// CreateCheckoutRequest is the input to Client.CreateCheckoutSession.
type CreateCheckoutRequest struct {
	Amount             int64
	Currency           string
	PaymentMethodTypes []string
	CustomerEmail      string
	SuccessURL         string
	CancelURL          string
	ClientReferenceID  string
	Metadata           map[string]string
}

// CheckoutSession is what Stripe returns from CreateCheckoutSession.
type CheckoutSession struct {
	ID              string
	URL             string
	PaymentIntentID string
	ClientSecret    string
	Status          string
	AmountTotal     int64
	Currency        string
}

// CreatePaymentIntentRequest is the input to Client.CreatePaymentIntent.
type CreatePaymentIntentRequest struct {
	Amount             int64
	Currency           string
	PaymentMethodTypes []string
	CustomerEmail      string
	Metadata           map[string]string
	Description        string
}

// PaymentIntent mirrors the subset of Stripe's PaymentIntent we care about.
type PaymentIntent struct {
	ID           string
	ClientSecret string
	Amount       int64
	Currency     string
	Status       string
	ChargeID     string
	Metadata     map[string]string
}

// CreateRefundRequest is the input to Client.CreateRefund.
type CreateRefundRequest struct {
	PaymentIntentID string
	Amount          int64 // 0 = full refund
	Reason          string
	Metadata        map[string]string
}

type Refund struct {
	ID              string
	Amount          int64
	Currency        string
	Status          string
	PaymentIntentID string
	ChargeID        string
}

// Event is the verified Stripe webhook event.
type Event struct {
	ID         string
	Type       string
	Created    int64
	APIVersion string
	Data       []byte // raw object JSON
}

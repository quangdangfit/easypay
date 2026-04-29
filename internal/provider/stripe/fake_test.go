package stripe

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFake_CheckoutSession(t *testing.T) {
	c := NewFake()
	res, err := c.CreateCheckoutSession(context.Background(), CreateCheckoutRequest{
		Amount: 1500, Currency: "usd",
	}, "idem-1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.ID == "" || res.URL == "" || res.PaymentIntentID == "" {
		t.Fatalf("missing fields: %+v", res)
	}
	if res.AmountTotal != 1500 || res.Currency != "usd" {
		t.Fatalf("amount/currency: %+v", res)
	}
}

func TestFake_PaymentIntent(t *testing.T) {
	c := NewFake()
	pi, err := c.CreatePaymentIntent(context.Background(), CreatePaymentIntentRequest{
		Amount: 200, Currency: "USD",
	}, "idem-2")
	if err != nil {
		t.Fatal(err)
	}
	if pi.ID == "" || pi.Amount != 200 {
		t.Fatalf("pi: %+v", pi)
	}
}

func TestFake_Refund(t *testing.T) {
	c := NewFake()
	r, err := c.CreateRefund(context.Background(), CreateRefundRequest{
		PaymentIntentID: "pi_1", Amount: 500,
	}, "idem-3")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "succeeded" || r.Amount != 500 {
		t.Fatalf("refund: %+v", r)
	}
}

func TestFake_GetReturnsSucceeded(t *testing.T) {
	c := NewFake()
	pi, err := c.GetPaymentIntent(context.Background(), "pi_x")
	if err != nil || pi.Status != "succeeded" {
		t.Fatalf("pi: %+v err=%v", pi, err)
	}
	cs, err := c.GetCheckoutSession(context.Background(), "cs_x")
	if err != nil || cs.ID != "cs_x" {
		t.Fatalf("cs: %+v err=%v", cs, err)
	}
}

func TestFake_VerifyDelegatesToReal(t *testing.T) {
	c := NewFake()
	payload := []byte(`{"id":"evt_1","type":"x","data":{"object":{}}}`)
	hdr := SignPayload(payload, "secret", time.Now().Unix())
	ev, err := c.VerifyWebhookSignature(payload, hdr, "secret")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ev.ID != "evt_1" {
		t.Fatalf("event: %+v", ev)
	}
}

func TestProviderError(t *testing.T) {
	pe := &ProviderError{Op: "create", Category: "card", Err: errors.New("declined")}
	if pe.Error() == "" {
		t.Fatal("empty error string")
	}
	if !errors.Is(pe, pe.Err) {
		t.Fatal("errors.Is should unwrap through ProviderError.Unwrap")
	}
	if pe.Unwrap() == nil {
		t.Fatal("unwrap nil")
	}
}

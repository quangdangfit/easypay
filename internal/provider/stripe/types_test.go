package stripe

import (
	"context"
	"errors"
	"testing"
)

func TestStub_AllMethodsReturnNotConfigured(t *testing.T) {
	s := NewStub()
	cases := []func() error{
		func() error {
			_, e := s.CreateCheckoutSession(context.Background(), CreateCheckoutRequest{}, "")
			return e
		},
		func() error {
			_, e := s.CreatePaymentIntent(context.Background(), CreatePaymentIntentRequest{}, "")
			return e
		},
		func() error { _, e := s.GetPaymentIntent(context.Background(), ""); return e },
		func() error { _, e := s.GetCheckoutSession(context.Background(), ""); return e },
		func() error { _, e := s.CreateRefund(context.Background(), CreateRefundRequest{}, ""); return e },
		func() error { _, e := s.VerifyWebhookSignature(nil, "", ""); return e },
	}
	for i, fn := range cases {
		if err := fn(); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("case %d: %v", i, err)
		}
	}
}

func TestBreakerClient_State(t *testing.T) {
	c := NewBreakerClient(NewFake(), "test")
	bc, ok := c.(*breakerClient)
	if !ok {
		t.Fatal("not a breakerClient")
	}
	if bc.State() != "closed" {
		t.Fatalf("initial: %s", bc.State())
	}
}

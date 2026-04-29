package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
)

func newRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}

func signedEvent(t *testing.T, secret, eventID, eventType, orderID, piID string) []byte {
	t.Helper()
	body := []byte(`{"id":"` + eventID + `","type":"` + eventType +
		`","data":{"object":{"id":"` + piID + `","object":"payment_intent","amount":1500,"status":"succeeded",` +
		`"metadata":{"order_id":"` + orderID + `","merchant_id":"M1"}}}}`)
	return body
}

const sec = "whsec_test"

func TestWebhook_BadSignatureRejected(t *testing.T) {
	repo := newOrderStore(t)
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_1", "payment_intent.succeeded", "ORD-1", "pi_1")
	if err := svc.Process(context.Background(), body, "garbage"); err == nil {
		t.Fatal("expected signature error")
	}
}

func TestWebhook_DuplicateEventReturnsErrDuplicate(t *testing.T) {
	repo := newOrderStore(t, &domain.Order{OrderID: "ORD-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_dup", "payment_intent.succeeded", "ORD-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := svc.Process(context.Background(), body, hdr); !errors.Is(err, ErrWebhookDuplicate) {
		t.Fatalf("want duplicate, got %v", err)
	}
}

func TestWebhook_SucceededPath(t *testing.T) {
	repo := newOrderStore(t, &domain.Order{OrderID: "ORD-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_ok", "payment_intent.succeeded", "ORD-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("process: %v", err)
	}
	if repo.byID["ORD-1"].Status != domain.OrderStatusPaid {
		t.Fatalf("status: %s", repo.byID["ORD-1"].Status)
	}
	if len(pub.confirmed) != 1 {
		t.Fatalf("expected 1 confirmation event, got %d", len(pub.confirmed))
	}
}

func TestWebhook_FailedPath(t *testing.T) {
	repo := newOrderStore(t, &domain.Order{OrderID: "ORD-1", MerchantID: "M1", Amount: 1500, Currency: "USD"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_fail", "payment_intent.payment_failed", "ORD-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("process: %v", err)
	}
	if repo.byID["ORD-1"].Status != domain.OrderStatusFailed {
		t.Fatalf("status: %s", repo.byID["ORD-1"].Status)
	}
}

func TestWebhook_RefundedPath(t *testing.T) {
	repo := newOrderStore(t, &domain.Order{OrderID: "ORD-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, pub.mock, rc, sec)

	// charge.refunded carries metadata.order_id in our fixtures.
	body := signedEvent(t, sec, "evt_refund", "charge.refunded", "ORD-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("process: %v", err)
	}
	if repo.byID["ORD-1"].Status != domain.OrderStatusRefunded {
		t.Fatalf("status: %s", repo.byID["ORD-1"].Status)
	}
}

func TestWebhook_DisputeIsNoOp(t *testing.T) {
	repo := newOrderStore(t, &domain.Order{OrderID: "ORD-1", MerchantID: "M1"})
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, newEventCapture(t).mock, rc, sec)
	body := signedEvent(t, sec, "evt_disp", "charge.dispute.created", "ORD-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("process: %v", err)
	}
	if repo.byID["ORD-1"].Status == domain.OrderStatusFailed {
		t.Fatal("dispute shouldn't fail order")
	}
}

func TestWebhook_UnknownEventTypeIgnored(t *testing.T) {
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), newOrderStore(t).mock, newEventCapture(t).mock, rc, sec)
	body := signedEvent(t, sec, "evt_x", "unknown.event", "ORD-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("expected ignore, got %v", err)
	}
}

// --- CreateRefund ---

func TestCreateRefund_HappyPath(t *testing.T) {
	repo := newOrderStore(t, &domain.Order{OrderID: "ORD-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1"})
	rc := newRedis(t)
	sf := newWebhookStripe(t, webhookStripeOpts{refundID: "re_1"})
	svc := NewWebhookService(sf, repo.mock, newEventCapture(t).mock, rc, sec)
	res, err := svc.CreateRefund(context.Background(), RefundInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, OrderID: "ORD-1", Amount: 500,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.RefundID != "re_1" {
		t.Fatalf("id: %s", res.RefundID)
	}
}

func TestCreateRefund_RejectsForeignMerchant(t *testing.T) {
	repo := newOrderStore(t, &domain.Order{OrderID: "ORD-1", MerchantID: "M1", StripePaymentIntentID: "pi_1"})
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, newEventCapture(t).mock, rc, sec)
	_, err := svc.CreateRefund(context.Background(), RefundInput{
		Merchant: &domain.Merchant{MerchantID: "M_OTHER"}, OrderID: "ORD-1",
	})
	if err == nil {
		t.Fatal("expected merchant mismatch error")
	}
}

func TestCreateRefund_RequiresPaymentIntent(t *testing.T) {
	repo := newOrderStore(t, &domain.Order{OrderID: "ORD-1", MerchantID: "M1"}) // no PI yet
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, newEventCapture(t).mock, rc, sec)
	_, err := svc.CreateRefund(context.Background(), RefundInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, OrderID: "ORD-1",
	})
	if err == nil {
		t.Fatal("expected no-PI error")
	}
}

func TestCreateRefund_RequiresMerchantAndOrder(t *testing.T) {
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), newOrderStore(t).mock, newEventCapture(t).mock, rc, sec)
	_, err := svc.CreateRefund(context.Background(), RefundInput{Merchant: nil, OrderID: ""})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

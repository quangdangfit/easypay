package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/mock/gomock"

	"github.com/quangdangfit/easypay/internal/domain"
	repomock "github.com/quangdangfit/easypay/internal/mocks/repo"
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
	repo := newTxStore(t)
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_1", "payment_intent.succeeded", "ord-1", "pi_1")
	if err := svc.Process(context.Background(), body, "garbage"); err == nil {
		t.Fatal("expected signature error")
	}
}

func TestWebhook_DuplicateEventReturnsErrDuplicate(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_dup", "payment_intent.succeeded", "ord-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := svc.Process(context.Background(), body, hdr); !errors.Is(err, ErrWebhookDuplicate) {
		t.Fatalf("want duplicate, got %v", err)
	}
}

func TestWebhook_SucceededPath(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_ok", "payment_intent.succeeded", "ord-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("process: %v", err)
	}
	if repo.byID["M1:ord-1"].Status != domain.TransactionStatusPaid {
		t.Fatalf("status: %s", repo.byID["M1:ord-1"].Status)
	}
	if len(pub.confirmed) != 1 {
		t.Fatalf("expected 1 confirmation event, got %d", len(pub.confirmed))
	}
}

func TestWebhook_FailedPath(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_fail", "payment_intent.payment_failed", "ord-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("process: %v", err)
	}
	if repo.byID["M1:ord-1"].Status != domain.TransactionStatusFailed {
		t.Fatalf("status: %s", repo.byID["M1:ord-1"].Status)
	}
}

func TestWebhook_RefundedPath(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	// charge.refunded carries metadata.order_id in our fixtures.
	body := signedEvent(t, sec, "evt_refund", "charge.refunded", "ord-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("process: %v", err)
	}
	if repo.byID["M1:ord-1"].Status != domain.TransactionStatusRefunded {
		t.Fatalf("status: %s", repo.byID["M1:ord-1"].Status)
	}
}

func TestWebhook_DisputeIsNoOp(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1"})
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), newEventCapture(t).mock, rc, sec)
	body := signedEvent(t, sec, "evt_disp", "charge.dispute.created", "ord-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("process: %v", err)
	}
	if repo.byID["M1:ord-1"].Status == domain.TransactionStatusFailed {
		t.Fatal("dispute shouldn't fail order")
	}
}

func TestWebhook_UnknownEventTypeIgnored(t *testing.T) {
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), newTxStore(t).mock, stubMerchants(t, 0), newEventCapture(t).mock, rc, sec)
	body := signedEvent(t, sec, "evt_x", "unknown.event", "ord-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("expected ignore, got %v", err)
	}
}

// --- CreateRefund ---

func TestCreateRefund_HappyPath(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1"})
	rc := newRedis(t)
	sf := newWebhookStripe(t, webhookStripeOpts{refundID: "re_1"})
	svc := NewWebhookService(sf, repo.mock, stubMerchants(t, 0), newEventCapture(t).mock, rc, sec)
	res, err := svc.CreateRefund(context.Background(), RefundInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, OrderID: "ord-1", Amount: 500,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.RefundID != "re_1" {
		t.Fatalf("id: %s", res.RefundID)
	}
}

func TestCreateRefund_RejectsForeignMerchant(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", StripePaymentIntentID: "pi_1"})
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), newEventCapture(t).mock, rc, sec)
	_, err := svc.CreateRefund(context.Background(), RefundInput{
		Merchant: &domain.Merchant{MerchantID: "M_OTHER"}, OrderID: "ord-1",
	})
	if err == nil {
		t.Fatal("expected merchant mismatch error")
	}
}

func TestCreateRefund_RequiresPaymentIntent(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1"}) // no PI yet
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), newEventCapture(t).mock, rc, sec)
	_, err := svc.CreateRefund(context.Background(), RefundInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, OrderID: "ord-1",
	})
	if err == nil {
		t.Fatal("expected no-PI error")
	}
}

func TestCreateRefund_RequiresMerchantAndOrder(t *testing.T) {
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), newTxStore(t).mock, stubMerchants(t, 0), newEventCapture(t).mock, rc, sec)
	_, err := svc.CreateRefund(context.Background(), RefundInput{Merchant: nil, OrderID: ""})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestWebhook_OrderNotFound(t *testing.T) {
	repo := newTxStore(t) // empty
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_notfound", "payment_intent.succeeded", "missing-order", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	err := svc.Process(context.Background(), body, hdr)
	if !errors.Is(err, ErrWebhookOrderMissing) {
		t.Fatalf("expected ErrWebhookOrderMissing, got %v", err)
	}
}

func TestCreateRefund_OrderNotFound(t *testing.T) {
	repo := newTxStore(t) // empty
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), newEventCapture(t).mock, rc, sec)
	_, err := svc.CreateRefund(context.Background(), RefundInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, OrderID: "missing",
	})
	if err == nil {
		t.Fatal("expected order not found error")
	}
}

func TestCreateRefund_StripeFails(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1"})
	rc := newRedis(t)
	sf := newWebhookStripe(t, webhookStripeOpts{refundErr: errors.New("stripe error")})
	svc := NewWebhookService(sf, repo.mock, stubMerchants(t, 0), newEventCapture(t).mock, rc, sec)
	_, err := svc.CreateRefund(context.Background(), RefundInput{
		Merchant: &domain.Merchant{MerchantID: "M1"}, OrderID: "ord-1", Amount: 500,
	})
	if err == nil {
		t.Fatal("expected stripe error to propagate")
	}
}

func TestWebhook_RefundedPath_WithMetadataFallback(t *testing.T) {
	// Refund event with payment_intent in metadata but not in top-level field
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	// Create body with payment_intent in metadata but not in top level
	body := []byte(`{"id":"evt_meta","type":"charge.refunded",` +
		`"data":{"object":{"id":"ch_1","object":"charge","amount":1500,"status":"refunded",` +
		`"metadata":{"order_id":"ord-1","merchant_id":"M1","payment_intent":"pi_1"}}}}`)
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("process: %v", err)
	}
	if repo.byID["M1:ord-1"].Status != domain.TransactionStatusRefunded {
		t.Fatalf("status: %s", repo.byID["M1:ord-1"].Status)
	}
}

func TestWebhook_RefundedPath_WithPILookup(t *testing.T) {
	// Refund event where metadata is missing but PI lookup succeeds
	tx := &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD", StripePaymentIntentID: "pi_1"}
	repo := newTxStore(t, tx)
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	// Create body with NO metadata but with pi_1 in the charge
	body := []byte(`{"id":"evt_pi","type":"charge.refunded",` +
		`"data":{"object":{"id":"ch_1","object":"charge","amount":1500,"status":"refunded",` +
		`"payment_intent":"pi_1","metadata":{}}}}`)
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("process: %v", err)
	}
	if repo.byID["M1:ord-1"].Status != domain.TransactionStatusRefunded {
		t.Fatalf("status: %s", repo.byID["M1:ord-1"].Status)
	}
}

func TestWebhook_FailedPath_MissingMetadata(t *testing.T) {
	repo := newTxStore(t)
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	// payment_intent.payment_failed with missing metadata
	body := []byte(`{"id":"evt_nometa","type":"payment_intent.payment_failed",` +
		`"data":{"object":{"id":"pi_1","object":"payment_intent","amount":1500,"status":"requires_payment_method",` +
		`"metadata":{}}}}`)
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	err := svc.Process(context.Background(), body, hdr)
	if err == nil {
		t.Fatal("expected error for missing metadata")
	}
}

func TestWebhook_FailedPath_HappyPath(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_fail", "payment_intent.payment_failed", "ord-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("process: %v", err)
	}
	if repo.byID["M1:ord-1"].Status != domain.TransactionStatusFailed {
		t.Fatalf("status: %s", repo.byID["M1:ord-1"].Status)
	}
}

func TestWebhook_SucceededPath_WithPaymentIntent(t *testing.T) {
	// Test case where metadata doesn't have payment_intent but object is payment_intent
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	body := []byte(`{"id":"evt_pi_obj","type":"payment_intent.succeeded",` +
		`"data":{"object":{"id":"pi_direct","object":"payment_intent","amount":1500,"status":"succeeded",` +
		`"metadata":{"order_id":"ord-1","merchant_id":"M1"}}}}`)
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("process: %v", err)
	}
	if repo.byID["M1:ord-1"].Status != domain.TransactionStatusPaid {
		t.Fatalf("status: %s", repo.byID["M1:ord-1"].Status)
	}
}

func TestWebhook_MalformedJSON(t *testing.T) {
	repo := newTxStore(t)
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	body := []byte(`{invalid json}`)
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	err := svc.Process(context.Background(), body, hdr)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestWebhook_SucceededPath_Idempotent(t *testing.T) {
	// Test that processing the same succeeded event twice is safe
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_dup", "payment_intent.succeeded", "ord-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())

	// First call should succeed
	if err := svc.Process(context.Background(), body, hdr); err != nil {
		t.Fatalf("first: %v", err)
	}

	// Second call with same event should be deduplicated by Redis
	err := svc.Process(context.Background(), body, hdr)
	if err != nil && !errors.Is(err, ErrWebhookDuplicate) {
		t.Fatalf("second call should be deduplicated or succeed, got %v", err)
	}
}

func TestWebhook_FailedPath_UpdateError(t *testing.T) {
	repo := newTxStore(t) // Empty - will cause update to fail
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_upd_err", "payment_intent.payment_failed", "ord-missing", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	err := svc.Process(context.Background(), body, hdr)
	if !errors.Is(err, ErrWebhookOrderMissing) {
		t.Fatalf("want ErrWebhookOrderMissing, got %v", err)
	}
}

func TestWebhook_SucceededPath_UnknownMerchant(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchantsError(t), pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_unknown_merchant", "payment_intent.succeeded", "ord-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	err := svc.Process(context.Background(), body, hdr)
	if err == nil {
		t.Fatal("expected error for unknown merchant")
	}
}

func TestWebhook_SucceededPath_CrossCheckFails(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{piStatus: "requires_payment_method"}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_cross_check_fail", "payment_intent.succeeded", "ord-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	err := svc.Process(context.Background(), body, hdr)
	if err == nil {
		t.Fatal("expected error when cross-check fails")
	}
	if !strings.Contains(err.Error(), "not succeeded") {
		t.Fatalf("expected cross-check error, got %v", err)
	}
}

func TestWebhook_SucceededPath_GetPaymentIntentError(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{piErr: errors.New("stripe error")}), repo.mock, stubMerchants(t, 0), pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_stripe_err", "payment_intent.succeeded", "ord-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	err := svc.Process(context.Background(), body, hdr)
	if err == nil {
		t.Fatal("expected error when GetPaymentIntent fails")
	}
}

func TestWebhook_FailedPath_UnknownMerchant(t *testing.T) {
	repo := newTxStore(t, &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD"})
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchantsError(t), pub.mock, rc, sec)

	body := signedEvent(t, sec, "evt_bad_merchant", "payment_intent.payment_failed", "ord-1", "pi_1")
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	err := svc.Process(context.Background(), body, hdr)
	if err == nil {
		t.Fatal("expected error for unknown merchant")
	}
}

func TestWebhook_RefundedPath_NoMetadata_UpdateFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := repomock.NewMockTransactionRepository(ctrl)
	order := &domain.Transaction{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD", ShardIndex: 0}
	repo.EXPECT().GetByPaymentIntentID(gomock.Any(), "pi_1").
		Return(order, nil)
	repo.EXPECT().UpdateStatus(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(errors.New("update failed"))

	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo, stubMerchants(t, 0), pub.mock, rc, sec)

	body := []byte(`{"id":"evt_pi_lookup","type":"charge.refunded",` +
		`"data":{"object":{"amount":1500,"payment_intent":"pi_1"}}}`)
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	err := svc.Process(context.Background(), body, hdr)
	if err == nil {
		t.Fatal("expected error when UpdateStatus fails")
	}
}

func TestWebhook_RefundedPath_ResolveMerchantError(t *testing.T) {
	repo := newTxStore(t)
	pub := newEventCapture(t)
	rc := newRedis(t)
	svc := NewWebhookService(newWebhookStripe(t, webhookStripeOpts{}), repo.mock, stubMerchantsError(t), pub.mock, rc, sec)

	body := []byte(`{"id":"evt_resolve_err","type":"charge.refunded",` +
		`"data":{"object":{"amount":1500,"metadata":{"order_id":"ord-1","merchant_id":"M_unknown"}}}}`)
	hdr := stripe.SignPayload(body, sec, time.Now().Unix())
	err := svc.Process(context.Background(), body, hdr)
	if err == nil {
		t.Fatal("expected error when resolving merchant fails")
	}
}

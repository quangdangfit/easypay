package service

import (
	"context"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/quangdangfit/easypay/internal/cache"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	cachemock "github.com/quangdangfit/easypay/internal/mocks/cache"
	kafkamock "github.com/quangdangfit/easypay/internal/mocks/kafka"
	repomock "github.com/quangdangfit/easypay/internal/mocks/repo"
	stripemock "github.com/quangdangfit/easypay/internal/mocks/stripe"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
	"github.com/quangdangfit/easypay/internal/repository"
)

// orderStore wires a MockOrderRepository whose behaviour is backed by an
// in-memory map. Tests can reach into orderStore.byID to seed and assert.
type orderStore struct {
	byID            map[string]*domain.Order
	pending         []*domain.Order
	updateCheckouts int
	mock            *repomock.MockOrderRepository
}

func newOrderStore(t *testing.T, seed ...*domain.Order) *orderStore {
	t.Helper()
	s := &orderStore{byID: map[string]*domain.Order{}}
	for _, o := range seed {
		s.byID[o.OrderID] = o
	}
	s.mock = repomock.NewMockOrderRepository(gomock.NewController(t))
	s.mock.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, o *domain.Order) error {
			s.byID[o.OrderID] = o
			return nil
		}).AnyTimes()
	s.mock.EXPECT().GetByOrderID(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, id string) (*domain.Order, error) {
			if o, ok := s.byID[id]; ok {
				return o, nil
			}
			return nil, repository.ErrNotFound
		}).AnyTimes()
	s.mock.EXPECT().GetByPaymentIntentID(gomock.Any(), gomock.Any()).
		Return(nil, repository.ErrNotFound).AnyTimes()
	s.mock.EXPECT().UpdateStatus(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, id string, st domain.OrderStatus, pi string) error {
			o, ok := s.byID[id]
			if !ok {
				return repository.ErrNotFound
			}
			o.Status = st
			if pi != "" {
				o.StripePaymentIntentID = pi
			}
			return nil
		}).AnyTimes()
	s.mock.EXPECT().UpdateCheckout(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, id, sid, pi, url string) error {
			s.updateCheckouts++
			o, ok := s.byID[id]
			if !ok {
				return repository.ErrNotFound
			}
			o.StripeSessionID = sid
			o.StripePaymentIntentID = pi
			o.CheckoutURL = url
			return nil
		}).AnyTimes()
	s.mock.EXPECT().BatchCreate(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, orders []*domain.Order) error {
			for _, o := range orders {
				s.byID[o.OrderID] = o
			}
			return nil
		}).AnyTimes()
	s.mock.EXPECT().GetPendingBefore(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ time.Time, _ int) ([]*domain.Order, error) {
			return s.pending, nil
		}).AnyTimes()
	return s
}

// pendingStore wires a MockPendingOrderStore with a map-backed Get/Put.
type pendingStore struct {
	store map[string]*cache.PendingOrder
	mock  *cachemock.MockPendingOrderStore
}

func newPendingStore(t *testing.T) *pendingStore {
	t.Helper()
	p := &pendingStore{store: map[string]*cache.PendingOrder{}}
	p.mock = cachemock.NewMockPendingOrderStore(gomock.NewController(t))
	p.mock.EXPECT().Put(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, o *cache.PendingOrder, _ time.Duration) error {
			p.store[o.OrderID] = o
			return nil
		}).AnyTimes()
	p.mock.EXPECT().Get(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, id string) (*cache.PendingOrder, error) {
			if o, ok := p.store[id]; ok {
				return o, nil
			}
			return nil, cache.ErrPendingOrderNotFound
		}).AnyTimes()
	return p
}

// eventCapture wires a MockEventPublisher that records every published event.
type eventCapture struct {
	events    []kafka.PaymentEvent
	confirmed []kafka.PaymentConfirmedEvent
	mock      *kafkamock.MockEventPublisher
}

func newEventCapture(t *testing.T) *eventCapture {
	t.Helper()
	e := &eventCapture{}
	e.mock = kafkamock.NewMockEventPublisher(gomock.NewController(t))
	e.mock.EXPECT().PublishPaymentEvent(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, ev kafka.PaymentEvent) error {
			e.events = append(e.events, ev)
			return nil
		}).AnyTimes()
	e.mock.EXPECT().PublishPaymentConfirmed(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, ev kafka.PaymentConfirmedEvent) error {
			e.confirmed = append(e.confirmed, ev)
			return nil
		}).AnyTimes()
	e.mock.EXPECT().Close().Return(nil).AnyTimes()
	return e
}

// idemStore wires a MockIdempotencyChecker with map-backed behaviour.
type idemStore struct {
	store map[string][]byte
	mock  *cachemock.MockIdempotencyChecker
}

func newIdemStore(t *testing.T) *idemStore {
	t.Helper()
	i := &idemStore{store: map[string][]byte{}}
	i.mock = cachemock.NewMockIdempotencyChecker(gomock.NewController(t))
	i.mock.EXPECT().Check(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, key string) (bool, []byte, error) {
			v, ok := i.store[key]
			return ok, v, nil
		}).AnyTimes()
	i.mock.EXPECT().Set(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, key string, response []byte, _ time.Duration) error {
			i.store[key] = response
			return nil
		}).AnyTimes()
	return i
}

// stripeStub returns a MockClient that simulates a healthy Stripe API: every
// CreateCheckoutSession produces the same hard-coded session, other methods
// return zero values.
type stripeStub struct {
	createCalls int
	mock        *stripemock.MockClient
}

func newStripeStub(t *testing.T) *stripeStub {
	t.Helper()
	s := &stripeStub{}
	s.mock = stripemock.NewMockClient(gomock.NewController(t))
	s.mock.EXPECT().CreateCheckoutSession(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ stripe.CreateCheckoutRequest, _ string) (*stripe.CheckoutSession, error) {
			s.createCalls++
			return &stripe.CheckoutSession{
				ID:              "cs_test_123",
				URL:             "https://checkout.stripe.com/cs_test_123",
				PaymentIntentID: "pi_test_123",
				ClientSecret:    "pi_test_123_secret_xyz",
			}, nil
		}).AnyTimes()
	s.mock.EXPECT().CreatePaymentIntent(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	s.mock.EXPECT().GetPaymentIntent(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	s.mock.EXPECT().GetCheckoutSession(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	s.mock.EXPECT().CreateRefund(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	s.mock.EXPECT().VerifyWebhookSignature(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	return s
}

// webhookStripeOpts configures behaviour of the webhook-flow Stripe stub.
type webhookStripeOpts struct {
	piStatus  string // "succeeded" if zero
	piErr     error
	refundID  string
	refundErr error
}

func newWebhookStripe(t *testing.T, opts webhookStripeOpts) *stripemock.MockClient {
	t.Helper()
	m := stripemock.NewMockClient(gomock.NewController(t))
	m.EXPECT().GetPaymentIntent(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, id string) (*stripe.PaymentIntent, error) {
			if opts.piErr != nil {
				return nil, opts.piErr
			}
			st := opts.piStatus
			if st == "" {
				st = "succeeded"
			}
			return &stripe.PaymentIntent{ID: id, Status: st, Amount: 1500, Currency: "usd"}, nil
		}).AnyTimes()
	m.EXPECT().CreateRefund(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, r stripe.CreateRefundRequest, _ string) (*stripe.Refund, error) {
			if opts.refundErr != nil {
				return nil, opts.refundErr
			}
			return &stripe.Refund{ID: opts.refundID, Amount: r.Amount, Currency: "usd", Status: "succeeded", PaymentIntentID: r.PaymentIntentID}, nil
		}).AnyTimes()
	m.EXPECT().VerifyWebhookSignature(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(stripe.VerifyAndConstructEvent).AnyTimes()
	m.EXPECT().CreateCheckoutSession(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	m.EXPECT().CreatePaymentIntent(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	m.EXPECT().GetCheckoutSession(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	return m
}

// successLocker returns a Locker mock whose Acquire always returns a Lock with
// no-op Release.
func successLocker(t *testing.T) *cachemock.MockLocker {
	t.Helper()
	ctrl := gomock.NewController(t)
	lock := cachemock.NewMockLock(ctrl)
	lock.EXPECT().Release(gomock.Any()).Return(nil).AnyTimes()
	l := cachemock.NewMockLocker(ctrl)
	l.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any()).Return(lock, nil).AnyTimes()
	return l
}

// allowingBucket returns a TokenBucket mock that always allows.
func allowingBucket(t *testing.T) *cachemock.MockTokenBucket {
	t.Helper()
	b := cachemock.NewMockTokenBucket(gomock.NewController(t))
	b.EXPECT().Allow(gomock.Any()).Return(nil).AnyTimes()
	return b
}

// blockingBucket returns a TokenBucket that always rejects with ErrRateLimited.
func blockingBucket(t *testing.T) *cachemock.MockTokenBucket {
	t.Helper()
	b := cachemock.NewMockTokenBucket(gomock.NewController(t))
	b.EXPECT().Allow(gomock.Any()).Return(cache.ErrRateLimited).AnyTimes()
	return b
}

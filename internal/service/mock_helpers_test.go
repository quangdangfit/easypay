package service

import (
	"context"
	"sync"
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

// txStore wires a MockTransactionRepository whose behaviour is backed by an
// in-memory map. Tests can reach into txStore.byID to seed and assert.
//
// Insert is the single write path; on duplicate (merchant_id,
// transaction_id) it returns repository.ErrDuplicateTransaction so the service's
// idempotent fallback to GetByTransactionID kicks in.
type txStore struct {
	mu              sync.Mutex
	byID            map[string]*domain.Transaction // keyed by order_id
	byTxn           map[string]*domain.Transaction // keyed by merchant_id+":"+transaction_id
	pending         []*domain.Transaction
	updateCheckouts int
	mock            *repomock.MockTransactionRepository
}

// cloneOrder returns a shallow copy so callers never share pointers with
// the in-memory map, matching the deserialize-per-read behaviour of the
// real MySQL repo.
func cloneOrder(o *domain.Transaction) *domain.Transaction {
	if o == nil {
		return nil
	}
	cp := *o
	return &cp
}

func (s *txStore) lookupByID(id string) (*domain.Transaction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.byID[id]
	return cloneOrder(o), ok
}

func (s *txStore) lookupByTxn(key string) (*domain.Transaction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.byTxn[key]
	return cloneOrder(o), ok
}

func newTxStore(t *testing.T, seed ...*domain.Transaction) *txStore {
	t.Helper()
	s := &txStore{
		byID:  map[string]*domain.Transaction{},
		byTxn: map[string]*domain.Transaction{},
	}
	for _, o := range seed {
		s.byID[o.MerchantID+":"+o.OrderID] = o
		s.byTxn[o.MerchantID+":"+o.TransactionID] = o
	}
	s.mock = repomock.NewMockTransactionRepository(gomock.NewController(t))
	s.mock.EXPECT().Insert(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, o *domain.Transaction) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			key := o.MerchantID + ":" + o.TransactionID
			if _, ok := s.byTxn[key]; ok {
				return repository.ErrDuplicateTransaction
			}
			// Store a clone so the caller's pointer doesn't alias what the
			// store returns to other goroutines.
			cp := cloneOrder(o)
			s.byID[o.MerchantID+":"+o.OrderID] = cp
			s.byTxn[key] = cp
			return nil
		}).AnyTimes()
	s.mock.EXPECT().GetByMerchantOrderID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uint8, merchantID, orderID string) (*domain.Transaction, error) {
			if o, ok := s.lookupByID(merchantID + ":" + orderID); ok {
				return o, nil
			}
			return nil, repository.ErrNotFound
		}).AnyTimes()
	s.mock.EXPECT().GetByTransactionID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uint8, merchantID, txnID string) (*domain.Transaction, error) {
			if o, ok := s.lookupByTxn(merchantID + ":" + txnID); ok {
				return o, nil
			}
			return nil, repository.ErrNotFound
		}).AnyTimes()
	s.mock.EXPECT().GetByPaymentIntentID(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, pi string) (*domain.Transaction, error) {
			if pi == "" {
				return nil, repository.ErrNotFound
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, o := range s.byID {
				if o.StripePaymentIntentID == pi {
					return cloneOrder(o), nil
				}
			}
			return nil, repository.ErrNotFound
		}).AnyTimes()
	s.mock.EXPECT().UpdateStatus(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uint8, merchantID, orderID string, st domain.TransactionStatus, pi string) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			o, ok := s.byID[merchantID+":"+orderID]
			if !ok {
				return repository.ErrNotFound
			}
			o.Status = st
			if pi != "" {
				o.StripePaymentIntentID = pi
			}
			return nil
		}).AnyTimes()
	s.mock.EXPECT().UpdateCheckout(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uint8, merchantID, orderID, sid, pi string) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.updateCheckouts++
			o, ok := s.byID[merchantID+":"+orderID]
			if !ok {
				return repository.ErrNotFound
			}
			if sid != "" {
				o.StripeSessionID = sid
			}
			if pi != "" {
				o.StripePaymentIntentID = pi
			}
			return nil
		}).AnyTimes()
	s.mock.EXPECT().GetPendingBefore(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ time.Time, _ int) ([]*domain.Transaction, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.pending, nil
		}).AnyTimes()
	return s
}

// eventCapture wires a MockEventPublisher that records every published event.
type eventCapture struct {
	confirmed []kafka.PaymentConfirmedEvent
	mock      *kafkamock.MockEventPublisher
}

func newEventCapture(t *testing.T) *eventCapture {
	t.Helper()
	e := &eventCapture{}
	e.mock = kafkamock.NewMockEventPublisher(gomock.NewController(t))
	e.mock.EXPECT().PublishPaymentConfirmed(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, ev kafka.PaymentConfirmedEvent) error {
			e.confirmed = append(e.confirmed, ev)
			return nil
		}).AnyTimes()
	e.mock.EXPECT().Close().Return(nil).AnyTimes()
	return e
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

// stubMerchants returns a MerchantRepository mock that resolves any
// merchant_id to a fixed shard_index. Used by webhook/checkout-resolver
// tests that need a fast LRU-style merchant lookup but don't otherwise
// care about the merchant record.
func stubMerchants(t *testing.T, shardIdx uint8) *repomock.MockMerchantRepository {
	t.Helper()
	m := repomock.NewMockMerchantRepository(gomock.NewController(t))
	m.EXPECT().GetByMerchantID(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, id string) (*domain.Merchant, error) {
			return &domain.Merchant{MerchantID: id, ShardIndex: shardIdx, Status: domain.MerchantStatusActive}, nil
		}).AnyTimes()
	m.EXPECT().GetByAPIKey(gomock.Any(), gomock.Any()).
		Return(nil, repository.ErrMerchantNotFound).AnyTimes()
	m.EXPECT().Insert(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
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

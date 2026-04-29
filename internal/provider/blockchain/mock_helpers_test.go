package blockchain

import (
	"context"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/kafka"
	kafkamock "github.com/quangdangfit/easypay/internal/mocks/kafka"
	repomock "github.com/quangdangfit/easypay/internal/mocks/repo"
	"github.com/quangdangfit/easypay/internal/repository"
)

// orderStore wires a MockOrderRepository whose behaviour is backed by an
// in-memory map. Tests reach into byID to seed and assert state.
type orderStore struct {
	byID map[string]*domain.Order
	mock *repomock.MockOrderRepository
}

func newOrderStore(t *testing.T, seed ...*domain.Order) *orderStore {
	t.Helper()
	s := &orderStore{byID: map[string]*domain.Order{}}
	for _, o := range seed {
		s.byID[o.OrderID] = o
	}
	s.mock = repomock.NewMockOrderRepository(gomock.NewController(t))
	s.mock.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
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
		DoAndReturn(func(_ context.Context, id string, st domain.OrderStatus, _ string) error {
			if o, ok := s.byID[id]; ok {
				o.Status = st
				return nil
			}
			return repository.ErrNotFound
		}).AnyTimes()
	s.mock.EXPECT().UpdateCheckout(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).AnyTimes()
	s.mock.EXPECT().BatchCreate(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	s.mock.EXPECT().GetPendingBefore(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, nil).AnyTimes()
	return s
}

// eventCapture wires a MockEventPublisher that records every confirmed event.
type eventCapture struct {
	confirmed []kafka.PaymentConfirmedEvent
	mock      *kafkamock.MockEventPublisher
}

func newEventCapture(t *testing.T) *eventCapture {
	t.Helper()
	e := &eventCapture{}
	e.mock = kafkamock.NewMockEventPublisher(gomock.NewController(t))
	e.mock.EXPECT().PublishPaymentEvent(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	e.mock.EXPECT().PublishPaymentConfirmed(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, ev kafka.PaymentConfirmedEvent) error {
			e.confirmed = append(e.confirmed, ev)
			return nil
		}).AnyTimes()
	e.mock.EXPECT().Close().Return(nil).AnyTimes()
	return e
}

// pendingTxStore wires a MockPendingTxRepository backed by a map.
type pendingTxStore struct {
	byHash    map[string]*domain.PendingTx
	createErr error
	mock      *repomock.MockPendingTxRepository
}

func newPendingTxStore(t *testing.T) *pendingTxStore {
	t.Helper()
	s := &pendingTxStore{byHash: map[string]*domain.PendingTx{}}
	s.mock = repomock.NewMockPendingTxRepository(gomock.NewController(t))
	s.mock.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, tx *domain.PendingTx) error {
			if s.createErr != nil {
				return s.createErr
			}
			if _, exists := s.byHash[tx.TxHash]; exists {
				return repository.ErrPendingTxNotFound
			}
			s.byHash[tx.TxHash] = tx
			return nil
		}).AnyTimes()
	s.mock.EXPECT().GetByTxHash(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, hash string) (*domain.PendingTx, error) {
			if tx, ok := s.byHash[hash]; ok {
				return tx, nil
			}
			return nil, repository.ErrPendingTxNotFound
		}).AnyTimes()
	s.mock.EXPECT().UpdateConfirmations(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, hash string, conf uint64, st domain.PendingTxStatus) error {
			tx, ok := s.byHash[hash]
			if !ok {
				return repository.ErrPendingTxNotFound
			}
			tx.Confirmations = conf
			tx.Status = st
			return nil
		}).AnyTimes()
	s.mock.EXPECT().ListPendingByChain(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, chainID int64, limit int) ([]*domain.PendingTx, error) {
			out := make([]*domain.PendingTx, 0)
			for _, tx := range s.byHash {
				if tx.ChainID == chainID && tx.Status == domain.PendingTxStatusPending {
					out = append(out, tx)
					if len(out) >= limit {
						break
					}
				}
			}
			return out, nil
		}).AnyTimes()
	return s
}

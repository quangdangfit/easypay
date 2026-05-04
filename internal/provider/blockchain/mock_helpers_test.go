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

// txStore wires a MockTransactionRepository whose behaviour is backed by an
// in-memory map. Tests reach into byID to seed and assert state.
type txStore struct {
	byID map[string]*domain.Transaction
	mock *repomock.MockTransactionRepository
}

func newTxStore(t *testing.T, seed ...*domain.Transaction) *txStore {
	t.Helper()
	s := &txStore{byID: map[string]*domain.Transaction{}}
	for _, o := range seed {
		s.byID[o.OrderID] = o
	}
	s.mock = repomock.NewMockTransactionRepository(gomock.NewController(t))
	s.mock.EXPECT().Insert(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	s.mock.EXPECT().GetByMerchantOrderID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uint8, _ string, id string) (*domain.Transaction, error) {
			if o, ok := s.byID[id]; ok {
				return o, nil
			}
			return nil, repository.ErrNotFound
		}).AnyTimes()
	s.mock.EXPECT().GetByOrderIDAny(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, id string) (*domain.Transaction, error) {
			if o, ok := s.byID[id]; ok {
				return o, nil
			}
			return nil, repository.ErrNotFound
		}).AnyTimes()
	s.mock.EXPECT().GetByTransactionID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, repository.ErrNotFound).AnyTimes()
	s.mock.EXPECT().GetByPaymentIntentID(gomock.Any(), gomock.Any()).
		Return(nil, repository.ErrNotFound).AnyTimes()
	s.mock.EXPECT().UpdateStatus(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uint8, _ string, id string, st domain.TransactionStatus, _ string) error {
			if o, ok := s.byID[id]; ok {
				o.Status = st
				return nil
			}
			return repository.ErrNotFound
		}).AnyTimes()
	s.mock.EXPECT().UpdateCheckout(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).AnyTimes()
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
	byHash    map[string]*domain.OnchainTransaction
	createErr error
	mock      *repomock.MockOnchainTxRepository
}

func newPendingTxStore(t *testing.T) *pendingTxStore {
	t.Helper()
	s := &pendingTxStore{byHash: map[string]*domain.OnchainTransaction{}}
	s.mock = repomock.NewMockOnchainTxRepository(gomock.NewController(t))
	s.mock.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, tx *domain.OnchainTransaction) error {
			if s.createErr != nil {
				return s.createErr
			}
			if _, exists := s.byHash[tx.TxHash]; exists {
				return repository.ErrOnchainTxNotFound
			}
			s.byHash[tx.TxHash] = tx
			return nil
		}).AnyTimes()
	s.mock.EXPECT().GetByTxHash(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, hash string) (*domain.OnchainTransaction, error) {
			if tx, ok := s.byHash[hash]; ok {
				return tx, nil
			}
			return nil, repository.ErrOnchainTxNotFound
		}).AnyTimes()
	s.mock.EXPECT().UpdateConfirmations(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, hash string, conf uint64, st domain.OnchainTxStatus) error {
			tx, ok := s.byHash[hash]
			if !ok {
				return repository.ErrOnchainTxNotFound
			}
			tx.Confirmations = conf
			tx.Status = st
			return nil
		}).AnyTimes()
	s.mock.EXPECT().ListPendingByChain(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, chainID int64, limit int) ([]*domain.OnchainTransaction, error) {
			out := make([]*domain.OnchainTransaction, 0)
			for _, tx := range s.byHash {
				if tx.ChainID == chainID && tx.Status == domain.OnchainTxStatusPending {
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

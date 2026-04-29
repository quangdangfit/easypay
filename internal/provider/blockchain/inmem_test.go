package blockchain

import (
	"context"
	"sync"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/repository"
)

// memCursor is an in-memory CursorStore.
type memCursor struct {
	mu     sync.Mutex
	v      map[int64]uint64
	getErr error
	setErr error
}

func newMemCursor() *memCursor { return &memCursor{v: map[int64]uint64{}} }

func (c *memCursor) Get(ctx context.Context, chainID int64) (uint64, error) {
	if c.getErr != nil {
		return 0, c.getErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.v[chainID], nil
}

func (c *memCursor) Set(ctx context.Context, chainID int64, block uint64) error {
	if c.setErr != nil {
		return c.setErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.v[chainID] = block
	return nil
}

// memPendingTx is an in-memory PendingTxRepository.
type memPendingTx struct {
	mu        sync.Mutex
	byHash    map[string]*domain.PendingTx
	createErr error
}

func newMemPendingTx() *memPendingTx {
	return &memPendingTx{byHash: map[string]*domain.PendingTx{}}
}

func (m *memPendingTx) Create(ctx context.Context, tx *domain.PendingTx) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.byHash[tx.TxHash]; exists {
		return repository.ErrPendingTxNotFound // surface "duplicate" via any error
	}
	m.byHash[tx.TxHash] = tx
	return nil
}

func (m *memPendingTx) GetByTxHash(ctx context.Context, hash string) (*domain.PendingTx, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tx, ok := m.byHash[hash]; ok {
		return tx, nil
	}
	return nil, repository.ErrPendingTxNotFound
}

func (m *memPendingTx) UpdateConfirmations(ctx context.Context, hash string, confirmations uint64, status domain.PendingTxStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tx, ok := m.byHash[hash]; ok {
		tx.Confirmations = confirmations
		tx.Status = status
		return nil
	}
	return repository.ErrPendingTxNotFound
}

func (m *memPendingTx) ListPendingByChain(ctx context.Context, chainID int64, limit int) ([]*domain.PendingTx, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*domain.PendingTx, 0)
	for _, tx := range m.byHash {
		if tx.ChainID == chainID && tx.Status == domain.PendingTxStatusPending {
			out = append(out, tx)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

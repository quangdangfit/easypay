package blockchain

import (
	"context"
	"sync"
)

// memCursor is an in-memory CursorStore. Kept as an in-package fake because
// CursorStore lives in this package — using bcmock would create an import cycle.
type memCursor struct {
	mu     sync.Mutex
	v      map[int64]uint64
	getErr error
	setErr error
}

func newMemCursor() *memCursor { return &memCursor{v: map[int64]uint64{}} }

func (c *memCursor) Get(_ context.Context, chainID int64) (uint64, error) {
	if c.getErr != nil {
		return 0, c.getErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.v[chainID], nil
}

func (c *memCursor) Set(_ context.Context, chainID int64, block uint64) error {
	if c.setErr != nil {
		return c.setErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.v[chainID] = block
	return nil
}

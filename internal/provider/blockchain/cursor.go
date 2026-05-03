package blockchain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/quangdangfit/easypay/internal/repository"
)

type CursorStore interface {
	Get(ctx context.Context, chainID int64) (uint64, error)
	Set(ctx context.Context, chainID int64, block uint64) error
}

type mysqlCursor struct {
	db *sql.DB
}

// NewMySQLCursor returns a block-cursor store rooted on the control-plane
// pool. block_cursors is keyed by chain_id and shared across the whole
// service — there's no merchant_id to shard on.
func NewMySQLCursor(router repository.ShardRouter) CursorStore {
	return &mysqlCursor{db: router.Control()}
}

func (c *mysqlCursor) Get(ctx context.Context, chainID int64) (uint64, error) {
	var v uint64
	err := c.db.QueryRowContext(ctx, "SELECT last_block FROM block_cursors WHERE chain_id = ?", chainID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("cursor get: %w", err)
	}
	return v, nil
}

func (c *mysqlCursor) Set(ctx context.Context, chainID int64, block uint64) error {
	const q = `INSERT INTO block_cursors (chain_id, last_block) VALUES (?, ?)
	           ON DUPLICATE KEY UPDATE last_block = VALUES(last_block)`
	if _, err := c.db.ExecContext(ctx, q, chainID, block); err != nil {
		return fmt.Errorf("cursor set: %w", err)
	}
	return nil
}

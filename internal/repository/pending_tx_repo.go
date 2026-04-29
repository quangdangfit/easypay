package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"

	"github.com/quangdangfit/easypay/internal/domain"
)

var ErrPendingTxNotFound = errors.New("pending tx not found")

type PendingTxRepository interface {
	Create(ctx context.Context, tx *domain.PendingTx) error
	GetByTxHash(ctx context.Context, hash string) (*domain.PendingTx, error)
	UpdateConfirmations(ctx context.Context, hash string, confirmations uint64, status domain.PendingTxStatus) error
	ListPendingByChain(ctx context.Context, chainID int64, limit int) ([]*domain.PendingTx, error)
}

type pendingTxRepo struct {
	db *sql.DB
}

func NewPendingTxRepository(db *sql.DB) PendingTxRepository {
	return &pendingTxRepo{db: db}
}

const ptxCols = `id, tx_hash, block_number, order_id, payer, token, amount, chain_id,
		confirmations, required_confirm, status, created_at`

func (r *pendingTxRepo) Create(ctx context.Context, tx *domain.PendingTx) error {
	const q = `INSERT INTO pending_txs
		(tx_hash, block_number, order_id, payer, token, amount, chain_id,
		 confirmations, required_confirm, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	amt := "0"
	if tx.Amount != nil {
		amt = tx.Amount.String()
	}
	res, err := r.db.ExecContext(ctx, q,
		tx.TxHash, tx.BlockNumber, tx.OrderID, nullStr(tx.Payer), nullStr(tx.Token),
		amt, tx.ChainID, tx.Confirmations, tx.RequiredConfirm, string(tx.Status),
	)
	if err != nil {
		return fmt.Errorf("insert pending_tx: %w", err)
	}
	id, _ := res.LastInsertId()
	tx.ID = id
	return nil
}

func (r *pendingTxRepo) GetByTxHash(ctx context.Context, hash string) (*domain.PendingTx, error) {
	q := `SELECT ` + ptxCols + ` FROM pending_txs WHERE tx_hash = ? LIMIT 1`
	row := r.db.QueryRowContext(ctx, q, hash)
	return scanPendingTx(row)
}

func (r *pendingTxRepo) UpdateConfirmations(ctx context.Context, hash string, confirmations uint64, status domain.PendingTxStatus) error {
	const q = `UPDATE pending_txs SET confirmations = ?, status = ? WHERE tx_hash = ?`
	res, err := r.db.ExecContext(ctx, q, confirmations, string(status), hash)
	if err != nil {
		return fmt.Errorf("update pending_tx: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrPendingTxNotFound
	}
	return nil
}

func (r *pendingTxRepo) ListPendingByChain(ctx context.Context, chainID int64, limit int) ([]*domain.PendingTx, error) {
	q := `SELECT ` + ptxCols + ` FROM pending_txs
	      WHERE chain_id = ? AND status = 'pending'
	      ORDER BY block_number ASC LIMIT ?`
	rows, err := r.db.QueryContext(ctx, q, chainID, limit)
	if err != nil {
		return nil, fmt.Errorf("query pending_txs: %w", err)
	}
	defer rows.Close()
	out := make([]*domain.PendingTx, 0, limit)
	for rows.Next() {
		t, err := scanPendingTx(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanPendingTx(s scanner) (*domain.PendingTx, error) {
	var t domain.PendingTx
	var status, amt string
	var payer, token sql.NullString
	if err := s.Scan(
		&t.ID, &t.TxHash, &t.BlockNumber, &t.OrderID, &payer, &token,
		&amt, &t.ChainID, &t.Confirmations, &t.RequiredConfirm, &status, &t.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPendingTxNotFound
		}
		return nil, fmt.Errorf("scan pending_tx: %w", err)
	}
	t.Status = domain.PendingTxStatus(status)
	t.Payer = payer.String
	t.Token = token.String
	t.Amount = new(big.Int)
	if amt != "" {
		t.Amount.SetString(amt, 10)
	}
	return &t, nil
}

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"

	"github.com/quangdangfit/easypay/internal/domain"
)

var ErrOnchainTxNotFound = errors.New("onchain tx not found")

type OnchainTxRepository interface {
	Create(ctx context.Context, tx *domain.OnchainTransaction) error
	GetByTxHash(ctx context.Context, hash string) (*domain.OnchainTransaction, error)
	UpdateConfirmations(ctx context.Context, hash string, confirmations uint64, status domain.OnchainTxStatus) error
	ListPendingByChain(ctx context.Context, chainID int64, limit int) ([]*domain.OnchainTransaction, error)
}

type onchainTxRepo struct {
	db *sql.DB
}

// NewOnchainTxRepository builds the on-chain repo over the control-plane
// pool. onchain_transactions is keyed on tx_hash UNIQUE and lives globally
// on the control plane; merchant_id is denormalized from the matching
// `transactions` row at insert time to support per-merchant ops queries.
func NewOnchainTxRepository(router ShardRouter) OnchainTxRepository {
	return &onchainTxRepo{db: router.Control()}
}

const onchainCols = `id, tx_hash, block_number, merchant_id, order_id, payer, token, amount, chain_id,
		confirmations, required_confirm, status, created_at`

func (r *onchainTxRepo) Create(ctx context.Context, tx *domain.OnchainTransaction) error {
	const q = `INSERT INTO onchain_transactions
		(tx_hash, block_number, merchant_id, order_id, payer, token, amount, chain_id,
		 confirmations, required_confirm, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	amt := "0"
	if tx.Amount != nil {
		amt = tx.Amount.String()
	}
	res, err := r.db.ExecContext(ctx, q,
		tx.TxHash, tx.BlockNumber, tx.MerchantID, tx.OrderID, nullString(tx.Payer), nullString(tx.Token),
		amt, tx.ChainID, tx.Confirmations, tx.RequiredConfirm, string(tx.Status),
	)
	if err != nil {
		return fmt.Errorf("insert onchain_tx: %w", err)
	}
	id, _ := res.LastInsertId()
	tx.ID = id
	return nil
}

func (r *onchainTxRepo) GetByTxHash(ctx context.Context, hash string) (*domain.OnchainTransaction, error) {
	q := `SELECT ` + onchainCols + ` FROM onchain_transactions WHERE tx_hash = ? LIMIT 1`
	row := r.db.QueryRowContext(ctx, q, hash)
	return scanOnchainTx(row)
}

func (r *onchainTxRepo) UpdateConfirmations(ctx context.Context, hash string, confirmations uint64, status domain.OnchainTxStatus) error {
	const q = `UPDATE onchain_transactions SET confirmations = ?, status = ? WHERE tx_hash = ?`
	res, err := r.db.ExecContext(ctx, q, confirmations, string(status), hash)
	if err != nil {
		return fmt.Errorf("update onchain_tx: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrOnchainTxNotFound
	}
	return nil
}

func (r *onchainTxRepo) ListPendingByChain(ctx context.Context, chainID int64, limit int) ([]*domain.OnchainTransaction, error) {
	q := `SELECT ` + onchainCols + ` FROM onchain_transactions
	      WHERE chain_id = ? AND status = 'pending'
	      ORDER BY block_number ASC LIMIT ?`
	rows, err := r.db.QueryContext(ctx, q, chainID, limit)
	if err != nil {
		return nil, fmt.Errorf("query onchain_transactions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*domain.OnchainTransaction, 0, limit)
	for rows.Next() {
		t, err := scanOnchainTx(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanOnchainTx(s scanner) (*domain.OnchainTransaction, error) {
	var t domain.OnchainTransaction
	var status, amt string
	var payer, token sql.NullString
	if err := s.Scan(
		&t.ID, &t.TxHash, &t.BlockNumber, &t.MerchantID, &t.OrderID, &payer, &token,
		&amt, &t.ChainID, &t.Confirmations, &t.RequiredConfirm, &status, &t.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOnchainTxNotFound
		}
		return nil, fmt.Errorf("scan onchain_tx: %w", err)
	}
	t.Status = domain.OnchainTxStatus(status)
	t.Payer = payer.String
	t.Token = token.String
	t.Amount = new(big.Int)
	if amt != "" {
		t.Amount.SetString(amt, 10)
	}
	return &t, nil
}

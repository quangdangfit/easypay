package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/quangdangfit/easypay/internal/domain"
)

// ErrNotFound is returned by all read paths when a row doesn't exist.
var ErrNotFound = errors.New("order not found")

// ErrDuplicateOrder is returned by Insert when a row with the same
// (merchant_id, transaction_id) — equivalently (merchant_id, order_id) —
// already exists. Callers rely on this to implement sync-write idempotency:
// on duplicate, they fetch the existing row via GetByTransactionID and
// return its already-derived response.
var ErrDuplicateOrder = errors.New("duplicate order (merchant_id, transaction_id)")

// OrderRepository is the port the service layer depends on. It speaks to a
// single `transactions` table; logical sharding lives on
// `merchants.shard_index` and is not in the read/write path today.
type OrderRepository interface {
	Insert(ctx context.Context, o *domain.Order) error
	GetByTransactionID(ctx context.Context, merchantID, transactionID string) (*domain.Order, error)
	GetByMerchantOrderID(ctx context.Context, merchantID, orderID string) (*domain.Order, error)
	// GetByOrderIDAny is a global lookup keyed only on order_id. Used by the
	// blockchain confirmation path, which doesn't know merchant_id ahead of
	// time (the smart-contract event only carries order_id). Returns the
	// first matching row — merchants that route on-chain payments should
	// use globally unique order_ids (UUID/ULID) to avoid collisions.
	GetByOrderIDAny(ctx context.Context, orderID string) (*domain.Order, error)
	GetByPaymentIntentID(ctx context.Context, pi string) (*domain.Order, error)
	UpdateStatus(ctx context.Context, merchantID, orderID string, status domain.OrderStatus, stripePaymentIntentID string) error
	UpdateCheckout(ctx context.Context, merchantID, orderID, stripeSessionID, stripePaymentIntentID string) error
	GetPendingBefore(ctx context.Context, before time.Time, limit int) ([]*domain.Order, error)
}

type orderRepo struct {
	db *sql.DB
}

// NewOrderRepository constructs an OrderRepository over a single MySQL pool.
func NewOrderRepository(db *sql.DB) OrderRepository {
	return &orderRepo{db: db}
}

const insertCols = "(merchant_id, transaction_id, order_id, amount, currency_code, status, payment_method, stripe_pi_id, stripe_session, created_at, updated_at)"

const insertPlaceholders = "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"

const selectCols = "merchant_id, transaction_id, order_id, amount, currency_code, status, payment_method, stripe_pi_id, stripe_session, created_at, updated_at"

func (r *orderRepo) Insert(ctx context.Context, o *domain.Order) error {
	merch := []byte(o.MerchantID)
	if len(merch) > 16 {
		return fmt.Errorf("merchant_id too long: %d bytes (max 16)", len(merch))
	}
	txn, err := decodeHex16(o.TransactionID)
	if err != nil {
		return fmt.Errorf("transaction_id: %w", err)
	}
	if err := domain.ValidateOrderID(o.OrderID); err != nil {
		return fmt.Errorf("order_id: %w", err)
	}
	if o.Amount < 0 {
		return fmt.Errorf("amount must be >= 0, got %d", o.Amount)
	}
	cur, err := encodeCurrency(o.Currency)
	if err != nil {
		return err
	}
	st, err := encodeStatus(o.Status)
	if err != nil {
		return err
	}
	pm := encodeMethod(o.PaymentMethod)

	now := time.Now().UTC()
	if o.CreatedAt.IsZero() {
		o.CreatedAt = now
	}
	o.UpdatedAt = now
	createdSec := unixToU32(o.CreatedAt)
	updatedSec := unixToU32(o.UpdatedAt)

	q := "INSERT INTO transactions " + insertCols + " VALUES " + insertPlaceholders
	_, err = r.db.ExecContext(ctx, q,
		merch, txn, o.OrderID, uint64(o.Amount), cur, st, pm,
		nullBytes(o.StripePaymentIntentID), nullBytes(o.StripeSessionID),
		createdSec, updatedSec,
	)
	if err != nil {
		if isDuplicateKeyErr(err) {
			return ErrDuplicateOrder
		}
		return fmt.Errorf("insert order: %w", err)
	}
	return nil
}

func (r *orderRepo) GetByTransactionID(ctx context.Context, merchantID, transactionID string) (*domain.Order, error) {
	merch := []byte(merchantID)
	txn, err := decodeHex16(transactionID)
	if err != nil {
		return nil, fmt.Errorf("transaction_id: %w", err)
	}
	q := "SELECT " + selectCols + " FROM transactions WHERE merchant_id = ? AND transaction_id = ? LIMIT 1"
	row := r.db.QueryRowContext(ctx, q, merch, txn)
	return scanOrder(row)
}

func (r *orderRepo) GetByMerchantOrderID(ctx context.Context, merchantID, orderID string) (*domain.Order, error) {
	if err := domain.ValidateOrderID(orderID); err != nil {
		return nil, err
	}
	merch := []byte(merchantID)
	q := "SELECT " + selectCols + " FROM transactions WHERE merchant_id = ? AND order_id = ? LIMIT 1"
	row := r.db.QueryRowContext(ctx, q, merch, orderID)
	return scanOrder(row)
}

func (r *orderRepo) GetByOrderIDAny(ctx context.Context, orderID string) (*domain.Order, error) {
	if err := domain.ValidateOrderID(orderID); err != nil {
		return nil, err
	}
	q := "SELECT " + selectCols + " FROM transactions WHERE order_id = ? LIMIT 1"
	row := r.db.QueryRowContext(ctx, q, orderID)
	return scanOrder(row)
}

// GetByPaymentIntentID looks up by Stripe PI id. Used on the refund recovery
// path when a Stripe charge.refunded event lacks our metadata. Indexed by
// idx_pi_id.
func (r *orderRepo) GetByPaymentIntentID(ctx context.Context, pi string) (*domain.Order, error) {
	if pi == "" {
		return nil, ErrNotFound
	}
	q := "SELECT " + selectCols + " FROM transactions WHERE stripe_pi_id = ? LIMIT 1"
	row := r.db.QueryRowContext(ctx, q, []byte(pi))
	return scanOrder(row)
}

func (r *orderRepo) UpdateStatus(ctx context.Context, merchantID, orderID string, status domain.OrderStatus, stripePaymentIntentID string) error {
	if err := domain.ValidateOrderID(orderID); err != nil {
		return err
	}
	st, err := encodeStatus(status)
	if err != nil {
		return err
	}
	merch := []byte(merchantID)
	now := unixToU32(time.Now().UTC())
	// COALESCE(NULLIF(?, ''), ...) keeps the existing PI id if the caller
	// passes "" (e.g. webhook for charge.refunded where the PI is already set).
	q := `UPDATE transactions SET
			status = ?, updated_at = ?,
			stripe_pi_id = COALESCE(NULLIF(?, _binary''), stripe_pi_id)
		 WHERE merchant_id = ? AND order_id = ?`
	res, err := r.db.ExecContext(ctx, q, st, now, []byte(stripePaymentIntentID), merch, orderID)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *orderRepo) UpdateCheckout(ctx context.Context, merchantID, orderID, sessionID, piID string) error {
	if err := domain.ValidateOrderID(orderID); err != nil {
		return err
	}
	merch := []byte(merchantID)
	now := unixToU32(time.Now().UTC())
	q := `UPDATE transactions SET
			stripe_session = COALESCE(NULLIF(?, _binary''), stripe_session),
			stripe_pi_id   = COALESCE(NULLIF(?, _binary''), stripe_pi_id),
			updated_at     = ?
		 WHERE merchant_id = ? AND order_id = ?`
	res, err := r.db.ExecContext(ctx, q, []byte(sessionID), []byte(piID), now, merch, orderID)
	if err != nil {
		return fmt.Errorf("update checkout: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetPendingBefore returns up to limit orders in created/pending state whose
// created_at is older than `before`. Used by the reconciliation cron.
func (r *orderRepo) GetPendingBefore(ctx context.Context, before time.Time, limit int) ([]*domain.Order, error) {
	if limit <= 0 {
		return nil, nil
	}
	cutoff := unixToU32(before.UTC())
	created, _ := encodeStatus(domain.OrderStatusCreated)
	pending, _ := encodeStatus(domain.OrderStatusPending)

	q := "SELECT " + selectCols + ` FROM transactions WHERE status IN (?, ?) AND created_at < ? ORDER BY created_at ASC LIMIT ?`
	rows, err := r.db.QueryContext(ctx, q, created, pending, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("query pending: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*domain.Order, 0, limit)
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanOrder(s scanner) (*domain.Order, error) {
	var (
		merch        []byte
		txn          []byte
		ord          string
		amount       uint64
		currencyCode uint16
		status       uint8
		method       uint8
		piID         []byte
		sessID       []byte
		createdSec   uint32
		updatedSec   uint32
	)
	if err := s.Scan(&merch, &txn, &ord, &amount, &currencyCode, &status, &method, &piID, &sessID, &createdSec, &updatedSec); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan order: %w", err)
	}
	cur, err := decodeCurrency(currencyCode)
	if err != nil {
		return nil, err
	}
	st, err := decodeStatus(status)
	if err != nil {
		return nil, err
	}
	return &domain.Order{
		MerchantID:            string(merch),
		TransactionID:         hexLower(txn),
		OrderID:               ord,
		Amount:                int64(amount),
		Currency:              cur,
		Status:                st,
		PaymentMethod:         decodeMethod(method),
		StripeSessionID:       string(sessID),
		StripePaymentIntentID: string(piID),
		CreatedAt:             time.Unix(int64(createdSec), 0).UTC(),
		UpdatedAt:             time.Unix(int64(updatedSec), 0).UTC(),
	}, nil
}

// nullBytes returns a typed nil for the driver if s is empty so the column
// stores NULL (not an empty BLOB).
func nullBytes(s string) any {
	if s == "" {
		return nil
	}
	return []byte(s)
}

// nullString returns a typed nil for the driver if s is empty (used by
// onchain_tx_repo for VARCHAR columns).
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// unixToU32 truncates a Unix timestamp to uint32. Negative values clamp to
// 0 (pre-epoch is impossible in practice but keeps gosec G115 quiet) and
// values past 2106 wrap, which the schema accepts: at that point we'll
// migrate to BIGINT or beyond.
func unixToU32(t time.Time) uint32 {
	v := t.Unix()
	if v < 0 {
		return 0
	}
	if v > int64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(v)
}

func hexLower(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0x0f]
	}
	return string(out)
}

// isDuplicateKeyErr matches MySQL ER_DUP_ENTRY (1062) without importing the
// driver. The text format is stable across go-sql-driver versions.
func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "Error 1062") || strings.Contains(s, "Duplicate entry")
}

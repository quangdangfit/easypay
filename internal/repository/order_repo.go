package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/quangdangfit/easypay/internal/domain"
)

// ErrNotFound is returned by all read paths when a row doesn't exist.
var ErrNotFound = errors.New("order not found")

// ErrDuplicateOrder is returned by Insert when a row with the same
// (merchant_id, transaction_id) already exists. Callers rely on this to
// implement sync-write idempotency: on duplicate, they fetch the existing
// row via GetByTransactionID and return its already-derived response.
var ErrDuplicateOrder = errors.New("duplicate order (merchant_id, transaction_id)")

// OrderRepository is the port the service layer depends on. The single
// public implementation routes a row to one of N=ShardCount physical
// `orders_<shard>` tables, each backed by its own *sql.DB pool.
type OrderRepository interface {
	Insert(ctx context.Context, o *domain.Order) error
	GetByTransactionID(ctx context.Context, merchantID, transactionID string) (*domain.Order, error)
	GetByOrderID(ctx context.Context, orderID string) (*domain.Order, error)
	GetByPaymentIntentID(ctx context.Context, pi string) (*domain.Order, error)
	UpdateStatus(ctx context.Context, orderID string, status domain.OrderStatus, stripePaymentIntentID string) error
	UpdateCheckout(ctx context.Context, orderID, stripeSessionID, stripePaymentIntentID string) error
	GetPendingBefore(ctx context.Context, before time.Time, limit int) ([]*domain.Order, error)
}

type shardedOrderRepo struct {
	shards [ShardCount]*sql.DB
}

// NewShardedOrderRepository wires a sharded repo over a slice of N=ShardCount
// pools. For dev/testing where all shards live on a single MySQL instance,
// pass the same *sql.DB ShardCount times.
func NewShardedOrderRepository(shards []*sql.DB) (OrderRepository, error) {
	if len(shards) != ShardCount {
		return nil, fmt.Errorf("sharded repo: want %d pools, got %d", ShardCount, len(shards))
	}
	r := &shardedOrderRepo{}
	for i, db := range shards {
		if db == nil {
			return nil, fmt.Errorf("sharded repo: pool[%d] is nil", i)
		}
		r.shards[i] = db
	}
	return r, nil
}

// NewOrderRepository is a convenience for the common single-MySQL deployment
// where all 16 shard tables live in the same database. Every call routes by
// table name; the underlying *sql.DB is shared.
func NewOrderRepository(db *sql.DB) OrderRepository {
	r := &shardedOrderRepo{}
	for i := range r.shards {
		r.shards[i] = db
	}
	return r
}

const insertCols = "(merchant_id, transaction_id, order_id, amount, currency_code, status, payment_method, stripe_pi_id, stripe_session, created_at, updated_at)"

const insertPlaceholders = "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"

const selectCols = "merchant_id, transaction_id, order_id, amount, currency_code, status, payment_method, stripe_pi_id, stripe_session, created_at, updated_at"

func (r *shardedOrderRepo) Insert(ctx context.Context, o *domain.Order) error {
	merch := []byte(o.MerchantID)
	if len(merch) > 16 {
		return fmt.Errorf("merchant_id too long: %d bytes (max 16)", len(merch))
	}
	txn, err := decodeHex16(o.TransactionID)
	if err != nil {
		return fmt.Errorf("transaction_id: %w", err)
	}
	ord, err := decodeHex12(o.OrderID)
	if err != nil {
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

	shard := ShardOf(o.MerchantID)
	// #nosec G202 -- table name comes from ShardTable() over a fixed enum.
	q := "INSERT INTO " + ShardTable(shard) + " " + insertCols + " VALUES " + insertPlaceholders
	_, err = r.shards[shard].ExecContext(ctx, q,
		merch, txn, ord, uint64(o.Amount), cur, st, pm,
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

func (r *shardedOrderRepo) GetByTransactionID(ctx context.Context, merchantID, transactionID string) (*domain.Order, error) {
	merch := []byte(merchantID)
	txn, err := decodeHex16(transactionID)
	if err != nil {
		return nil, fmt.Errorf("transaction_id: %w", err)
	}
	shard := ShardOf(merchantID)
	// #nosec G202 -- table name from fixed enum.
	q := "SELECT " + selectCols + " FROM " + ShardTable(shard) + " WHERE merchant_id = ? AND transaction_id = ? LIMIT 1"
	row := r.shards[shard].QueryRowContext(ctx, q, merch, txn)
	return scanOrder(row)
}

func (r *shardedOrderRepo) GetByOrderID(ctx context.Context, orderID string) (*domain.Order, error) {
	if err := domain.ValidateOrderID(orderID); err != nil {
		return nil, err
	}
	shard, err := domain.ShardOfOrderID(orderID)
	if err != nil {
		return nil, err
	}
	if shard >= ShardCount {
		return nil, fmt.Errorf("order_id shard %d out of range", shard)
	}
	ord, err := decodeHex12(orderID)
	if err != nil {
		return nil, err
	}
	// #nosec G202 -- table name from fixed enum.
	q := "SELECT " + selectCols + " FROM " + ShardTable(shard) + " WHERE order_id = ? LIMIT 1"
	row := r.shards[shard].QueryRowContext(ctx, q, ord)
	return scanOrder(row)
}

// GetByPaymentIntentID fans out across all shards. Used only on the refund
// recovery path when a Stripe charge.refunded event lacks our metadata.
// Latency = max(shards). Acceptable because this isn't on the hot path.
func (r *shardedOrderRepo) GetByPaymentIntentID(ctx context.Context, pi string) (*domain.Order, error) {
	if pi == "" {
		return nil, ErrNotFound
	}
	type result struct {
		o   *domain.Order
		err error
	}
	out := make(chan result, ShardCount)
	var wg sync.WaitGroup
	for i := uint8(0); i < ShardCount; i++ {
		wg.Add(1)
		go func(shard uint8) {
			defer wg.Done()
			// #nosec G202 -- table name from fixed enum.
			q := "SELECT " + selectCols + " FROM " + ShardTable(shard) + " WHERE stripe_pi_id = ? LIMIT 1"
			row := r.shards[shard].QueryRowContext(ctx, q, []byte(pi))
			o, err := scanOrder(row)
			out <- result{o, err}
		}(i)
	}
	wg.Wait()
	close(out)
	for res := range out {
		if res.err == nil && res.o != nil {
			return res.o, nil
		}
		if res.err != nil && !errors.Is(res.err, ErrNotFound) {
			return nil, res.err
		}
	}
	return nil, ErrNotFound
}

func (r *shardedOrderRepo) UpdateStatus(ctx context.Context, orderID string, status domain.OrderStatus, stripePaymentIntentID string) error {
	if err := domain.ValidateOrderID(orderID); err != nil {
		return err
	}
	shard, err := domain.ShardOfOrderID(orderID)
	if err != nil {
		return err
	}
	if shard >= ShardCount {
		return fmt.Errorf("order_id shard %d out of range", shard)
	}
	ord, err := decodeHex12(orderID)
	if err != nil {
		return err
	}
	st, err := encodeStatus(status)
	if err != nil {
		return err
	}
	now := unixToU32(time.Now().UTC())
	// COALESCE(NULLIF(?, ''), ...) keeps the existing PI id if the caller
	// passes "" (e.g. webhook for charge.refunded where the PI is already set).
	// #nosec G202 -- table name from fixed enum.
	q := "UPDATE " + ShardTable(shard) + ` SET
			status = ?, updated_at = ?,
			stripe_pi_id = COALESCE(NULLIF(?, _binary''), stripe_pi_id)
		 WHERE order_id = ?`
	res, err := r.shards[shard].ExecContext(ctx, q, st, now, []byte(stripePaymentIntentID), ord)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *shardedOrderRepo) UpdateCheckout(ctx context.Context, orderID, sessionID, piID string) error {
	if err := domain.ValidateOrderID(orderID); err != nil {
		return err
	}
	shard, err := domain.ShardOfOrderID(orderID)
	if err != nil {
		return err
	}
	if shard >= ShardCount {
		return fmt.Errorf("order_id shard %d out of range", shard)
	}
	ord, err := decodeHex12(orderID)
	if err != nil {
		return err
	}
	now := unixToU32(time.Now().UTC())
	// #nosec G202 -- table name from fixed enum.
	q := "UPDATE " + ShardTable(shard) + ` SET
			stripe_session = COALESCE(NULLIF(?, _binary''), stripe_session),
			stripe_pi_id   = COALESCE(NULLIF(?, _binary''), stripe_pi_id),
			updated_at     = ?
		 WHERE order_id = ?`
	res, err := r.shards[shard].ExecContext(ctx, q, []byte(sessionID), []byte(piID), now, ord)
	if err != nil {
		return fmt.Errorf("update checkout: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetPendingBefore fans out across shards for the reconciliation cron.
// Each shard returns up to limit; we aggregate and trim to limit overall.
func (r *shardedOrderRepo) GetPendingBefore(ctx context.Context, before time.Time, limit int) ([]*domain.Order, error) {
	if limit <= 0 {
		return nil, nil
	}
	cutoff := unixToU32(before.UTC())
	created, _ := encodeStatus(domain.OrderStatusCreated)
	pending, _ := encodeStatus(domain.OrderStatusPending)

	type result struct {
		rows []*domain.Order
		err  error
	}
	out := make(chan result, ShardCount)
	var wg sync.WaitGroup
	for i := uint8(0); i < ShardCount; i++ {
		wg.Add(1)
		go func(shard uint8) {
			defer wg.Done()
			// #nosec G202 -- table name from fixed enum.
			q := "SELECT " + selectCols + " FROM " + ShardTable(shard) + ` WHERE status IN (?, ?) AND created_at < ? ORDER BY created_at ASC LIMIT ?`
			rows, err := r.shards[shard].QueryContext(ctx, q, created, pending, cutoff, limit)
			if err != nil {
				out <- result{err: fmt.Errorf("shard %d query: %w", shard, err)}
				return
			}
			defer func() { _ = rows.Close() }()
			batch := make([]*domain.Order, 0, limit)
			for rows.Next() {
				o, err := scanOrder(rows)
				if err != nil {
					out <- result{err: fmt.Errorf("shard %d scan: %w", shard, err)}
					return
				}
				batch = append(batch, o)
			}
			out <- result{rows: batch, err: rows.Err()}
		}(i)
	}
	wg.Wait()
	close(out)
	all := make([]*domain.Order, 0, limit)
	for res := range out {
		if res.err != nil {
			return nil, res.err
		}
		all = append(all, res.rows...)
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanOrder(s scanner) (*domain.Order, error) {
	var (
		merch        []byte
		txn          []byte
		ord          []byte
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
		OrderID:               hexLower(ord),
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
// pending_tx_repo for VARCHAR columns).
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

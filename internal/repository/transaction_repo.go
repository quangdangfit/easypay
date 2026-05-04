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
var ErrNotFound = errors.New("transaction not found")

// ErrDuplicateTransaction is returned by Insert when a row with the same
// transaction_id already exists. Because transaction_id is derived
// deterministically from sha256(merchant_id || ':' || order_id)[:16],
// retries with the same (merchant_id, order_id) collapse onto the same
// row via the UNIQUE on transaction_id. Callers rely on this to implement
// sync-write idempotency: on duplicate, they fetch the existing row via
// GetByTransactionID and return its already-derived response.
var ErrDuplicateTransaction = errors.New("duplicate transaction (transaction_id)")

// TransactionRepository is the port the service layer depends on. It speaks
// to the `transactions` table. Methods that have a merchant context take
// the merchant's logical shard index and route via ShardRouter.For; the
// few global lookups (GetByOrderIDAny, GetByPaymentIntentID,
// GetPendingBefore) scatter-gather across every physical pool.
type TransactionRepository interface {
	// Insert routes via t.ShardIndex. Callers MUST set t.ShardIndex before
	// calling — the field is copied from merchant.ShardIndex.
	Insert(ctx context.Context, t *domain.Transaction) error
	GetByTransactionID(ctx context.Context, shardIdx uint8, merchantID, transactionID string) (*domain.Transaction, error)
	GetByMerchantOrderID(ctx context.Context, shardIdx uint8, merchantID, orderID string) (*domain.Transaction, error)
	UpdateStatus(ctx context.Context, shardIdx uint8, merchantID, orderID string, status domain.TransactionStatus, stripePaymentIntentID string) error
	UpdateCheckout(ctx context.Context, shardIdx uint8, merchantID, orderID, stripeSessionID, stripePaymentIntentID string) error
	// GetByOrderIDAny is a global lookup keyed only on order_id. Used by the
	// blockchain confirmation path, which doesn't know merchant_id ahead of
	// time (the smart-contract event only carries order_id). Scatter-gathers
	// across every physical shard and returns the first match. Merchants
	// that route on-chain payments should use globally unique order_ids
	// (UUID/ULID) to avoid collisions across their own merchant_id.
	GetByOrderIDAny(ctx context.Context, orderID string) (*domain.Transaction, error)
	// GetByPaymentIntentID looks up by Stripe PI id across every physical
	// shard. Used on the refund recovery path when a Stripe charge.refunded
	// event lacks our metadata.
	GetByPaymentIntentID(ctx context.Context, pi string) (*domain.Transaction, error)
	// GetPendingBefore scans every physical shard for stuck transactions.
	// Returned rows carry their physical-pool shard index so follow-up
	// writes can route deterministically.
	GetPendingBefore(ctx context.Context, before time.Time, limit int) ([]*domain.Transaction, error)
}

type transactionRepo struct {
	router ShardRouter
}

// NewTransactionRepository constructs a TransactionRepository over a ShardRouter.
func NewTransactionRepository(router ShardRouter) TransactionRepository {
	return &transactionRepo{router: router}
}

const insertCols = "(merchant_id, transaction_id, order_id, amount, currency_code, status, payment_method, stripe_pi_id, stripe_session, created_at, updated_at)"

const insertPlaceholders = "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"

const selectCols = "merchant_id, transaction_id, order_id, amount, currency_code, status, payment_method, stripe_pi_id, stripe_session, created_at, updated_at"

func (r *transactionRepo) Insert(ctx context.Context, t *domain.Transaction) error {
	merch := []byte(t.MerchantID)
	if len(merch) > 16 {
		return fmt.Errorf("merchant_id too long: %d bytes (max 16)", len(merch))
	}
	txn, err := decodeHex16(t.TransactionID)
	if err != nil {
		return fmt.Errorf("transaction_id: %w", err)
	}
	if err := domain.ValidateOrderID(t.OrderID); err != nil {
		return fmt.Errorf("order_id: %w", err)
	}
	if t.Amount < 0 {
		return fmt.Errorf("amount must be >= 0, got %d", t.Amount)
	}
	cur, err := encodeCurrency(t.Currency)
	if err != nil {
		return err
	}
	st, err := encodeStatus(t.Status)
	if err != nil {
		return err
	}
	pm := encodeMethod(t.PaymentMethod)

	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now

	db, err := r.router.For(t.ShardIndex)
	if err != nil {
		return fmt.Errorf("route: %w", err)
	}

	q := "INSERT INTO transactions " + insertCols + " VALUES " + insertPlaceholders
	_, err = db.ExecContext(ctx, q,
		merch, txn, t.OrderID, uint64(t.Amount), cur, st, pm,
		nullBytes(t.StripePaymentIntentID), nullBytes(t.StripeSessionID),
		t.CreatedAt, t.UpdatedAt,
	)
	if err != nil {
		if isDuplicateKeyErr(err) {
			return ErrDuplicateTransaction
		}
		return fmt.Errorf("insert transaction: %w", err)
	}
	return nil
}

func (r *transactionRepo) GetByTransactionID(ctx context.Context, shardIdx uint8, merchantID, transactionID string) (*domain.Transaction, error) {
	db, err := r.router.For(shardIdx)
	if err != nil {
		return nil, fmt.Errorf("route: %w", err)
	}
	merch := []byte(merchantID)
	txn, err := decodeHex16(transactionID)
	if err != nil {
		return nil, fmt.Errorf("transaction_id: %w", err)
	}
	q := "SELECT " + selectCols + " FROM transactions WHERE merchant_id = ? AND transaction_id = ? LIMIT 1"
	row := db.QueryRowContext(ctx, q, merch, txn)
	t, err := scanTransaction(row)
	if err != nil {
		return nil, err
	}
	t.ShardIndex = shardIdx
	return t, nil
}

func (r *transactionRepo) GetByMerchantOrderID(ctx context.Context, shardIdx uint8, merchantID, orderID string) (*domain.Transaction, error) {
	if err := domain.ValidateOrderID(orderID); err != nil {
		return nil, err
	}
	db, err := r.router.For(shardIdx)
	if err != nil {
		return nil, fmt.Errorf("route: %w", err)
	}
	merch := []byte(merchantID)
	q := "SELECT " + selectCols + " FROM transactions WHERE merchant_id = ? AND order_id = ? LIMIT 1"
	row := db.QueryRowContext(ctx, q, merch, orderID)
	t, err := scanTransaction(row)
	if err != nil {
		return nil, err
	}
	t.ShardIndex = shardIdx
	return t, nil
}

func (r *transactionRepo) GetByOrderIDAny(ctx context.Context, orderID string) (*domain.Transaction, error) {
	if err := domain.ValidateOrderID(orderID); err != nil {
		return nil, err
	}
	q := "SELECT " + selectCols + " FROM transactions WHERE order_id = ? LIMIT 1"
	return r.scatterFirst(ctx, q, orderID)
}

// GetByPaymentIntentID looks up by Stripe PI id across every physical
// shard. Used on the refund recovery path when a Stripe charge.refunded
// event lacks our metadata. Indexed by idx_pi_id on each shard.
func (r *transactionRepo) GetByPaymentIntentID(ctx context.Context, pi string) (*domain.Transaction, error) {
	if pi == "" {
		return nil, ErrNotFound
	}
	q := "SELECT " + selectCols + " FROM transactions WHERE stripe_pi_id = ? LIMIT 1"
	return r.scatterFirst(ctx, q, []byte(pi))
}

// scatterFirst runs a single-row query against every physical shard in
// parallel and returns the first non-error match. Used for the two
// no-merchant-context lookup paths. The returned transaction's ShardIndex
// is set to the *physical* pool index of the shard that matched, expanded
// to a logical index — for follow-up writes we need a logical index, so we
// rebuild one. With multiple logical shards mapping to the same physical
// pool, any logical index in that range routes back to the same DB; we
// pick the first such logical index.
func (r *transactionRepo) scatterFirst(ctx context.Context, q string, args ...any) (*domain.Transaction, error) {
	pools := r.router.All()
	logicalForPhysical := r.physicalToFirstLogical(len(pools))
	type result struct {
		txn *domain.Transaction
		idx int
		err error
	}
	results := make(chan result, len(pools))
	for i, db := range pools {
		go func(i int, db *sql.DB) {
			row := db.QueryRowContext(ctx, q, args...)
			t, err := scanTransaction(row)
			results <- result{txn: t, idx: i, err: err}
		}(i, db)
	}

	var firstErr error
	misses := 0
	for range pools {
		res := <-results
		if res.err != nil {
			if errors.Is(res.err, ErrNotFound) {
				misses++
				continue
			}
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		// Found — annotate with the matching shard's logical index so
		// follow-up writes route correctly. We don't drain the remaining
		// goroutines; the channel is buffered, they exit on their own.
		res.txn.ShardIndex = logicalForPhysical[res.idx]
		return res.txn, nil
	}
	if misses == len(pools) {
		return nil, ErrNotFound
	}
	return nil, firstErr
}

// physicalToFirstLogical inverts the range-partition mapping to find a
// logical-shard index that routes to physical pool `i`. Any logical index
// in the same partition works, so we pick the first one. Cheap to compute
// on-demand; not cached because the router is fixed at startup.
func (r *transactionRepo) physicalToFirstLogical(physicalCount int) []uint8 {
	logicalCount := r.router.LogicalCount()
	out := make([]uint8, physicalCount)
	per := int(logicalCount) / physicalCount
	for i := 0; i < physicalCount; i++ {
		// uint8 cast is safe: per <= logicalCount <= 255 and i < physicalCount <= logicalCount.
		out[i] = uint8(i * per) // #nosec G115
	}
	return out
}

func (r *transactionRepo) UpdateStatus(ctx context.Context, shardIdx uint8, merchantID, orderID string, status domain.TransactionStatus, stripePaymentIntentID string) error {
	if err := domain.ValidateOrderID(orderID); err != nil {
		return err
	}
	st, err := encodeStatus(status)
	if err != nil {
		return err
	}
	db, err := r.router.For(shardIdx)
	if err != nil {
		return fmt.Errorf("route: %w", err)
	}
	merch := []byte(merchantID)
	now := time.Now().UTC()
	// COALESCE(NULLIF(?, ''), ...) keeps the existing PI id if the caller
	// passes "" (e.g. webhook for charge.refunded where the PI is already set).
	q := `UPDATE transactions SET
			status = ?, updated_at = ?,
			stripe_pi_id = COALESCE(NULLIF(?, _binary''), stripe_pi_id)
		 WHERE merchant_id = ? AND order_id = ?`
	res, err := db.ExecContext(ctx, q, st, now, []byte(stripePaymentIntentID), merch, orderID)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *transactionRepo) UpdateCheckout(ctx context.Context, shardIdx uint8, merchantID, orderID, sessionID, piID string) error {
	if err := domain.ValidateOrderID(orderID); err != nil {
		return err
	}
	db, err := r.router.For(shardIdx)
	if err != nil {
		return fmt.Errorf("route: %w", err)
	}
	merch := []byte(merchantID)
	now := time.Now().UTC()
	q := `UPDATE transactions SET
			stripe_session = COALESCE(NULLIF(?, _binary''), stripe_session),
			stripe_pi_id   = COALESCE(NULLIF(?, _binary''), stripe_pi_id),
			updated_at     = ?
		 WHERE merchant_id = ? AND order_id = ?`
	res, err := db.ExecContext(ctx, q, []byte(sessionID), []byte(piID), now, merch, orderID)
	if err != nil {
		return fmt.Errorf("update checkout: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetPendingBefore returns up to `limit` transactions in created/pending
// state whose created_at is older than `before`, scanning every physical
// shard in parallel. The total cap is `limit` across all shards combined;
// per-shard we ask for `limit` rows so we never miss a stuck transaction
// if all stragglers happen to live on one shard.
func (r *transactionRepo) GetPendingBefore(ctx context.Context, before time.Time, limit int) ([]*domain.Transaction, error) {
	if limit <= 0 {
		return nil, nil
	}
	cutoff := before.UTC()
	created, _ := encodeStatus(domain.TransactionStatusCreated)
	pending, _ := encodeStatus(domain.TransactionStatusPending)
	q := "SELECT " + selectCols + ` FROM transactions WHERE status IN (?, ?) AND created_at < ? ORDER BY created_at ASC LIMIT ?`

	pools := r.router.All()
	logicalForPhysical := r.physicalToFirstLogical(len(pools))

	type shardResult struct {
		txns []*domain.Transaction
		idx  int
		err  error
	}
	results := make(chan shardResult, len(pools))

	var wg sync.WaitGroup
	for i, db := range pools {
		wg.Add(1)
		go func(i int, db *sql.DB) {
			defer wg.Done()
			rows, err := db.QueryContext(ctx, q, created, pending, cutoff, limit)
			if err != nil {
				results <- shardResult{idx: i, err: fmt.Errorf("query pending shard %d: %w", i, err)}
				return
			}
			defer func() { _ = rows.Close() }()
			out := make([]*domain.Transaction, 0, limit)
			for rows.Next() {
				t, err := scanTransaction(rows)
				if err != nil {
					results <- shardResult{idx: i, err: fmt.Errorf("scan shard %d: %w", i, err)}
					return
				}
				t.ShardIndex = logicalForPhysical[i]
				out = append(out, t)
			}
			if err := rows.Err(); err != nil {
				results <- shardResult{idx: i, err: err}
				return
			}
			results <- shardResult{idx: i, txns: out}
		}(i, db)
	}
	wg.Wait()
	close(results)

	all := make([]*domain.Transaction, 0, limit)
	var firstErr error
	for res := range results {
		if res.err != nil {
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		all = append(all, res.txns...)
	}
	if firstErr != nil {
		return nil, firstErr
	}
	// Stable order across reconciler ticks: oldest first, then by
	// transaction_id as a tiebreaker so the same row doesn't keep flapping
	// between shards on identical created_at seconds.
	sortTransactionsByCreatedAt(all)
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func sortTransactionsByCreatedAt(txns []*domain.Transaction) {
	// Tiny n (≤ batch size, default 500). Insertion sort is plenty here
	// and keeps the dependency surface zero. Stable on equal keys.
	for i := 1; i < len(txns); i++ {
		j := i
		for j > 0 {
			a, b := txns[j-1], txns[j]
			if a.CreatedAt.Before(b.CreatedAt) {
				break
			}
			if a.CreatedAt.Equal(b.CreatedAt) && a.TransactionID <= b.TransactionID {
				break
			}
			txns[j-1], txns[j] = txns[j], txns[j-1]
			j--
		}
	}
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTransaction(s scanner) (*domain.Transaction, error) {
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
		createdAt    time.Time
		updatedAt    time.Time
	)
	if err := s.Scan(&merch, &txn, &ord, &amount, &currencyCode, &status, &method, &piID, &sessID, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan transaction: %w", err)
	}
	cur, err := decodeCurrency(currencyCode)
	if err != nil {
		return nil, err
	}
	st, err := decodeStatus(status)
	if err != nil {
		return nil, err
	}
	return &domain.Transaction{
		MerchantID:            string(merch),
		TransactionID:         hexLower(txn),
		OrderID:               ord,
		Amount:                int64(amount),
		Currency:              cur,
		Status:                st,
		PaymentMethod:         decodeMethod(method),
		StripeSessionID:       string(sessID),
		StripePaymentIntentID: string(piID),
		CreatedAt:             createdAt.UTC(),
		UpdatedAt:             updatedAt.UTC(),
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

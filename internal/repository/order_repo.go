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

var ErrNotFound = errors.New("order not found")

type OrderRepository interface {
	Create(ctx context.Context, order *domain.Order) error
	GetByOrderID(ctx context.Context, orderID string) (*domain.Order, error)
	GetByPaymentIntentID(ctx context.Context, pi string) (*domain.Order, error)
	UpdateStatus(ctx context.Context, orderID string, status domain.OrderStatus, stripePaymentIntentID string) error
	UpdateCheckout(ctx context.Context, orderID, stripeSessionID, stripePaymentIntentID, checkoutURL string) error
	BatchCreate(ctx context.Context, orders []*domain.Order) error
	GetPendingBefore(ctx context.Context, before time.Time, limit int) ([]*domain.Order, error)
}

type orderRepo struct {
	db *sql.DB
}

func NewOrderRepository(db *sql.DB) OrderRepository {
	return &orderRepo{db: db}
}

const orderCols = `id, order_id, merchant_id, transaction_id, amount, currency, status,
		payment_method, stripe_session_id, stripe_payment_intent_id, stripe_charge_id,
		checkout_url, callback_url, created_at, updated_at`

func (r *orderRepo) Create(ctx context.Context, o *domain.Order) error {
	const q = `INSERT INTO orders
		(order_id, merchant_id, transaction_id, amount, currency, status,
		 payment_method, stripe_session_id, stripe_payment_intent_id, stripe_charge_id,
		 checkout_url, callback_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	res, err := r.db.ExecContext(ctx, q,
		o.OrderID, o.MerchantID, o.TransactionID, o.Amount, o.Currency, string(o.Status),
		nullStr(o.PaymentMethod), nullStr(o.StripeSessionID), nullStr(o.StripePaymentIntentID), nullStr(o.StripeChargeID),
		nullStr(o.CheckoutURL), nullStr(o.CallbackURL),
	)
	if err != nil {
		return fmt.Errorf("insert order: %w", err)
	}
	id, _ := res.LastInsertId()
	o.ID = id
	return nil
}

func (r *orderRepo) GetByOrderID(ctx context.Context, orderID string) (*domain.Order, error) {
	q := `SELECT ` + orderCols + ` FROM orders WHERE order_id = ? LIMIT 1`
	row := r.db.QueryRowContext(ctx, q, orderID)
	return scanOrder(row)
}

func (r *orderRepo) GetByPaymentIntentID(ctx context.Context, pi string) (*domain.Order, error) {
	q := `SELECT ` + orderCols + ` FROM orders WHERE stripe_payment_intent_id = ? LIMIT 1`
	row := r.db.QueryRowContext(ctx, q, pi)
	return scanOrder(row)
}

func (r *orderRepo) UpdateStatus(ctx context.Context, orderID string, status domain.OrderStatus, stripePaymentIntentID string) error {
	const q = `UPDATE orders
		SET status = ?, stripe_payment_intent_id = COALESCE(NULLIF(?, ''), stripe_payment_intent_id)
		WHERE order_id = ?`
	res, err := r.db.ExecContext(ctx, q, string(status), stripePaymentIntentID, orderID)
	if err != nil {
		return fmt.Errorf("update order status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *orderRepo) UpdateCheckout(ctx context.Context, orderID, sessionID, piID, checkoutURL string) error {
	const q = `UPDATE orders SET
		stripe_session_id        = COALESCE(NULLIF(?, ''), stripe_session_id),
		stripe_payment_intent_id = COALESCE(NULLIF(?, ''), stripe_payment_intent_id),
		checkout_url             = COALESCE(NULLIF(?, ''), checkout_url)
		WHERE order_id = ?`
	res, err := r.db.ExecContext(ctx, q, sessionID, piID, checkoutURL, orderID)
	if err != nil {
		return fmt.Errorf("update checkout: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *orderRepo) BatchCreate(ctx context.Context, orders []*domain.Order) error {
	if len(orders) == 0 {
		return nil
	}
	const cols = `(order_id, merchant_id, transaction_id, amount, currency, status,
		payment_method, stripe_session_id, stripe_payment_intent_id, stripe_charge_id,
		checkout_url, callback_url)`
	placeholders := make([]string, 0, len(orders))
	args := make([]any, 0, len(orders)*12)
	for _, o := range orders {
		placeholders = append(placeholders, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
		args = append(args,
			o.OrderID, o.MerchantID, o.TransactionID, o.Amount, o.Currency, string(o.Status),
			nullStr(o.PaymentMethod), nullStr(o.StripeSessionID), nullStr(o.StripePaymentIntentID), nullStr(o.StripeChargeID),
			nullStr(o.CheckoutURL), nullStr(o.CallbackURL),
		)
	}
	// #nosec G202 -- cols and placeholders are compile-time constants; only ? params interpolate user data.
	q := "INSERT INTO orders " + cols + " VALUES " + strings.Join(placeholders, ", ")
	if _, err := r.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("batch insert: %w", err)
	}
	return nil
}

func (r *orderRepo) GetPendingBefore(ctx context.Context, before time.Time, limit int) ([]*domain.Order, error) {
	q := `SELECT ` + orderCols + ` FROM orders
	      WHERE status IN ('created','pending') AND created_at < ?
	      ORDER BY created_at ASC LIMIT ?`
	rows, err := r.db.QueryContext(ctx, q, before, limit)
	if err != nil {
		return nil, fmt.Errorf("query pending: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*domain.Order, 0, limit)
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanOrder(s scanner) (*domain.Order, error) {
	var o domain.Order
	var status string
	var pm, ssid, pid, cid, curl, cb sql.NullString
	if err := s.Scan(
		&o.ID, &o.OrderID, &o.MerchantID, &o.TransactionID, &o.Amount, &o.Currency, &status,
		&pm, &ssid, &pid, &cid, &curl, &cb, &o.CreatedAt, &o.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan order: %w", err)
	}
	o.Status = domain.OrderStatus(status)
	o.PaymentMethod = pm.String
	o.StripeSessionID = ssid.String
	o.StripePaymentIntentID = pid.String
	o.StripeChargeID = cid.String
	o.CheckoutURL = curl.String
	o.CallbackURL = cb.String
	return &o, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

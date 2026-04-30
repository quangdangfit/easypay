package repository

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/quangdangfit/easypay/internal/domain"
)

func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func TestOrderRepo_Create(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectExec("INSERT INTO orders").
		WithArgs("ord-1", "M1", "TXN-1", int64(1500), "USD", "pending",
			nil, nil, nil, nil, nil, nil).
		WillReturnResult(sqlmock.NewResult(7, 1))

	o := &domain.Order{
		OrderID: "ord-1", MerchantID: "M1", TransactionID: "TXN-1",
		Amount: 1500, Currency: "USD", Status: domain.OrderStatusPending,
	}
	if err := repo.Create(context.Background(), o); err != nil {
		t.Fatalf("create: %v", err)
	}
	if o.ID != 7 {
		t.Fatalf("id: %d", o.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOrderRepo_Create_Error(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectExec("INSERT INTO orders").WillReturnError(errors.New("dup"))

	if err := repo.Create(context.Background(), &domain.Order{}); err == nil {
		t.Fatal("expected error")
	}
}

func orderRow() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "order_id", "merchant_id", "transaction_id", "amount",
		"currency", "status", "payment_method", "stripe_session_id",
		"stripe_payment_intent_id", "stripe_charge_id", "checkout_url",
		"callback_url", "created_at", "updated_at",
	}).AddRow(
		int64(1), "ord-1", "M1", "TXN-1", int64(1500),
		"USD", "paid", "card", "cs_x",
		"pi_x", "ch_x", "https://x",
		"https://cb", time.Now(), time.Now(),
	)
}

func TestOrderRepo_GetByOrderID(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectQuery("SELECT.*FROM orders WHERE order_id").
		WithArgs("ord-1").
		WillReturnRows(orderRow())

	o, err := repo.GetByOrderID(context.Background(), "ord-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if o.OrderID != "ord-1" || o.Status != domain.OrderStatusPaid {
		t.Fatalf("got: %+v", o)
	}
}

func TestOrderRepo_GetByOrderID_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectQuery("SELECT.*FROM orders WHERE order_id").
		WithArgs("missing").WillReturnError(sql.ErrNoRows)

	_, err := repo.GetByOrderID(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestOrderRepo_GetByPaymentIntentID(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectQuery("WHERE stripe_payment_intent_id").
		WithArgs("pi_x").WillReturnRows(orderRow())
	o, err := repo.GetByPaymentIntentID(context.Background(), "pi_x")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if o.StripePaymentIntentID != "pi_x" {
		t.Fatalf("got: %+v", o)
	}
}

func TestOrderRepo_UpdateStatus(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE orders")).
		WithArgs("paid", "pi_x", "ord-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.UpdateStatus(context.Background(), "ord-1", domain.OrderStatusPaid, "pi_x"); err != nil {
		t.Fatalf("update: %v", err)
	}
}

func TestOrderRepo_UpdateStatus_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectExec("UPDATE orders").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.UpdateStatus(context.Background(), "absent", domain.OrderStatusPaid, "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestOrderRepo_UpdateCheckout(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectExec("UPDATE orders SET").
		WithArgs("cs_x", "pi_x", "https://x", "ord-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.UpdateCheckout(context.Background(), "ord-1", "cs_x", "pi_x", "https://x"); err != nil {
		t.Fatalf("update: %v", err)
	}
}

func TestOrderRepo_UpdateCheckout_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectExec("UPDATE orders SET").
		WillReturnResult(sqlmock.NewResult(0, 0))
	err := repo.UpdateCheckout(context.Background(), "x", "", "", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestOrderRepo_BatchCreate_Empty(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewOrderRepository(db)
	if err := repo.BatchCreate(context.Background(), nil); err != nil {
		t.Fatalf("empty: %v", err)
	}
}

func TestOrderRepo_BatchCreate(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectExec("INSERT INTO orders.*VALUES").
		WillReturnResult(sqlmock.NewResult(0, 2))

	orders := []*domain.Order{
		{OrderID: "ord-1", MerchantID: "M1", TransactionID: "TXN-1", Amount: 100, Currency: "USD", Status: domain.OrderStatusPending},
		{OrderID: "ord-2", MerchantID: "M1", TransactionID: "TXN-2", Amount: 200, Currency: "USD", Status: domain.OrderStatusPending},
	}
	if err := repo.BatchCreate(context.Background(), orders); err != nil {
		t.Fatalf("batch: %v", err)
	}
}

func TestOrderRepo_BatchCreate_Error(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectExec("INSERT INTO orders").WillReturnError(errors.New("dup"))
	err := repo.BatchCreate(context.Background(), []*domain.Order{
		{OrderID: "ord-1", MerchantID: "M1", TransactionID: "TXN-1", Amount: 1, Currency: "USD", Status: domain.OrderStatusPending},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestOrderRepo_GetPendingBefore(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectQuery("FROM orders.*WHERE status IN").
		WithArgs(sqlmock.AnyArg(), 10).
		WillReturnRows(orderRow())

	out, err := repo.GetPendingBefore(context.Background(), time.Now(), 10)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("rows: %d", len(out))
	}
}

func TestOrderRepo_GetPendingBefore_QueryError(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectQuery("FROM orders").WillReturnError(errors.New("conn"))
	_, err := repo.GetPendingBefore(context.Background(), time.Now(), 10)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestNullStr(t *testing.T) {
	if v := nullStr(""); v != nil {
		t.Fatal("empty should be nil")
	}
	if v := nullStr("x"); v != "x" {
		t.Fatalf("got: %v", v)
	}
}

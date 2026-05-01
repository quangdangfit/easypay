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

// fixed test fixtures: a 32-hex transaction_id and a 24-hex order_id whose
// first byte selects shard 0x05 (so we hit a deterministic table name).
const (
	testMerchant = "M1"
	testTxnHex   = "0123456789abcdef0123456789abcdef"
	testOrderHex = "05abcdef0123456789abcdef" // shard byte 0x05 -> orders_05
	testShard    = uint8(0x05)
)

func orderRow() *sqlmock.Rows {
	now := time.Now().Unix()
	merch := []byte(testMerchant)
	txn, _ := decodeHex16(testTxnHex)
	ord, _ := decodeHex12(testOrderHex)
	return sqlmock.NewRows([]string{
		"merchant_id", "transaction_id", "order_id", "amount", "currency_code",
		"status", "payment_method", "stripe_pi_id", "stripe_session", "created_at", "updated_at",
	}).AddRow(
		merch, txn, ord, uint64(1500), uint16(840),
		uint8(2), uint8(0), []byte("pi_x"), []byte("cs_x"), uint32(now), uint32(now),
	)
}

func TestOrderRepo_Insert(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	merchShard := ShardOf(testMerchant)
	mock.ExpectExec("INSERT INTO " + ShardTable(merchShard)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	o := &domain.Order{
		MerchantID:    testMerchant,
		TransactionID: testTxnHex,
		OrderID:       testOrderHex,
		Amount:        1500,
		Currency:      "USD",
		Status:        domain.OrderStatusPending,
		PaymentMethod: "card",
	}
	if err := repo.Insert(context.Background(), o); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOrderRepo_Insert_Duplicate(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	merchShard := ShardOf(testMerchant)
	mock.ExpectExec("INSERT INTO " + ShardTable(merchShard)).
		WillReturnError(errors.New("Error 1062: Duplicate entry"))

	err := repo.Insert(context.Background(), &domain.Order{
		MerchantID: testMerchant, TransactionID: testTxnHex, OrderID: testOrderHex,
		Amount: 1, Currency: "USD", Status: domain.OrderStatusCreated, PaymentMethod: "card",
	})
	if !errors.Is(err, ErrDuplicateOrder) {
		t.Fatalf("want ErrDuplicateOrder, got %v", err)
	}
}

func TestOrderRepo_Insert_BadTxnHex(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewOrderRepository(db)
	err := repo.Insert(context.Background(), &domain.Order{
		MerchantID: testMerchant, TransactionID: "tooshort", OrderID: testOrderHex,
		Amount: 1, Currency: "USD", Status: domain.OrderStatusCreated, PaymentMethod: "card",
	})
	if err == nil {
		t.Fatal("expected error for bad txn hex")
	}
}

func TestOrderRepo_GetByTransactionID(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	merchShard := ShardOf(testMerchant)
	mock.ExpectQuery("SELECT.*FROM " + ShardTable(merchShard) + " WHERE merchant_id").
		WillReturnRows(orderRow())

	o, err := repo.GetByTransactionID(context.Background(), testMerchant, testTxnHex)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if o.OrderID != testOrderHex || o.Status != domain.OrderStatusPaid || o.Currency != "USD" {
		t.Fatalf("got: %+v", o)
	}
}

func TestOrderRepo_GetByTransactionID_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	merchShard := ShardOf(testMerchant)
	mock.ExpectQuery("SELECT.*FROM " + ShardTable(merchShard)).
		WillReturnError(sql.ErrNoRows)

	_, err := repo.GetByTransactionID(context.Background(), testMerchant, testTxnHex)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestOrderRepo_GetByOrderID(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectQuery("SELECT.*FROM " + ShardTable(testShard) + " WHERE order_id").
		WillReturnRows(orderRow())

	o, err := repo.GetByOrderID(context.Background(), testOrderHex)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if o.OrderID != testOrderHex {
		t.Fatalf("got: %+v", o)
	}
}

func TestOrderRepo_GetByOrderID_BadFormat(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewOrderRepository(db)
	_, err := repo.GetByOrderID(context.Background(), "tooshort")
	if !errors.Is(err, domain.ErrInvalidOrderID) {
		t.Fatalf("want ErrInvalidOrderID, got %v", err)
	}
}

func TestOrderRepo_UpdateStatus(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE " + ShardTable(testShard))).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.UpdateStatus(context.Background(), testOrderHex, domain.OrderStatusPaid, "pi_x"); err != nil {
		t.Fatalf("update: %v", err)
	}
}

func TestOrderRepo_UpdateStatus_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectExec("UPDATE " + ShardTable(testShard)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	err := repo.UpdateStatus(context.Background(), testOrderHex, domain.OrderStatusPaid, "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestOrderRepo_UpdateCheckout(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectExec("UPDATE " + ShardTable(testShard)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.UpdateCheckout(context.Background(), testOrderHex, "cs_x", "pi_x"); err != nil {
		t.Fatalf("update: %v", err)
	}
}

func TestOrderRepo_UpdateCheckout_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(db)
	mock.ExpectExec("UPDATE " + ShardTable(testShard)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	err := repo.UpdateCheckout(context.Background(), testOrderHex, "", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestNewShardedOrderRepository_PoolCount(t *testing.T) {
	if _, err := NewShardedOrderRepository(nil); err == nil {
		t.Fatal("expected error for empty pool list")
	}
	if _, err := NewShardedOrderRepository(make([]*sql.DB, ShardCount-1)); err == nil {
		t.Fatal("expected error for short pool list")
	}
}

func TestHexLower(t *testing.T) {
	got := hexLower([]byte{0x00, 0x0f, 0xab, 0xcd})
	if got != "000fabcd" {
		t.Fatalf("got %q", got)
	}
}

func TestNullBytes(t *testing.T) {
	if v := nullBytes(""); v != nil {
		t.Fatal("empty should be nil")
	}
	if v := nullBytes("x"); v == nil {
		t.Fatal("non-empty should not be nil")
	}
}

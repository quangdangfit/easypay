package repository

import (
	"context"
	"database/sql"
	"errors"
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

const (
	testMerchant = "M1"
	testTxnHex   = "0123456789abcdef0123456789abcdef"
	testOrderID  = "order-1"
)

func transactionRow() *sqlmock.Rows {
	now := time.Now().UTC()
	merch := []byte(testMerchant)
	txn, _ := decodeHex16(testTxnHex)
	return sqlmock.NewRows([]string{
		"merchant_id", "transaction_id", "order_id", "amount", "currency_code",
		"status", "payment_method", "stripe_pi_id", "stripe_session", "created_at", "updated_at",
	}).AddRow(
		merch, txn, testOrderID, uint64(1500), uint16(840),
		uint8(2), uint8(0), []byte("pi_x"), []byte("cs_x"), now, now,
	)
}

func TestTransactionRepo_Insert(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectExec("INSERT INTO transactions").
		WillReturnResult(sqlmock.NewResult(0, 1))

	tx := &domain.Transaction{
		MerchantID:    testMerchant,
		TransactionID: testTxnHex,
		OrderID:       testOrderID,
		Amount:        1500,
		Currency:      "USD",
		Status:        domain.TransactionStatusPending,
		PaymentMethod: "card",
	}
	if err := repo.Insert(context.Background(), tx); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionRepo_Insert_Duplicate(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectExec("INSERT INTO transactions").
		WillReturnError(errors.New("Error 1062: Duplicate entry"))

	err := repo.Insert(context.Background(), &domain.Transaction{
		MerchantID: testMerchant, TransactionID: testTxnHex, OrderID: testOrderID,
		Amount: 1, Currency: "USD", Status: domain.TransactionStatusCreated, PaymentMethod: "card",
	})
	if !errors.Is(err, ErrDuplicateTransaction) {
		t.Fatalf("want ErrDuplicateTransaction, got %v", err)
	}
}

func TestTransactionRepo_Insert_BadTxnHex(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	err := repo.Insert(context.Background(), &domain.Transaction{
		MerchantID: testMerchant, TransactionID: "tooshort", OrderID: testOrderID,
		Amount: 1, Currency: "USD", Status: domain.TransactionStatusCreated, PaymentMethod: "card",
	})
	if err == nil {
		t.Fatal("expected error for bad txn hex")
	}
}

func TestTransactionRepo_Insert_BadOrderID(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	err := repo.Insert(context.Background(), &domain.Transaction{
		MerchantID: testMerchant, TransactionID: testTxnHex, OrderID: "has space",
		Amount: 1, Currency: "USD", Status: domain.TransactionStatusCreated, PaymentMethod: "card",
	})
	if !errors.Is(err, domain.ErrInvalidOrderID) {
		t.Fatalf("want ErrInvalidOrderID, got %v", err)
	}
}

func TestTransactionRepo_GetByTransactionID(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectQuery("SELECT.*FROM transactions WHERE merchant_id").
		WillReturnRows(transactionRow())

	tx, err := repo.GetByTransactionID(context.Background(), 0, testMerchant, testTxnHex)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if tx.OrderID != testOrderID || tx.Status != domain.TransactionStatusPaid || tx.Currency != "USD" {
		t.Fatalf("got: %+v", tx)
	}
}

func TestTransactionRepo_GetByTransactionID_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectQuery("SELECT.*FROM transactions").
		WillReturnError(sql.ErrNoRows)

	_, err := repo.GetByTransactionID(context.Background(), 0, testMerchant, testTxnHex)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestTransactionRepo_GetByMerchantOrderID(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectQuery("SELECT.*FROM transactions WHERE merchant_id = . AND order_id").
		WillReturnRows(transactionRow())

	tx, err := repo.GetByMerchantOrderID(context.Background(), 0, testMerchant, testOrderID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if tx.OrderID != testOrderID {
		t.Fatalf("got: %+v", tx)
	}
}

func TestTransactionRepo_GetByMerchantOrderID_BadFormat(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	_, err := repo.GetByMerchantOrderID(context.Background(), 0, testMerchant, "has space")
	if !errors.Is(err, domain.ErrInvalidOrderID) {
		t.Fatalf("want ErrInvalidOrderID, got %v", err)
	}
}

func TestTransactionRepo_UpdateStatus(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectExec("UPDATE transactions").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.UpdateStatus(context.Background(), 0, testMerchant, testOrderID, domain.TransactionStatusPaid, "pi_x"); err != nil {
		t.Fatalf("update: %v", err)
	}
}

func TestTransactionRepo_UpdateStatus_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectExec("UPDATE transactions").
		WillReturnResult(sqlmock.NewResult(0, 0))
	err := repo.UpdateStatus(context.Background(), 0, testMerchant, testOrderID, domain.TransactionStatusPaid, "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestTransactionRepo_UpdateCheckout(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectExec("UPDATE transactions").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repo.UpdateCheckout(context.Background(), 0, testMerchant, testOrderID, "cs_x", "pi_x"); err != nil {
		t.Fatalf("update: %v", err)
	}
}

func TestTransactionRepo_UpdateCheckout_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectExec("UPDATE transactions").
		WillReturnResult(sqlmock.NewResult(0, 0))
	err := repo.UpdateCheckout(context.Background(), 0, testMerchant, testOrderID, "", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
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

func TestNullString(t *testing.T) {
	if v := nullString(""); v != nil {
		t.Fatal("empty should be nil")
	}
	if v := nullString("test"); v != "test" {
		t.Fatal("non-empty should return string")
	}
}

func TestTransactionRepo_Insert_MerchantIDTooLong(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	err := repo.Insert(context.Background(), &domain.Transaction{
		MerchantID:    "M" + "1234567890abcdef", // 17 bytes, exceeds limit
		TransactionID: testTxnHex,
		OrderID:       testOrderID,
		Amount:        1,
		Currency:      "USD",
		Status:        domain.TransactionStatusCreated,
		PaymentMethod: "card",
	})
	if err == nil {
		t.Fatal("expected error for merchant_id too long")
	}
}

func TestTransactionRepo_Insert_NegativeAmount(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	err := repo.Insert(context.Background(), &domain.Transaction{
		MerchantID:    testMerchant,
		TransactionID: testTxnHex,
		OrderID:       testOrderID,
		Amount:        -1,
		Currency:      "USD",
		Status:        domain.TransactionStatusCreated,
		PaymentMethod: "card",
	})
	if err == nil {
		t.Fatal("expected error for negative amount")
	}
}

func TestTransactionRepo_Insert_InvalidCurrency(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	err := repo.Insert(context.Background(), &domain.Transaction{
		MerchantID:    testMerchant,
		TransactionID: testTxnHex,
		OrderID:       testOrderID,
		Amount:        1,
		Currency:      "INVALID",
		Status:        domain.TransactionStatusCreated,
		PaymentMethod: "card",
	})
	if err == nil {
		t.Fatal("expected error for invalid currency")
	}
}

func TestTransactionRepo_Insert_InvalidStatus(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	err := repo.Insert(context.Background(), &domain.Transaction{
		MerchantID:    testMerchant,
		TransactionID: testTxnHex,
		OrderID:       testOrderID,
		Amount:        1,
		Currency:      "USD",
		Status:        domain.TransactionStatus("invalid_status"),
		PaymentMethod: "card",
	})
	if err == nil {
		t.Fatal("expected error for invalid status")
	}
}

func TestTransactionRepo_Insert_DBError(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectExec("INSERT INTO transactions").
		WillReturnError(errors.New("some database error"))
	err := repo.Insert(context.Background(), &domain.Transaction{
		MerchantID:    testMerchant,
		TransactionID: testTxnHex,
		OrderID:       testOrderID,
		Amount:        1,
		Currency:      "USD",
		Status:        domain.TransactionStatusCreated,
		PaymentMethod: "card",
	})
	if err == nil {
		t.Fatal("expected error for database failure")
	}
}

func TestTransactionRepo_UpdateStatus_InvalidOrderID(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	err := repo.UpdateStatus(context.Background(), 0, testMerchant, "has space", domain.TransactionStatusPaid, "")
	if !errors.Is(err, domain.ErrInvalidOrderID) {
		t.Fatalf("want ErrInvalidOrderID, got %v", err)
	}
}

func TestTransactionRepo_UpdateStatus_InvalidStatus(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	err := repo.UpdateStatus(context.Background(), 0, testMerchant, testOrderID, domain.TransactionStatus("invalid"), "")
	if err == nil {
		t.Fatal("expected error for invalid status")
	}
}

func TestTransactionRepo_UpdateStatus_DBError(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectExec("UPDATE transactions").
		WillReturnError(errors.New("database error"))
	err := repo.UpdateStatus(context.Background(), 0, testMerchant, testOrderID, domain.TransactionStatusPaid, "pi_x")
	if err == nil {
		t.Fatal("expected error for database failure")
	}
}

func TestTransactionRepo_UpdateCheckout_InvalidOrderID(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	err := repo.UpdateCheckout(context.Background(), 0, testMerchant, "bad order", "", "")
	if !errors.Is(err, domain.ErrInvalidOrderID) {
		t.Fatalf("want ErrInvalidOrderID, got %v", err)
	}
}

func TestTransactionRepo_UpdateCheckout_DBError(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectExec("UPDATE transactions").
		WillReturnError(errors.New("database error"))
	err := repo.UpdateCheckout(context.Background(), 0, testMerchant, testOrderID, "cs_x", "pi_x")
	if err == nil {
		t.Fatal("expected error for database failure")
	}
}

func TestTransactionRepo_GetByTransactionID_DBError(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectQuery("SELECT.*FROM transactions").
		WillReturnError(errors.New("database error"))
	_, err := repo.GetByTransactionID(context.Background(), 0, testMerchant, testTxnHex)
	if err == nil {
		t.Fatal("expected error for database failure")
	}
}

func TestTransactionRepo_GetByTransactionID_BadFormat(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	_, err := repo.GetByTransactionID(context.Background(), 0, testMerchant, "tooshort")
	if err == nil {
		t.Fatal("expected error for bad transaction hex")
	}
}

func TestTransactionRepo_GetByMerchantOrderID_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectQuery("SELECT.*FROM transactions").
		WillReturnError(sql.ErrNoRows)
	_, err := repo.GetByMerchantOrderID(context.Background(), 0, testMerchant, testOrderID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestTransactionRepo_GetByMerchantOrderID_DBError(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewTransactionRepository(NewSingleShardRouter(db, 16))
	mock.ExpectQuery("SELECT.*FROM transactions").
		WillReturnError(errors.New("database error"))
	_, err := repo.GetByMerchantOrderID(context.Background(), 0, testMerchant, testOrderID)
	if err == nil {
		t.Fatal("expected error for database failure")
	}
}

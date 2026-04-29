package repository

import (
	"context"
	"database/sql"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/quangdangfit/easypay/internal/domain"
)

func ptxRow() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tx_hash", "block_number", "order_id", "payer", "token",
		"amount", "chain_id", "confirmations", "required_confirm", "status",
		"created_at",
	}).AddRow(int64(1), "0xabc", uint64(100), "ORD-1", "0xdead", "0xbeef",
		"123", int64(11155111), uint64(3), uint64(12), "pending", time.Now())
}

func TestPendingTxRepo_Create(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPendingTxRepository(db)
	mock.ExpectExec("INSERT INTO pending_txs").
		WillReturnResult(sqlmock.NewResult(5, 1))

	tx := &domain.PendingTx{
		TxHash: "0xabc", BlockNumber: 100, OrderID: "ORD-1",
		ChainID: 1, RequiredConfirm: 12, Status: domain.PendingTxStatusPending,
		Amount: big.NewInt(99),
	}
	if err := repo.Create(context.Background(), tx); err != nil {
		t.Fatalf("create: %v", err)
	}
	if tx.ID != 5 {
		t.Fatalf("id: %d", tx.ID)
	}
}

func TestPendingTxRepo_Create_NilAmount(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPendingTxRepository(db)
	mock.ExpectExec("INSERT INTO pending_txs").
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := repo.Create(context.Background(), &domain.PendingTx{TxHash: "x", Status: domain.PendingTxStatusPending}); err != nil {
		t.Fatal(err)
	}
}

func TestPendingTxRepo_Create_Error(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPendingTxRepository(db)
	mock.ExpectExec("INSERT INTO pending_txs").WillReturnError(errors.New("dup"))
	if err := repo.Create(context.Background(), &domain.PendingTx{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestPendingTxRepo_GetByTxHash(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPendingTxRepository(db)
	mock.ExpectQuery("FROM pending_txs WHERE tx_hash").WillReturnRows(ptxRow())

	tx, err := repo.GetByTxHash(context.Background(), "0xabc")
	if err != nil {
		t.Fatal(err)
	}
	if tx.OrderID != "ORD-1" || tx.Amount.String() != "123" {
		t.Fatalf("got: %+v", tx)
	}
}

func TestPendingTxRepo_GetByTxHash_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPendingTxRepository(db)
	mock.ExpectQuery("FROM pending_txs").WillReturnError(sql.ErrNoRows)
	_, err := repo.GetByTxHash(context.Background(), "missing")
	if !errors.Is(err, ErrPendingTxNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestPendingTxRepo_UpdateConfirmations(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPendingTxRepository(db)
	mock.ExpectExec("UPDATE pending_txs").
		WithArgs(uint64(8), "confirmed", "0xabc").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.UpdateConfirmations(context.Background(), "0xabc", 8, domain.PendingTxStatusConfirmed); err != nil {
		t.Fatalf("err: %v", err)
	}
}

func TestPendingTxRepo_UpdateConfirmations_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPendingTxRepository(db)
	mock.ExpectExec("UPDATE pending_txs").WillReturnResult(sqlmock.NewResult(0, 0))
	err := repo.UpdateConfirmations(context.Background(), "x", 0, domain.PendingTxStatusFailed)
	if !errors.Is(err, ErrPendingTxNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestPendingTxRepo_ListPendingByChain(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPendingTxRepository(db)
	mock.ExpectQuery("FROM pending_txs.*WHERE chain_id").
		WithArgs(int64(1), 5).WillReturnRows(ptxRow())

	out, err := repo.ListPendingByChain(context.Background(), 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("rows: %d", len(out))
	}
}

func TestPendingTxRepo_ListPendingByChain_Error(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewPendingTxRepository(db)
	mock.ExpectQuery("FROM pending_txs").WillReturnError(errors.New("boom"))
	_, err := repo.ListPendingByChain(context.Background(), 1, 5)
	if err == nil {
		t.Fatal("expected error")
	}
}

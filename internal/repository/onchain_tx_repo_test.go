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

func onchainRow() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tx_hash", "block_number", "merchant_id", "order_id", "payer", "token",
		"amount", "chain_id", "confirmations", "required_confirm", "status",
		"created_at",
	}).AddRow(int64(1), "0xabc", uint64(100), "M1", "ord-1", "0xdead", "0xbeef",
		"123", int64(11155111), uint64(3), uint64(12), "pending", time.Now())
}

func TestOnchainTxRepo_Create(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOnchainTxRepository(NewSingleShardRouter(db, 16))
	mock.ExpectExec("INSERT INTO onchain_transactions").
		WillReturnResult(sqlmock.NewResult(5, 1))

	tx := &domain.OnchainTransaction{
		TxHash: "0xabc", BlockNumber: 100, OrderID: "ord-1",
		ChainID: 1, RequiredConfirm: 12, Status: domain.OnchainTxStatusPending,
		Amount: big.NewInt(99),
	}
	if err := repo.Create(context.Background(), tx); err != nil {
		t.Fatalf("create: %v", err)
	}
	if tx.ID != 5 {
		t.Fatalf("id: %d", tx.ID)
	}
}

func TestOnchainTxRepo_Create_NilAmount(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOnchainTxRepository(NewSingleShardRouter(db, 16))
	mock.ExpectExec("INSERT INTO onchain_transactions").
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := repo.Create(context.Background(), &domain.OnchainTransaction{TxHash: "x", Status: domain.OnchainTxStatusPending}); err != nil {
		t.Fatal(err)
	}
}

func TestOnchainTxRepo_Create_Error(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOnchainTxRepository(NewSingleShardRouter(db, 16))
	mock.ExpectExec("INSERT INTO onchain_transactions").WillReturnError(errors.New("dup"))
	if err := repo.Create(context.Background(), &domain.OnchainTransaction{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestOnchainTxRepo_GetByTxHash(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOnchainTxRepository(NewSingleShardRouter(db, 16))
	mock.ExpectQuery("FROM onchain_transactions WHERE tx_hash").WillReturnRows(onchainRow())

	tx, err := repo.GetByTxHash(context.Background(), "0xabc")
	if err != nil {
		t.Fatal(err)
	}
	if tx.OrderID != "ord-1" || tx.Amount.String() != "123" {
		t.Fatalf("got: %+v", tx)
	}
}

func TestOnchainTxRepo_GetByTxHash_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOnchainTxRepository(NewSingleShardRouter(db, 16))
	mock.ExpectQuery("FROM onchain_transactions").WillReturnError(sql.ErrNoRows)
	_, err := repo.GetByTxHash(context.Background(), "missing")
	if !errors.Is(err, ErrOnchainTxNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestOnchainTxRepo_UpdateConfirmations(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOnchainTxRepository(NewSingleShardRouter(db, 16))
	mock.ExpectExec("UPDATE onchain_transactions").
		WithArgs(uint64(8), "confirmed", "0xabc").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.UpdateConfirmations(context.Background(), "0xabc", 8, domain.OnchainTxStatusConfirmed); err != nil {
		t.Fatalf("err: %v", err)
	}
}

func TestOnchainTxRepo_UpdateConfirmations_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOnchainTxRepository(NewSingleShardRouter(db, 16))
	mock.ExpectExec("UPDATE onchain_transactions").WillReturnResult(sqlmock.NewResult(0, 0))
	err := repo.UpdateConfirmations(context.Background(), "x", 0, domain.OnchainTxStatusFailed)
	if !errors.Is(err, ErrOnchainTxNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestOnchainTxRepo_ListPendingByChain(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOnchainTxRepository(NewSingleShardRouter(db, 16))
	mock.ExpectQuery("FROM onchain_transactions.*WHERE chain_id").
		WithArgs(int64(1), 5).WillReturnRows(onchainRow())

	out, err := repo.ListPendingByChain(context.Background(), 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("rows: %d", len(out))
	}
}

func TestOnchainTxRepo_ListPendingByChain_Error(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOnchainTxRepository(NewSingleShardRouter(db, 16))
	mock.ExpectQuery("FROM onchain_transactions").WillReturnError(errors.New("boom"))
	_, err := repo.ListPendingByChain(context.Background(), 1, 5)
	if err == nil {
		t.Fatal("expected error")
	}
}

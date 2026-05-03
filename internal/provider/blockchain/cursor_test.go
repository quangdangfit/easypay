package blockchain

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/quangdangfit/easypay/internal/repository"
)

func newDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func TestCursor_GetExisting(t *testing.T) {
	db, mock := newDB(t)
	c := NewMySQLCursor(repository.NewSingleShardRouter(db, 16))
	mock.ExpectQuery("SELECT last_block FROM block_cursors").
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"last_block"}).AddRow(uint64(42)))

	v, err := c.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v != 42 {
		t.Fatalf("got %d", v)
	}
}

func TestCursor_GetMissing(t *testing.T) {
	db, mock := newDB(t)
	c := NewMySQLCursor(repository.NewSingleShardRouter(db, 16))
	mock.ExpectQuery("SELECT last_block FROM block_cursors").
		WillReturnError(sql.ErrNoRows)
	v, err := c.Get(context.Background(), 1)
	if err != nil || v != 0 {
		t.Fatalf("got=%d err=%v", v, err)
	}
}

func TestCursor_GetError(t *testing.T) {
	db, mock := newDB(t)
	c := NewMySQLCursor(repository.NewSingleShardRouter(db, 16))
	mock.ExpectQuery("SELECT").WillReturnError(errors.New("boom"))
	if _, err := c.Get(context.Background(), 1); err == nil {
		t.Fatal("expected error")
	}
}

func TestCursor_Set(t *testing.T) {
	db, mock := newDB(t)
	c := NewMySQLCursor(repository.NewSingleShardRouter(db, 16))
	mock.ExpectExec("INSERT INTO block_cursors").
		WithArgs(int64(1), uint64(99)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := c.Set(context.Background(), 1, 99); err != nil {
		t.Fatalf("set: %v", err)
	}
}

func TestCursor_SetError(t *testing.T) {
	db, mock := newDB(t)
	c := NewMySQLCursor(repository.NewSingleShardRouter(db, 16))
	mock.ExpectExec("INSERT INTO block_cursors").
		WillReturnError(errors.New("conn"))
	if err := c.Set(context.Background(), 1, 99); err == nil {
		t.Fatal("expected error")
	}
}

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

func merchantRow() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "merchant_id", "name", "api_key", "secret_key",
		"callback_url", "rate_limit", "status", "created_at",
	}).AddRow(int64(1), "M1", "Test", "k", "s", "", 1000, "active", time.Now())
}

func TestMerchantRepo_GetByAPIKey(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantRepository(db)
	mock.ExpectQuery("FROM merchants WHERE api_key").
		WithArgs("k").WillReturnRows(merchantRow())

	m, err := repo.GetByAPIKey(context.Background(), "k")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if m.MerchantID != "M1" {
		t.Fatalf("got: %+v", m)
	}

	// Second call should be cached → no extra query.
	m2, err := repo.GetByAPIKey(context.Background(), "k")
	if err != nil {
		t.Fatalf("cached: %v", err)
	}
	if m2.MerchantID != "M1" {
		t.Fatalf("cached mismatch")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMerchantRepo_GetByAPIKey_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantRepository(db)
	mock.ExpectQuery("FROM merchants").WithArgs("absent").WillReturnError(sql.ErrNoRows)
	_, err := repo.GetByAPIKey(context.Background(), "absent")
	if !errors.Is(err, ErrMerchantNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestMerchantRepo_GetByAPIKey_QueryError(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantRepository(db)
	mock.ExpectQuery("FROM merchants").WillReturnError(errors.New("conn refused"))
	_, err := repo.GetByAPIKey(context.Background(), "k")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLRUCache_TTLAndEviction(t *testing.T) {
	c := newLRUCache(2, 10*time.Millisecond)

	c.put("a", &domain.Merchant{MerchantID: "a"})
	if got, ok := c.get("a"); !ok || got.MerchantID != "a" {
		t.Fatal("get fresh")
	}

	time.Sleep(20 * time.Millisecond)
	if _, ok := c.get("a"); ok {
		t.Fatal("expected expiry")
	}

	// Eviction at capacity.
	c2 := newLRUCache(1, time.Minute)
	c2.put("a", &domain.Merchant{MerchantID: "a"})
	c2.put("b", &domain.Merchant{MerchantID: "b"})
	count := 0
	if _, ok := c2.get("a"); ok {
		count++
	}
	if _, ok := c2.get("b"); ok {
		count++
	}
	if count > 1 {
		t.Fatalf("cap exceeded: %d", count)
	}
}

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
		"callback_url", "rate_limit", "status", "shard_index", "created_at",
	}).AddRow(int64(1), "M1", "Test", "k", "s", "", 1000, "active", uint8(0), time.Now())
}

func TestMerchantRepo_GetByAPIKey(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantRepository(db, 16)
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
	repo := NewMerchantRepository(db, 16)
	mock.ExpectQuery("FROM merchants").WithArgs("absent").WillReturnError(sql.ErrNoRows)
	_, err := repo.GetByAPIKey(context.Background(), "absent")
	if !errors.Is(err, ErrMerchantNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestMerchantRepo_GetByAPIKey_QueryError(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantRepository(db, 16)
	mock.ExpectQuery("FROM merchants").WillReturnError(errors.New("conn refused"))
	_, err := repo.GetByAPIKey(context.Background(), "k")
	if err == nil {
		t.Fatal("expected error")
	}
}

// Pick least loaded with no existing merchants → shard 0.
func TestMerchantRepo_Insert_PicksZeroOnEmpty(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantRepository(db, 16)
	// pickLeastLoadedShard: empty result.
	mock.ExpectQuery("SELECT shard_index, COUNT").
		WithArgs(uint8(16)).
		WillReturnRows(sqlmock.NewRows([]string{"shard_index", "n"}))
	mock.ExpectExec("INSERT INTO merchants").
		WillReturnResult(sqlmock.NewResult(7, 1))

	m := &domain.Merchant{MerchantID: "M1", Name: "T", APIKey: "k", SecretKey: "s"}
	if err := repo.Insert(context.Background(), m); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if m.ShardIndex != 0 {
		t.Fatalf("shard: %d", m.ShardIndex)
	}
	if m.ID != 7 {
		t.Fatalf("id: %d", m.ID)
	}
}

// When shard 0 already has 1 merchant and shards 1..15 are empty, pick 1.
func TestMerchantRepo_Insert_PicksLeastLoaded(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantRepository(db, 16)
	mock.ExpectQuery("SELECT shard_index, COUNT").
		WithArgs(uint8(16)).
		WillReturnRows(sqlmock.NewRows([]string{"shard_index", "n"}).
			AddRow(uint8(0), int64(1)))
	mock.ExpectExec("INSERT INTO merchants").
		WillReturnResult(sqlmock.NewResult(2, 1))

	m := &domain.Merchant{MerchantID: "M2", Name: "T", APIKey: "k2", SecretKey: "s2"}
	if err := repo.Insert(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if m.ShardIndex != 1 {
		t.Fatalf("shard: %d (want 1)", m.ShardIndex)
	}
}

func TestMerchantRepo_Insert_DuplicateError(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewMerchantRepository(db, 16)
	mock.ExpectQuery("SELECT shard_index, COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"shard_index", "n"}))
	mock.ExpectExec("INSERT INTO merchants").
		WillReturnError(errors.New("Error 1062: Duplicate entry"))
	err := repo.Insert(context.Background(), &domain.Merchant{MerchantID: "M1", Name: "T", APIKey: "k", SecretKey: "s"})
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

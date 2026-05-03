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

// With a single physical pool, scatterFirst still exercises the
// goroutine-fan-out + result-collection path and the
// physicalToFirstLogical mapping.
func TestOrderRepo_GetByOrderIDAny_FoundOnSinglePool(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(NewSingleShardRouter(db, 16))

	mock.ExpectQuery("SELECT.*FROM transactions WHERE order_id").
		WithArgs(testOrderID).
		WillReturnRows(orderRow())

	o, err := repo.GetByOrderIDAny(context.Background(), testOrderID)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if o.OrderID != testOrderID {
		t.Fatalf("got: %+v", o)
	}
	// scatterFirst stamps the logical index of the matching pool. With a
	// single pool the first logical index in the partition is 0.
	if o.ShardIndex != 0 {
		t.Fatalf("ShardIndex=%d, want 0", o.ShardIndex)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOrderRepo_GetByOrderIDAny_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(NewSingleShardRouter(db, 16))
	mock.ExpectQuery("SELECT.*FROM transactions WHERE order_id").
		WillReturnError(sql.ErrNoRows)
	_, err := repo.GetByOrderIDAny(context.Background(), testOrderID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestOrderRepo_GetByOrderIDAny_BadFormat(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewOrderRepository(NewSingleShardRouter(db, 16))
	if _, err := repo.GetByOrderIDAny(context.Background(), "has space"); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestOrderRepo_GetByPaymentIntentID_EmptyShortcut(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewOrderRepository(NewSingleShardRouter(db, 16))
	_, err := repo.GetByPaymentIntentID(context.Background(), "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound for empty pi, got %v", err)
	}
}

func TestOrderRepo_GetByPaymentIntentID_Found(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(NewSingleShardRouter(db, 16))
	mock.ExpectQuery("SELECT.*FROM transactions WHERE stripe_pi_id").
		WithArgs([]byte("pi_x")).
		WillReturnRows(orderRow())

	o, err := repo.GetByPaymentIntentID(context.Background(), "pi_x")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if o.StripePaymentIntentID != "pi_x" {
		t.Fatalf("got: %+v", o)
	}
}

// Driver error from a shard surfaces as a non-nil error (not ErrNotFound).
func TestOrderRepo_GetByPaymentIntentID_DriverError(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(NewSingleShardRouter(db, 16))
	mock.ExpectQuery("SELECT.*FROM transactions WHERE stripe_pi_id").
		WillReturnError(errors.New("conn lost"))
	_, err := repo.GetByPaymentIntentID(context.Background(), "pi_x")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("driver error must not be reported as ErrNotFound")
	}
}

func TestOrderRepo_GetPendingBefore_ZeroLimit(t *testing.T) {
	db, _ := newMockDB(t)
	repo := NewOrderRepository(NewSingleShardRouter(db, 16))
	got, err := repo.GetPendingBefore(context.Background(), time.Now(), 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != nil {
		t.Fatalf("want nil for limit=0, got %d rows", len(got))
	}
}

func TestOrderRepo_GetPendingBefore_ReturnsRowsSortedByCreatedAt(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(NewSingleShardRouter(db, 16))

	merch := []byte(testMerchant)
	txn1, _ := decodeHex16("11111111111111111111111111111111")
	txn2, _ := decodeHex16("22222222222222222222222222222222")
	// Returned out-of-order on purpose; sortOrdersByCreatedAt should reorder.
	older := uint32(time.Now().Unix() - 100)
	newer := uint32(time.Now().Unix() - 10)
	rows := sqlmock.NewRows([]string{
		"merchant_id", "transaction_id", "order_id", "amount", "currency_code",
		"status", "payment_method", "stripe_pi_id", "stripe_session", "created_at", "updated_at",
	}).
		AddRow(merch, txn2, "o-newer", uint64(1), uint16(840),
			uint8(0), uint8(0), nil, nil, newer, newer).
		AddRow(merch, txn1, "o-older", uint64(1), uint16(840),
			uint8(0), uint8(0), nil, nil, older, older)

	mock.ExpectQuery("SELECT.*FROM transactions WHERE status IN").
		WillReturnRows(rows)

	got, err := repo.GetPendingBefore(context.Background(), time.Now(), 10)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].OrderID != "o-older" || got[1].OrderID != "o-newer" {
		t.Fatalf("not sorted by created_at: %v / %v", got[0].OrderID, got[1].OrderID)
	}
}

func TestOrderRepo_GetPendingBefore_DriverError(t *testing.T) {
	db, mock := newMockDB(t)
	repo := NewOrderRepository(NewSingleShardRouter(db, 16))
	mock.ExpectQuery("SELECT.*FROM transactions WHERE status IN").
		WillReturnError(errors.New("conn lost"))
	_, err := repo.GetPendingBefore(context.Background(), time.Now(), 10)
	if err == nil {
		t.Fatal("expected driver error")
	}
}

func TestSortOrdersByCreatedAt_StableOnEqualKeys(t *testing.T) {
	now := time.Now().UTC()
	a := &domain.Order{TransactionID: "aa", CreatedAt: now}
	b := &domain.Order{TransactionID: "bb", CreatedAt: now}
	c := &domain.Order{TransactionID: "cc", CreatedAt: now.Add(-time.Second)}
	in := []*domain.Order{a, b, c}
	sortOrdersByCreatedAt(in)
	if in[0] != c {
		t.Fatalf("oldest should sort first; got %s", in[0].TransactionID)
	}
	// Tiebreaker: aa before bb.
	if in[1] != a || in[2] != b {
		t.Fatalf("unstable tiebreaker: %s, %s", in[1].TransactionID, in[2].TransactionID)
	}
}

package repository

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/quangdangfit/easypay/internal/config"
)

func TestSingleShardRouter_ForControlAllAndCount(t *testing.T) {
	db, _ := newMockDB(t)
	r := NewSingleShardRouter(db, 16)

	if got := r.LogicalCount(); got != 16 {
		t.Fatalf("LogicalCount=%d, want 16", got)
	}
	if r.Control() != db {
		t.Fatal("Control did not return underlying *sql.DB")
	}
	all := r.All()
	if len(all) != 1 || all[0] != db {
		t.Fatalf("All=%+v", all)
	}

	for i := uint8(0); i < 16; i++ {
		got, err := r.For(i)
		if err != nil {
			t.Fatalf("For(%d): %v", i, err)
		}
		if got != db {
			t.Fatalf("For(%d) returned different pool", i)
		}
	}
	if _, err := r.For(16); !errors.Is(err, ErrUnknownShard) {
		t.Fatalf("expected ErrUnknownShard for out-of-range, got %v", err)
	}
}

func TestSingleShardRouter_DefaultsLogicalCountToOne(t *testing.T) {
	db, _ := newMockDB(t)
	r := NewSingleShardRouter(db, 0)
	if r.LogicalCount() != 1 {
		t.Fatalf("LogicalCount=%d, want 1 (default)", r.LogicalCount())
	}
}

func TestSingleShardRouter_AllReturnsCopy(t *testing.T) {
	db, _ := newMockDB(t)
	r := NewSingleShardRouter(db, 4)
	a := r.All()
	a[0] = nil // mutate caller's slice
	b := r.All()
	if b[0] == nil {
		t.Fatal("All() must return a defensive copy")
	}
}

func TestSingleShardRouter_Close(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectClose()
	r := NewSingleShardRouter(db, 4)
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenShards_RejectsZeroLogicalCount(t *testing.T) {
	_, err := OpenShards(config.DBConfig{DSN: "dummy"}, 0)
	if err == nil {
		t.Fatal("expected error for logicalCount=0")
	}
}

func TestOpenShards_RejectsLogicalLessThanShards(t *testing.T) {
	cfg := config.DBConfig{
		Shards: []config.ShardDBConfig{
			{DSN: "a"}, {DSN: "b"}, {DSN: "c"},
		},
	}
	// logicalCount=2 with 3 shards → invalid.
	_, err := OpenShards(cfg, 2)
	if err == nil {
		t.Fatal("expected error: logicalCount < len(shards)")
	}
}

func TestOpenShards_RejectsNonDivisibleLogicalCount(t *testing.T) {
	cfg := config.DBConfig{
		Shards: []config.ShardDBConfig{
			{DSN: "a"}, {DSN: "b"}, {DSN: "c"},
		},
	}
	// 16 % 3 != 0.
	_, err := OpenShards(cfg, 16)
	if err == nil {
		t.Fatal("expected error: logicalCount not divisible by len(shards)")
	}
}

func TestShardRouterPinger_DelegatesToPools(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectPing()

	p := &ShardRouterPinger{Router: NewSingleShardRouter(db, 1)}
	if err := p.Ping(t.Context()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

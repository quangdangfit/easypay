package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// rclient spins up an in-process Redis (miniredis) and returns a wired
// go-redis client. miniredis supports BF.* commands transparently — they
// no-op + return an OK reply, which is exactly what our idempotency layer
// treats as a "module not loaded, fall through" path.
func rclient(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })
	return rc, mr
}

// --- idempotency ---

func TestIdempotency_SetThenCheck(t *testing.T) {
	rc, _ := rclient(t)
	c := NewIdempotency(rc)
	ctx := context.Background()

	if err := c.Set(ctx, "M1:TXN-1", []byte(`{"x":1}`), time.Minute); err != nil {
		t.Fatalf("set: %v", err)
	}
	exists, body, err := c.Check(ctx, "M1:TXN-1")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	// miniredis doesn't implement BF.EXISTS so the fallback path returns
	// "maybe" → SET hit → exists=true.
	if !exists {
		t.Fatal("expected exists")
	}
	if string(body) != `{"x":1}` {
		t.Fatalf("body: %s", body)
	}
}

func TestIdempotency_MissingKey(t *testing.T) {
	rc, _ := rclient(t)
	c := NewIdempotency(rc)
	exists, _, err := c.Check(context.Background(), "absent")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if exists {
		t.Fatal("expected miss")
	}
}

// --- rate limiter ---

func TestRateLimiter_AllowsUnderLimit(t *testing.T) {
	rc, _ := rclient(t)
	rl := NewRateLimiter(rc)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		ok, remaining, err := rl.Allow(ctx, "M1", 5, time.Minute)
		if err != nil || !ok {
			t.Fatalf("call %d: ok=%v err=%v", i, ok, err)
		}
		if remaining < 0 || remaining > 5 {
			t.Fatalf("call %d: remaining=%d", i, remaining)
		}
	}
}

func TestRateLimiter_BlocksOverLimit(t *testing.T) {
	rc, _ := rclient(t)
	rl := NewRateLimiter(rc)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, _, _ = rl.Allow(ctx, "M2", 3, time.Minute)
	}
	ok, remaining, err := rl.Allow(ctx, "M2", 3, time.Minute)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Fatal("expected block")
	}
	if remaining != 0 {
		t.Fatalf("remaining: %d", remaining)
	}
}

func TestRateLimiter_ZeroLimitDeniesAll(t *testing.T) {
	rc, _ := rclient(t)
	rl := NewRateLimiter(rc)
	ok, _, err := rl.Allow(context.Background(), "M3", 0, time.Minute)
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

// --- locker ---

func TestLocker_AcquireAndReleaseAllowsReacquire(t *testing.T) {
	rc, _ := rclient(t)
	l := NewLocker(rc)
	ctx := context.Background()

	lk1, err := l.Acquire(ctx, "ord-1", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lk1.Release(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}
	lk2, err := l.Acquire(ctx, "ord-1", time.Minute)
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	_ = lk2.Release(ctx)
}

func TestLocker_RejectsConcurrentAcquire(t *testing.T) {
	rc, _ := rclient(t)
	l := NewLocker(rc)
	ctx := context.Background()
	lk, err := l.Acquire(ctx, "ord-2", time.Minute)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer func() { _ = lk.Release(ctx) }()
	if _, err := l.Acquire(ctx, "ord-2", time.Minute); !errors.Is(err, ErrLockNotAcquired) {
		t.Fatalf("expected ErrLockNotAcquired, got %v", err)
	}
}

func TestLocker_ReleaseNilSafe(t *testing.T) {
	var l *redisLock
	if err := l.Release(context.Background()); err != nil {
		t.Fatalf("nil-release should be safe: %v", err)
	}
}

// --- token bucket ---

func TestTokenBucket_AllowsThenBlocks(t *testing.T) {
	rc, _ := rclient(t)
	b := NewTokenBucket(rc, "k", 3, 1)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := b.Allow(ctx); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if err := b.Allow(ctx); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
}

// --- pending order store ---

func TestPendingOrderStore_PutGet(t *testing.T) {
	rc, _ := rclient(t)
	s := NewPendingOrderStore(rc)
	ctx := context.Background()
	o := &PendingOrder{OrderID: "ORD-1", MerchantID: "M1", Amount: 100, Currency: "USD"}
	if err := s.Put(ctx, o, time.Minute); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Get(ctx, "ORD-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.OrderID != o.OrderID || got.Amount != o.Amount {
		t.Fatalf("mismatch: %+v vs %+v", got, o)
	}
}

func TestPendingOrderStore_Missing(t *testing.T) {
	rc, _ := rclient(t)
	s := NewPendingOrderStore(rc)
	if _, err := s.Get(context.Background(), "absent"); !errors.Is(err, ErrPendingOrderNotFound) {
		t.Fatalf("want ErrPendingOrderNotFound, got %v", err)
	}
}

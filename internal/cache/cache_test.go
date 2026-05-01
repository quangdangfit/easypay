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
// go-redis client.
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

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

func TestRateLimiter_NegativeLimitDeniesAll(t *testing.T) {
	rc, _ := rclient(t)
	rl := NewRateLimiter(rc)
	ok, remaining, err := rl.Allow(context.Background(), "M4", -1, time.Minute)
	if err != nil || ok || remaining != 0 {
		t.Fatalf("ok=%v remaining=%d err=%v", ok, remaining, err)
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

// --- URL cache ---

func TestURLCache_StoreAndRetrieve(t *testing.T) {
	c := NewURLCache(10, 5*time.Second)
	c.Put("k", "https://example.com")
	if v, ok := c.Get("k"); !ok || v != "https://example.com" {
		t.Fatalf("cache miss or wrong value: %v", v)
	}
}

func TestURLCache_MissWhenEmpty(t *testing.T) {
	c := NewURLCache(10, 5*time.Second)
	if _, ok := c.Get("missing"); ok {
		t.Fatal("expected cache miss")
	}
}

func TestURLCache_ExpiresAfterTTL(t *testing.T) {
	c := NewURLCache(10, 100*time.Millisecond)
	c.Put("k", "https://example.com")
	time.Sleep(150 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected expiration")
	}
}

func TestURLCache_InvalidateRemovesEntry(t *testing.T) {
	c := NewURLCache(10, 5*time.Second)
	c.Put("k", "https://example.com")
	c.Invalidate("k")
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected invalidation to remove entry")
	}
}

// --- locker error cases ---

func TestLocker_AcquireHandlesRedisError(t *testing.T) {
	rc, mr := rclient(t)
	l := NewLocker(rc)
	ctx := context.Background()

	mr.Close() // break redis
	_, err := l.Acquire(ctx, "key", time.Minute)
	if err == nil {
		t.Fatal("expected error on closed redis")
	}
}

func TestLocker_ReleaseHandlesRedisError(t *testing.T) {
	rc, mr := rclient(t)
	l := NewLocker(rc)
	ctx := context.Background()

	lk, _ := l.Acquire(ctx, "key", time.Minute)
	mr.Close()
	err := lk.Release(ctx)
	if err == nil {
		t.Fatal("expected error on closed redis")
	}
}

// --- rate limiter error cases ---

func TestRateLimiter_AllowHandlesRedisError(t *testing.T) {
	rc, mr := rclient(t)
	rl := NewRateLimiter(rc)
	ctx := context.Background()

	mr.Close()
	_, _, err := rl.Allow(ctx, "M1", 5, time.Minute)
	if err == nil {
		t.Fatal("expected error on closed redis")
	}
}

// --- token bucket error cases ---

func TestTokenBucket_AllowHandlesRedisError(t *testing.T) {
	rc, mr := rclient(t)
	b := NewTokenBucket(rc, "k", 3, 1)
	ctx := context.Background()

	mr.Close()
	err := b.Allow(ctx)
	if err == nil {
		t.Fatal("expected error on closed redis")
	}
}

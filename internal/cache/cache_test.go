package cache

import (
	"context"
	"errors"
	"fmt"
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

func TestRateLimiter_MultipleKeys_IndependentLimits(t *testing.T) {
	rc, _ := rclient(t)
	rl := NewRateLimiter(rc)
	ctx := context.Background()

	// Two different keys should have independent limits
	for i := 0; i < 3; i++ {
		ok, _, _ := rl.Allow(ctx, "key1", 3, time.Minute)
		if !ok {
			t.Fatalf("key1 call %d should be allowed", i)
		}
	}

	for i := 0; i < 3; i++ {
		ok, _, _ := rl.Allow(ctx, "key2", 3, time.Minute)
		if !ok {
			t.Fatalf("key2 call %d should be allowed", i)
		}
	}

	// Both should now be at limit
	ok1, _, _ := rl.Allow(ctx, "key1", 3, time.Minute)
	ok2, _, _ := rl.Allow(ctx, "key2", 3, time.Minute)
	if ok1 || ok2 {
		t.Fatal("both keys should be at limit now")
	}
}

func TestRateLimiter_RemainingCount(t *testing.T) {
	rc, _ := rclient(t)
	rl := NewRateLimiter(rc)
	ctx := context.Background()

	ok, remaining, err := rl.Allow(ctx, "M1", 5, time.Minute)
	if err != nil || !ok {
		t.Fatalf("first call: ok=%v err=%v", ok, err)
	}
	// Should have 4 remaining
	if remaining != 4 {
		t.Fatalf("remaining: %d, want 4", remaining)
	}

	// Use 3 more
	for i := 0; i < 3; i++ {
		rl.Allow(ctx, "M1", 5, time.Minute)
	}

	// Check remaining is 0 on 5th call
	ok2, remaining2, _ := rl.Allow(ctx, "M1", 5, time.Minute)
	if !ok2 || remaining2 != 0 {
		t.Fatalf("should allow 5th call, remaining=%d", remaining2)
	}
}

// --- Additional coverage for edge cases ---

func TestURLCache_ConcurrentAccess(t *testing.T) {
	c := NewURLCache(100, 5*time.Second)
	c.Put("k1", "v1")

	// Multiple concurrent reads should not panic
	for i := 0; i < 10; i++ {
		go func() {
			c.Get("k1")
		}()
	}

	// Concurrent writes
	for i := 0; i < 5; i++ {
		go func(i int) {
			c.Put(fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
		}(i)
	}

	time.Sleep(100 * time.Millisecond)
	if v, ok := c.Get("k1"); !ok || v != "v1" {
		t.Fatal("concurrent access broke cache")
	}
}

func TestRateLimiter_LargeLimit(t *testing.T) {
	rc, _ := rclient(t)
	rl := NewRateLimiter(rc)
	ctx := context.Background()

	// Test with very large limit
	ok, remaining, err := rl.Allow(ctx, "huge", 10000, time.Minute)
	if err != nil || !ok {
		t.Fatalf("should allow: err=%v", err)
	}
	if remaining != 9999 {
		t.Fatalf("remaining: %d, want 9999", remaining)
	}
}

func TestTokenBucket_ExactCapacity(t *testing.T) {
	rc, _ := rclient(t)
	b := NewTokenBucket(rc, "exact", 5, 10.0) // 5 capacity, 10 tokens/sec
	ctx := context.Background()

	// Fill exactly to capacity
	for i := 0; i < 5; i++ {
		if err := b.Allow(ctx); err != nil {
			t.Fatalf("call %d should allow: %v", i, err)
		}
	}

	// Next should fail
	if err := b.Allow(ctx); err == nil {
		t.Fatal("should be rate limited at capacity")
	}
}

func TestRateLimiter_WindowBoundary(t *testing.T) {
	rc, _ := rclient(t)
	rl := NewRateLimiter(rc)
	ctx := context.Background()

	// Use up the limit
	for i := 0; i < 3; i++ {
		rl.Allow(ctx, "boundary", 3, 100*time.Millisecond)
	}

	// Should be blocked
	ok, _, _ := rl.Allow(ctx, "boundary", 3, 100*time.Millisecond)
	if ok {
		t.Fatal("should be blocked")
	}

	// Wait for window to expire
	time.Sleep(150 * time.Millisecond)

	// Should be allowed again
	ok, _, _ = rl.Allow(ctx, "boundary", 3, 100*time.Millisecond)
	if !ok {
		t.Fatal("should be allowed after window expires")
	}
}

// --- URL cache eviction ---

func TestURLCache_EvictsExpiredWhenFull(t *testing.T) {
	c := NewURLCache(2, 100*time.Millisecond)
	c.Put("k1", "v1")
	time.Sleep(150 * time.Millisecond)
	c.Put("k2", "v2")
	// Cache is now full with k1 expired and k2 fresh
	c.Put("k3", "v3")
	// Should have evicted expired k1, so cache has k2, k3
	if _, ok := c.Get("k1"); ok {
		t.Fatal("expired k1 should have been evicted")
	}
	if _, ok := c.Get("k2"); !ok {
		t.Fatal("fresh k2 should still exist")
	}
	if _, ok := c.Get("k3"); !ok {
		t.Fatal("new k3 should exist")
	}
}

func TestURLCache_EvictsArbitraryWhenFullAndNoExpired(t *testing.T) {
	c := NewURLCache(2, 10*time.Second)
	c.Put("k1", "v1")
	c.Put("k2", "v2")
	// Cache is full with two non-expired entries
	c.Put("k3", "v3")
	// Should have evicted one arbitrary entry (k1 or k2)
	count := 0
	if _, ok := c.Get("k1"); ok {
		count++
	}
	if _, ok := c.Get("k2"); ok {
		count++
	}
	if _, ok := c.Get("k3"); !ok {
		t.Fatal("new k3 should exist")
	}
	if count != 1 {
		t.Fatalf("expected exactly one of k1/k2 to remain, got %d", count)
	}
}

func TestURLCache_UpdateExistingDoesNotEvict(t *testing.T) {
	c := NewURLCache(2, 10*time.Second)
	c.Put("k1", "v1")
	c.Put("k2", "v2")
	// Update existing key should not trigger eviction
	c.Put("k1", "v1-new")
	v, ok := c.Get("k1")
	if !ok || v != "v1-new" {
		t.Fatal("update should succeed without eviction")
	}
	if _, ok := c.Get("k2"); !ok {
		t.Fatal("k2 should still exist")
	}
}

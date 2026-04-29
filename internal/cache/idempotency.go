package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// IdempotencyChecker is the interface used by the payment service to dedupe
// inbound merchant requests across retries.
type IdempotencyChecker interface {
	// Check returns whether key is known. If true and a cached body exists
	// the body is returned for the caller to short-circuit with.
	Check(ctx context.Context, key string) (exists bool, cachedResponse []byte, err error)
	// Set stores key + response with a TTL. Idempotent (uses SET NX).
	Set(ctx context.Context, key string, response []byte, ttl time.Duration) error
}

type redisIdempotency struct {
	rc       *redis.Client
	bfShards int
}

// NewIdempotency wires Bloom-filter-backed idempotency on top of redis.
// Bloom filter is created lazily on first BF.ADD via BF.RESERVE.
func NewIdempotency(rc *redis.Client) IdempotencyChecker {
	return &redisIdempotency{rc: rc, bfShards: 8}
}

func (r *redisIdempotency) bloomKey(key string) string {
	// Distribute across a small number of bloom shards based on the key hash.
	var h uint32
	for i := 0; i < len(key); i++ {
		h = h*31 + uint32(key[i])
	}
	return fmt.Sprintf("bloom:tx:%d", int(h)%r.bfShards)
}

func (r *redisIdempotency) idemKey(key string) string {
	return "idem:" + key
}

func (r *redisIdempotency) Check(ctx context.Context, key string) (bool, []byte, error) {
	bf := r.bloomKey(key)
	maybe, err := r.rc.Do(ctx, "BF.EXISTS", bf, key).Bool()
	if err != nil && !errors.Is(err, redis.Nil) {
		// If BF module is unavailable or key missing, fall back to plain GET.
		maybe = true
	}
	if !maybe {
		return false, nil, nil
	}
	// Bloom said "maybe" — check Redis cache for cached response.
	val, err := r.rc.Get(ctx, r.idemKey(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("idem get: %w", err)
	}
	return true, val, nil
}

func (r *redisIdempotency) Set(ctx context.Context, key string, response []byte, ttl time.Duration) error {
	bf := r.bloomKey(key)
	// Best-effort: BF module may be missing; the Redis SET below is authoritative.
	_ = r.rc.Do(ctx, "BF.ADD", bf, key).Err()
	if _, err := r.rc.SetNX(ctx, r.idemKey(key), response, ttl).Result(); err != nil {
		return fmt.Errorf("idem set: %w", err)
	}
	return nil
}

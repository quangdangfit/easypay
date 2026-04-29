package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// TokenBucket is a distributed rate limiter port. Allow() either reserves
// one token and returns nil, or returns ErrRateLimited.
type TokenBucket interface {
	Allow(ctx context.Context) error
}

// redisTokenBucket is the Redis-backed implementation. A constant rate of
// `refill` tokens/second is added up to `capacity`. Atomic via Lua script.
type redisTokenBucket struct {
	rc       *redis.Client
	key      string
	capacity int
	refill   float64 // tokens per second
}

var ErrRateLimited = errors.New("rate limited")

func NewTokenBucket(rc *redis.Client, key string, capacity int, ratePerSec float64) TokenBucket {
	return &redisTokenBucket{rc: rc, key: "tb:" + key, capacity: capacity, refill: ratePerSec}
}

const tbScript = `
local key       = KEYS[1]
local capacity  = tonumber(ARGV[1])
local refill    = tonumber(ARGV[2])  -- tokens per second
local now_ms    = tonumber(ARGV[3])  -- current time, ms

local data = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts     = tonumber(data[2])
if tokens == nil then tokens = capacity end
if ts == nil then ts = now_ms end

local elapsed = math.max(0, now_ms - ts) / 1000.0
tokens = math.min(capacity, tokens + elapsed * refill)

local allowed = 0
local wait_ms = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  wait_ms = math.ceil((1 - tokens) / refill * 1000)
end

redis.call('HSET', key, 'tokens', tokens, 'ts', now_ms)
redis.call('PEXPIRE', key, math.ceil(capacity / refill * 1000) + 5000)
return { allowed, wait_ms }
`

func (b *redisTokenBucket) Allow(ctx context.Context) error {
	res, err := b.rc.Eval(ctx, tbScript,
		[]string{b.key},
		b.capacity, b.refill, time.Now().UnixMilli(),
	).Result()
	if err != nil {
		return fmt.Errorf("token bucket eval: %w", err)
	}
	arr, ok := res.([]any)
	if !ok || len(arr) != 2 {
		return fmt.Errorf("token bucket bad reply: %v", res)
	}
	allowed, _ := arr[0].(int64)
	if allowed == 1 {
		return nil
	}
	return ErrRateLimited
}

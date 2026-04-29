package cache

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimiter implements a sliding-window counter via Redis sorted sets.
// Each request adds a member scored by unix-nanos; oldest entries past the
// window are trimmed and the remaining count is checked against the limit.
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, remaining int, err error)
}

type redisRateLimiter struct {
	rc *redis.Client
}

func NewRateLimiter(rc *redis.Client) RateLimiter {
	return &redisRateLimiter{rc: rc}
}

const slidingWindowScript = `
local key       = KEYS[1]
local now       = tonumber(ARGV[1])
local windowNs  = tonumber(ARGV[2])
local limit     = tonumber(ARGV[3])
local member    = ARGV[4]

redis.call('ZREMRANGEBYSCORE', key, 0, now - windowNs)
local count = redis.call('ZCARD', key)
if count >= limit then
  return { 0, limit - count }
end
redis.call('ZADD', key, now, member)
redis.call('PEXPIRE', key, math.ceil(windowNs / 1000000))
return { 1, limit - (count + 1) }
`

func (r *redisRateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, error) {
	if limit <= 0 {
		return false, 0, nil
	}
	now := time.Now().UnixNano()
	member := strconv.FormatInt(now, 10)
	res, err := r.rc.Eval(ctx, slidingWindowScript,
		[]string{"rate:" + key},
		now, int64(window), limit, member,
	).Result()
	if err != nil {
		return false, 0, fmt.Errorf("ratelimit eval: %w", err)
	}
	arr, ok := res.([]any)
	if !ok || len(arr) != 2 {
		return false, 0, fmt.Errorf("ratelimit unexpected reply: %v", res)
	}
	allowed, _ := arr[0].(int64)
	remaining, _ := arr[1].(int64)
	if remaining < 0 {
		remaining = 0
	}
	return allowed == 1, int(remaining), nil
}

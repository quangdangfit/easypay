package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var ErrLockNotAcquired = errors.New("lock not acquired")

// Locker is the port consumed by services that need a distributed mutex.
// Implementations include redisLocker (default) and may include in-process
// fakes for tests.
type Locker interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (Lock, error)
}

// Lock is the handle returned by Locker.Acquire. Release uses CAS so we
// never delete a lock acquired by someone else.
type Lock interface {
	Release(ctx context.Context) error
}

type redisLocker struct {
	rc *redis.Client
}

func NewLocker(rc *redis.Client) Locker {
	return &redisLocker{rc: rc}
}

func (l *redisLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (Lock, error) {
	token := uuid.NewString()
	ok, err := l.rc.SetNX(ctx, "lock:"+key, token, ttl).Result()
	if err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	if !ok {
		return nil, ErrLockNotAcquired
	}
	return &redisLock{rc: l.rc, key: "lock:" + key, token: token}, nil
}

type redisLock struct {
	rc    *redis.Client
	key   string
	token string
}

const releaseScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

func (lk *redisLock) Release(ctx context.Context) error {
	if lk == nil {
		return nil
	}
	if err := lk.rc.Eval(ctx, releaseScript, []string{lk.key}, lk.token).Err(); err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("release lock: %w", err)
	}
	return nil
}

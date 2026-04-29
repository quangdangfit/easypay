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

// Lock holds a distributed lock. Call Release to free it; Release uses a
// CAS Lua script so we never delete a lock acquired by someone else.
type Lock struct {
	rc    *redis.Client
	key   string
	token string
}

type Locker struct {
	rc *redis.Client
}

func NewLocker(rc *redis.Client) *Locker {
	return &Locker{rc: rc}
}

// Acquire returns a Lock or ErrLockNotAcquired if another holder owns it.
func (l *Locker) Acquire(ctx context.Context, key string, ttl time.Duration) (*Lock, error) {
	token := uuid.NewString()
	ok, err := l.rc.SetNX(ctx, "lock:"+key, token, ttl).Result()
	if err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	if !ok {
		return nil, ErrLockNotAcquired
	}
	return &Lock{rc: l.rc, key: "lock:" + key, token: token}, nil
}

const releaseScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

func (lk *Lock) Release(ctx context.Context) error {
	if lk == nil {
		return nil
	}
	if err := lk.rc.Eval(ctx, releaseScript, []string{lk.key}, lk.token).Err(); err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("release lock: %w", err)
	}
	return nil
}

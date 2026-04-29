package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// FraudChecker scores incoming requests. score >= Threshold should be rejected
// upstream. The default impl uses a velocity check: count of recent attempts
// per merchant + customer key.
type FraudChecker interface {
	Check(ctx context.Context, merchantID, userID string, amount int64) (score int, err error)
}

type velocityChecker struct {
	rc        *redis.Client
	window    time.Duration
	threshold int
}

func NewVelocityChecker(rc *redis.Client) FraudChecker {
	return &velocityChecker{
		rc:        rc,
		window:    time.Minute,
		threshold: 20,
	}
}

func (v *velocityChecker) Check(ctx context.Context, merchantID, userID string, amount int64) (int, error) {
	if userID == "" {
		// No user identity to track velocity on; defer to other layers.
		return 0, nil
	}
	key := fmt.Sprintf("fraud:vel:%s:%s", merchantID, userID)
	now := time.Now().UnixNano()
	min := now - int64(v.window)

	pipe := v.rc.Pipeline()
	pipe.ZRemRangeByScore(ctx, key, "0", strconv.FormatInt(min, 10))
	addCmd := pipe.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: now})
	cardCmd := pipe.ZCard(ctx, key)
	pipe.PExpire(ctx, key, v.window+time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("velocity exec: %w", err)
	}
	_ = addCmd

	count, err := cardCmd.Result()
	if err != nil {
		return 0, fmt.Errorf("velocity zcard: %w", err)
	}
	if count >= int64(v.threshold) {
		return 100, nil
	}
	return int(count * 100 / int64(v.threshold)), nil
}

package cache

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/quangdangfit/easypay/internal/config"
)

func NewRedis(cfg config.RedisConfig) (*redis.Client, error) {
	rc := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	ctx, cancel := contextWithTimeout()
	defer cancel()
	if err := rc.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return rc, nil
}

// RedisPinger adapts *redis.Client to handler.Pinger.
type RedisPinger struct{ Client *redis.Client }

func (p *RedisPinger) Ping(ctx context.Context) error { return p.Client.Ping(ctx).Err() }

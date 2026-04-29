package cache

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/quangdangfit/easypay/internal/config"
)

func TestNewRedis_Connects(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	rc, err := NewRedis(config.RedisConfig{Addr: mr.Addr()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = rc.Close() }()

	p := &RedisPinger{Client: rc}
	if err := p.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestNewRedis_DialError(t *testing.T) {
	_, err := NewRedis(config.RedisConfig{Addr: "127.0.0.1:1"})
	if err == nil {
		t.Fatal("expected dial error")
	}
}

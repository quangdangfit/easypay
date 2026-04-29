package logger

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type ctxKey string

const (
	keyRequestID  ctxKey = "request_id"
	keyMerchantID ctxKey = "merchant_id"
	keyOrderID    ctxKey = "order_id"
)

var defaultLogger *slog.Logger

func Init(level string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	defaultLogger = slog.New(h)
	slog.SetDefault(defaultLogger)
}

func L() *slog.Logger {
	if defaultLogger == nil {
		Init("info")
	}
	return defaultLogger
}

func With(ctx context.Context) *slog.Logger {
	l := L()
	if v, ok := ctx.Value(keyRequestID).(string); ok && v != "" {
		l = l.With("request_id", v)
	}
	if v, ok := ctx.Value(keyMerchantID).(string); ok && v != "" {
		l = l.With("merchant_id", v)
	}
	if v, ok := ctx.Value(keyOrderID).(string); ok && v != "" {
		l = l.With("order_id", v)
	}
	return l
}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyRequestID, id)
}

func WithMerchantID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyMerchantID, id)
}

func WithOrderID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyOrderID, id)
}

func RequestID(ctx context.Context) string {
	v, _ := ctx.Value(keyRequestID).(string)
	return v
}

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/api"
	"github.com/quangdangfit/easypay/internal/api/handler"
	"github.com/quangdangfit/easypay/internal/cache"
	"github.com/quangdangfit/easypay/internal/config"
	"github.com/quangdangfit/easypay/internal/consumer"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/internal/service"
	"github.com/quangdangfit/easypay/pkg/logger"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger.Init(cfg.App.LogLevel)
	log := logger.L()
	log.Info("starting easypay", "env", cfg.App.Env, "port", cfg.App.Port)

	// MySQL.
	db, err := repository.OpenMySQL(cfg.DB)
	if err != nil {
		return fmt.Errorf("mysql: %w", err)
	}
	defer db.Close()

	// Redis.
	rc, err := cache.NewRedis(cfg.Redis)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer rc.Close()

	// Kafka producer.
	publisher := kafka.NewPublisher(cfg.Kafka)
	defer publisher.Close()

	// Repos & cache helpers.
	orderRepo := repository.NewOrderRepository(db)
	merchantRepo := repository.NewMerchantRepository(db)
	idem := cache.NewIdempotency(rc)
	rl := cache.NewRateLimiter(rc)

	// Stripe — Phase 4 will swap in the real client; until then use the stub.
	stripeClient := stripe.NewStub()

	// Service + handlers.
	paySvc := service.NewPaymentService(idem, stripeClient, publisher, cfg.Stripe.DefaultCurrency,
		cfg.Blockchain.ContractAddress, cfg.Blockchain.ChainID)
	payH := handler.NewPaymentHandler(paySvc)
	payStatusH := handler.NewPaymentStatusHandler(orderRepo)

	healthH := handler.NewHealthHandler(
		&repository.MySQLPinger{DB: db},
		&cache.RedisPinger{Client: rc},
		kafka.NewPinger(cfg.Kafka),
	)

	app := api.NewRouter(api.Deps{
		Health:        healthH,
		Payment:       payH,
		PaymentStatus: payStatusH,
		Merchants:     merchantRepo,
		RateLimiter:   rl,
		HMACSkew:      cfg.Security.HMACTimestampSkew,
	})

	// Async batch consumer for payment.events → MySQL.
	consumerCtx, consumerCancel := context.WithCancel(context.Background())
	defer consumerCancel()
	paymentConsumer := consumer.NewPaymentConsumer(orderRepo).NewBatch(cfg.Kafka)
	go func() {
		if err := paymentConsumer.Run(consumerCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("payment consumer exited", "err", err)
		}
	}()

	addr := fmt.Sprintf(":%d", cfg.App.Port)
	errCh := make(chan error, 1)
	go func() {
		if err := app.Listen(addr); err != nil && !errors.Is(err, fiber.ErrServiceUnavailable) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-stop:
		log.Info("shutdown signal received", "signal", sig.String())
	case err := <-errCh:
		return fmt.Errorf("server: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	consumerCancel()
	log.Info("server stopped cleanly")
	return nil
}

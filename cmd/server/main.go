package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/joho/godotenv"

	"github.com/ethereum/go-ethereum/common"

	"github.com/quangdangfit/easypay/internal/api"
	"github.com/quangdangfit/easypay/internal/api/handler"
	"github.com/quangdangfit/easypay/internal/cache"
	"github.com/quangdangfit/easypay/internal/config"
	"github.com/quangdangfit/easypay/internal/consumer"
	"github.com/quangdangfit/easypay/internal/kafka"
	"github.com/quangdangfit/easypay/internal/provider/blockchain"
	"github.com/quangdangfit/easypay/internal/provider/stripe"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/internal/service"
	"github.com/quangdangfit/easypay/pkg/logger"
)

func lookupSecretByMerchantID(ctx context.Context, db *sql.DB, merchantID string) string {
	var secret string
	err := db.QueryRowContext(ctx, "SELECT secret_key FROM merchants WHERE merchant_id = ? LIMIT 1", merchantID).Scan(&secret)
	if err != nil {
		return ""
	}
	return secret
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	// Best-effort load of .env (dev convenience). Real envs use the platform's
	// secret manager; we don't error if the file is absent.
	_ = godotenv.Load()

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

	// Stripe.
	stripeClient := stripe.NewClient(cfg.Stripe.SecretKey, cfg.Stripe.WebhookSecret, cfg.Stripe.APIVersion)

	// Service + handlers.
	paySvc := service.NewPaymentService(idem, stripeClient, publisher, cfg.Stripe.DefaultCurrency,
		cfg.Blockchain.ContractAddress, cfg.Blockchain.ChainID)
	webhookSvc := service.NewWebhookService(stripeClient, orderRepo, publisher, rc, cfg.Stripe.WebhookSecret)
	payH := handler.NewPaymentHandler(paySvc)
	payStatusH := handler.NewPaymentStatusHandler(orderRepo)
	refundH := handler.NewRefundHandler(webhookSvc)
	webhookH := handler.NewWebhookHandler(webhookSvc)

	healthH := handler.NewHealthHandler(
		&repository.MySQLPinger{DB: db},
		&cache.RedisPinger{Client: rc},
		kafka.NewPinger(cfg.Kafka),
	)

	app := api.NewRouter(api.Deps{
		Health:        healthH,
		Payment:       payH,
		PaymentStatus: payStatusH,
		Refund:        refundH,
		Webhook:       webhookH,
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

	// Settlement consumer for payment.confirmed → merchant callback.
	merchantSecretLookup := func(merchantID string) string {
		// Best-effort sync lookup — settlement runs async so a 1s timeout is fine.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		// Repository is keyed by api_key not merchant_id; we'd ideally have
		// a GetByMerchantID. For now we tolerate a small extra query.
		// TODO(safety-nets phase): introduce proper GetByMerchantID.
		return lookupSecretByMerchantID(ctx, db, merchantID)
	}
	settlementConsumer := consumer.NewSettlementConsumer(merchantSecretLookup).NewBatch(cfg.Kafka)
	go func() {
		if err := settlementConsumer.Run(consumerCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("settlement consumer exited", "err", err)
		}
	}()

	// Order reconciliation cron (Phase 6).
	orderReconciler := service.NewOrderReconciliation(orderRepo, stripeClient, publisher)
	go func() {
		if err := orderReconciler.Run(consumerCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("order reconciler exited", "err", err)
		}
	}()

	// Blockchain listener (Phase 5). Only spin up if a contract address is configured.
	if cfg.Blockchain.ContractAddress != "" && cfg.Blockchain.RPCWebsocket != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		chainClient, err := blockchain.NewClient(ctx, cfg.Blockchain.RPCWebsocket, cfg.Blockchain.RPCHTTP)
		cancel()
		if err != nil {
			log.Warn("blockchain client unavailable, listener disabled", "err", err)
		} else {
			pendingTxRepo := repository.NewPendingTxRepository(db)
			cursor := blockchain.NewMySQLCursor(db)
			chainCfg := blockchain.ChainConfig{
				ChainID:               cfg.Blockchain.ChainID,
				ContractAddress:       common.HexToAddress(cfg.Blockchain.ContractAddress),
				RequiredConfirmations: cfg.Blockchain.RequiredConfirmations,
				StartBlock:            cfg.Blockchain.StartBlock,
			}
			listener := blockchain.NewListener(chainClient, chainCfg, cursor, pendingTxRepo, orderRepo, publisher)
			go listener.Run(consumerCtx)
		}
	}

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

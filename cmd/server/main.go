package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"

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

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "path to YAML config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger.Init(cfg.App.LogLevel)
	log := logger.L()
	log.Info("starting easypay", "env", cfg.App.Env, "port", cfg.App.Port)

	// MySQL — one pool per physical shard. The router maps a merchant's
	// logical shard_index → physical pool. With db.shards empty, the router
	// collapses to a single pool from db.dsn (legacy / dev).
	router, err := repository.OpenShards(cfg.DB, cfg.App.LogicalShardCount)
	if err != nil {
		return fmt.Errorf("mysql shards: %w", err)
	}
	defer func() { _ = router.Close() }()

	// Redis.
	rc, err := cache.NewRedis(cfg.Redis)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer func() { _ = rc.Close() }()

	// Kafka producer (only `payment.confirmed` is published from this app
	// now — the write path commits to MySQL synchronously and does not fan
	// out via Kafka).
	publisher := kafka.NewPublisher(cfg.Kafka)
	defer func() { _ = publisher.Close() }()

	// Repos & cache helpers.
	orderRepo := repository.NewOrderRepository(router)
	merchantRepo := repository.NewMerchantRepository(router, cfg.App.LogicalShardCount)
	rl := cache.NewRateLimiter(rc)
	locker := cache.NewLocker(rc)
	urlCache := cache.NewURLCache(10000, 5*time.Second)
	stripeBucket := cache.NewTokenBucket(rc, "stripe:create_session", cfg.App.StripeRateLimit, float64(cfg.App.StripeRateLimit))

	// Stripe — pick implementation by mode (live = SDK, fake = in-process).
	var stripeClient stripe.Client
	switch cfg.Stripe.Mode {
	case "fake":
		stripeClient = stripe.NewFake()
		log.Warn("STRIPE_MODE=fake — using in-process synthetic responses, no real Stripe calls")
	default:
		stripeClient = stripe.NewClient(cfg.Stripe.SecretKey, cfg.Stripe.WebhookSecret, cfg.Stripe.APIVersion)
	}
	// Wrap with circuit breaker so a bad Stripe day doesn't drag the gateway down.
	stripeClient = stripe.NewBreakerClient(stripeClient, "stripe")

	// Service + handlers.
	paySvc := service.NewPaymentService(stripeClient, orderRepo, service.PaymentServiceOptions{
		DefaultCurrency:   cfg.Stripe.DefaultCurrency,
		CryptoContract:    cfg.Blockchain.ContractAddress,
		CryptoChainID:     cfg.Blockchain.ChainID,
		LazyCheckout:      cfg.App.LazyCheckout,
		PublicBaseURL:     cfg.App.PublicBaseURL,
		CheckoutSecret:    cfg.App.CheckoutTokenSecret,
		CheckoutTokenTTL:  cfg.App.CheckoutTokenTTL,
		DefaultSuccessURL: cfg.App.CheckoutDefaultSuccessURL,
		DefaultCancelURL:  cfg.App.CheckoutDefaultCancelURL,
	})
	merchantSvc := service.NewMerchantService(merchantRepo)
	webhookSvc := service.NewWebhookService(stripeClient, orderRepo, merchantRepo, publisher, rc, cfg.Stripe.WebhookSecret)
	checkoutResolver := service.NewCheckoutResolver(service.CheckoutResolverOptions{
		Stripe:            stripeClient,
		Repo:              orderRepo,
		Merchants:         merchantRepo,
		Locker:            locker,
		URLCache:          urlCache,
		Bucket:            stripeBucket,
		DefaultSuccessURL: cfg.App.CheckoutDefaultSuccessURL,
		DefaultCancelURL:  cfg.App.CheckoutDefaultCancelURL,
	})
	payH := handler.NewPaymentHandler(paySvc)
	payStatusH := handler.NewPaymentStatusHandler(orderRepo)
	refundH := handler.NewRefundHandler(webhookSvc)
	webhookH := handler.NewWebhookHandler(webhookSvc)
	checkoutH := handler.NewCheckoutHandler(checkoutResolver, cfg.App.CheckoutTokenSecret)
	merchantH := handler.NewMerchantHandler(merchantSvc)

	healthH := handler.NewHealthHandler(
		&repository.ShardRouterPinger{Router: router},
		&cache.RedisPinger{Client: rc},
		kafka.NewPinger(cfg.Kafka),
	)

	app := api.NewRouter(api.Deps{
		Health:        healthH,
		Payment:       payH,
		PaymentStatus: payStatusH,
		Refund:        refundH,
		Webhook:       webhookH,
		Checkout:      checkoutH,
		Merchant:      merchantH,
		Merchants:     merchantRepo,
		RateLimiter:   rl,
		HMACSkew:      cfg.Security.HMACTimestampSkew,
		AdminAPIKey:   cfg.Security.AdminAPIKey,
	})

	// Settlement consumer for payment.confirmed → merchant callback.
	// Looks up callback URL + signing secret per event so merchants can
	// rotate either without bouncing the gateway.
	consumerCtx, consumerCancel := context.WithCancel(context.Background())
	defer consumerCancel()
	merchantLookup := func(merchantID string) consumer.MerchantCallback {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m, err := merchantRepo.GetByMerchantID(ctx, merchantID)
		if err != nil {
			return consumer.MerchantCallback{}
		}
		return consumer.MerchantCallback{URL: m.CallbackURL, Secret: m.SecretKey}
	}
	settlementConsumer := consumer.NewSettlementConsumer(merchantLookup).NewBatch(cfg.Kafka)
	go func() {
		if err := settlementConsumer.Run(consumerCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("settlement consumer exited", "err", err)
		}
	}()

	// Order reconciliation cron.
	orderReconciler := service.NewOrderReconciliation(orderRepo, stripeClient, publisher)
	go func() {
		if err := orderReconciler.Run(consumerCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("order reconciler exited", "err", err)
		}
	}()

	// Blockchain listener. Only spin up if a contract address is configured.
	if cfg.Blockchain.ContractAddress != "" && cfg.Blockchain.RPCWebsocket != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		chainClient, err := blockchain.NewClient(ctx, cfg.Blockchain.RPCWebsocket, cfg.Blockchain.RPCHTTP)
		cancel()
		if err != nil {
			log.Warn("blockchain client unavailable, listener disabled", "err", err)
		} else {
			onchainTxRepo := repository.NewOnchainTxRepository(router)
			cursor := blockchain.NewMySQLCursor(router)
			chainCfg := blockchain.ChainConfig{
				ChainID:               cfg.Blockchain.ChainID,
				ContractAddress:       common.HexToAddress(cfg.Blockchain.ContractAddress),
				RequiredConfirmations: cfg.Blockchain.RequiredConfirmations,
				StartBlock:            cfg.Blockchain.StartBlock,
			}
			listener := blockchain.NewListener(chainClient, chainCfg, cursor, onchainTxRepo, orderRepo, publisher)
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

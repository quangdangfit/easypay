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
	"github.com/quangdangfit/easypay/internal/config"
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

	// In Phase 1, dependencies (DB / Redis / Kafka) are not wired yet — pass nil and
	// readiness will simply report no checks. They will be wired in later phases.
	healthH := handler.NewHealthHandler(nil, nil, nil)

	app := api.NewRouter(api.Deps{Health: healthH})

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
	log.Info("server stopped cleanly")
	return nil
}

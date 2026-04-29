package api

import (
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/api/handler"
	"github.com/quangdangfit/easypay/internal/api/middleware"
	"github.com/quangdangfit/easypay/internal/cache"
	"github.com/quangdangfit/easypay/internal/repository"
)

type Deps struct {
	Health        *handler.HealthHandler
	Payment       *handler.PaymentHandler
	PaymentStatus *handler.PaymentStatusHandler
	Merchants     repository.MerchantRepository
	RateLimiter   cache.RateLimiter
	HMACSkew      time.Duration
}

func NewRouter(deps Deps) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:               "easypay",
		DisableStartupMessage: true,
		StrictRouting:         false,
		CaseSensitive:         false,
	})

	app.Use(middleware.RequestID())

	// Public health endpoints (no auth).
	app.Get("/healthz", deps.Health.Liveness)
	app.Get("/readyz", deps.Health.Readiness)

	// Merchant API (HMAC + rate limit).
	merchantAPI := app.Group("/api")
	if deps.Merchants != nil {
		merchantAPI.Use(middleware.HMACAuth(deps.Merchants, deps.HMACSkew))
	}
	if deps.RateLimiter != nil {
		merchantAPI.Use(middleware.RateLimit(deps.RateLimiter))
	}
	if deps.Payment != nil {
		merchantAPI.Post("/payments", deps.Payment.Create)
	}
	if deps.PaymentStatus != nil {
		merchantAPI.Get("/payments/:id", deps.PaymentStatus.Get)
	}

	return app
}

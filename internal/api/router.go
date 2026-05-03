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
	Refund        *handler.RefundHandler
	Webhook       *handler.WebhookHandler
	Checkout      *handler.CheckoutHandler
	Merchant      *handler.MerchantHandler
	Merchants     repository.MerchantRepository
	RateLimiter   cache.RateLimiter
	HMACSkew      time.Duration
	// AdminAPIKey gates the /admin/* group. When empty, admin routes are
	// not mounted at all.
	AdminAPIKey string
}

func NewRouter(deps Deps) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:               "easypay",
		DisableStartupMessage: true,
		StrictRouting:         false,
		CaseSensitive:         false,
	})

	app.Use(middleware.RequestID())
	app.Use(middleware.PrometheusMiddleware())

	// Public health + metrics (no auth).
	app.Get("/healthz", deps.Health.Liveness)
	app.Get("/readyz", deps.Health.Readiness)
	app.Get("/metrics", handler.PrometheusHandler())

	// Stripe webhook — outside HMAC merchant auth (Stripe signs with its own
	// secret, verified inside the handler).
	if deps.Webhook != nil {
		app.Post("/webhook/stripe", deps.Webhook.Stripe)
	}

	// Public hosted checkout (lazy-create + redirect to Stripe) and the
	// post-checkout success/cancel pages Stripe redirects users to.
	if deps.Checkout != nil {
		app.Get("/pay/:merchant_id/:order_id", deps.Checkout.Redirect)
		app.Get("/checkout/success", deps.Checkout.Success)
		app.Get("/checkout/cancel", deps.Checkout.Cancel)
	}

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
	if deps.Refund != nil {
		merchantAPI.Post("/payments/:id/refund", deps.Refund.Create)
	}

	// Admin API (X-Admin-Key). Mounted only when an admin key is configured;
	// production deployments must set ADMIN_API_KEY to enable.
	if deps.AdminAPIKey != "" && deps.Merchant != nil {
		admin := app.Group("/admin", middleware.AdminAuth(deps.AdminAPIKey))
		admin.Post("/merchants", deps.Merchant.Create)
	}

	return app
}

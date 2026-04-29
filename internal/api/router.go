package api

import (
	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/api/handler"
	"github.com/quangdangfit/easypay/internal/api/middleware"
)

type Deps struct {
	Health *handler.HealthHandler
}

func NewRouter(deps Deps) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:               "easypay",
		DisableStartupMessage: true,
		ReadTimeout:           0,
		StrictRouting:         false,
		CaseSensitive:         false,
	})

	app.Use(middleware.RequestID())

	app.Get("/healthz", deps.Health.Liveness)
	app.Get("/readyz", deps.Health.Readiness)

	return app
}

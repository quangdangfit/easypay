package handler

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/pkg/response"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

type HealthHandler struct {
	DB    Pinger
	Redis Pinger
	Kafka Pinger
}

func NewHealthHandler(db, redis, kafka Pinger) *HealthHandler {
	return &HealthHandler{DB: db, Redis: redis, Kafka: kafka}
}

func (h *HealthHandler) Liveness(c *fiber.Ctx) error {
	return response.OK(c, fiber.Map{"status": "alive"})
}

func (h *HealthHandler) Readiness(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.UserContext(), 2*time.Second)
	defer cancel()

	checks := fiber.Map{}
	allOK := true

	if h.DB != nil {
		if err := h.DB.Ping(ctx); err != nil {
			checks["db"] = "down: " + err.Error()
			allOK = false
		} else {
			checks["db"] = "ok"
		}
	}
	if h.Redis != nil {
		if err := h.Redis.Ping(ctx); err != nil {
			checks["redis"] = "down: " + err.Error()
			allOK = false
		} else {
			checks["redis"] = "ok"
		}
	}
	if h.Kafka != nil {
		if err := h.Kafka.Ping(ctx); err != nil {
			checks["kafka"] = "down: " + err.Error()
			allOK = false
		} else {
			checks["kafka"] = "ok"
		}
	}

	if !allOK {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"status": "not_ready", "checks": checks})
	}
	return response.OK(c, fiber.Map{"status": "ready", "checks": checks})
}

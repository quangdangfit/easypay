package middleware

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/quangdangfit/easypay/pkg/logger"
)

const HeaderRequestID = "X-Request-ID"

func RequestID() fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Get(HeaderRequestID)
		if id == "" {
			id = uuid.NewString()
		}
		c.Set(HeaderRequestID, id)
		c.Locals("request_id", id)
		ctx := logger.WithRequestID(c.UserContext(), id)
		c.SetUserContext(ctx)
		return c.Next()
	}
}

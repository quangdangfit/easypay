package middleware

import (
	"crypto/subtle"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/pkg/response"
)

// HeaderAdminKey is the header carrying the operator's admin key.
const HeaderAdminKey = "X-Admin-Key"

// AdminAuth verifies the X-Admin-Key header equals the configured shared
// secret using a constant-time comparison. When expected is empty the
// middleware refuses every request — admin routes should not be mounted
// at all in that case (see router.go).
func AdminAuth(expected string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if expected == "" {
			return response.Unauthorized(c, "admin_disabled", "admin endpoints disabled")
		}
		got := c.Get(HeaderAdminKey)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			return response.Unauthorized(c, "bad_admin_key", "X-Admin-Key invalid or missing")
		}
		return c.Next()
	}
}

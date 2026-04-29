package middleware

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/cache"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/pkg/response"
)

// RateLimit applies a per-merchant sliding window using the merchant's
// configured `rate_limit` (requests per minute). The merchant must already be
// attached via HMACAuth.
func RateLimit(rl cache.RateLimiter) fiber.Handler {
	return func(c *fiber.Ctx) error {
		m, ok := c.Locals(LocalsMerchant).(*domain.Merchant)
		if !ok || m == nil {
			return c.Next()
		}
		limit := m.RateLimit
		if limit <= 0 {
			limit = 1000
		}
		allowed, remaining, err := rl.Allow(c.UserContext(), m.MerchantID, limit, time.Minute)
		if err != nil {
			// Fail open on cache error; we'd rather accept than reject in an outage.
			return c.Next()
		}
		c.Set("X-RateLimit-Limit", strconv.Itoa(limit))
		c.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		if !allowed {
			return response.TooManyRequests(c, "rate_limited", "merchant rate limit exceeded")
		}
		return c.Next()
	}
}

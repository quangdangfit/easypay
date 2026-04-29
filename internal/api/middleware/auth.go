package middleware

import (
	"context"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/pkg/hmac"
	"github.com/quangdangfit/easypay/pkg/logger"
	"github.com/quangdangfit/easypay/pkg/response"
)

const (
	HeaderAPIKey    = "X-API-Key"
	HeaderTimestamp = "X-Timestamp"
	HeaderSignature = "X-Signature"

	LocalsMerchant = "merchant"
)

// HMACAuth verifies that:
//   - X-API-Key matches a known merchant
//   - X-Timestamp drift is within `skew`
//   - X-Signature == HMAC-SHA256(timestamp + "." + raw_body, merchant.secret_key)
func HMACAuth(merchants repository.MerchantRepository, skew time.Duration) fiber.Handler {
	return func(c *fiber.Ctx) error {
		apiKey := c.Get(HeaderAPIKey)
		ts := c.Get(HeaderTimestamp)
		sig := c.Get(HeaderSignature)
		if apiKey == "" || ts == "" || sig == "" {
			return response.Unauthorized(c, "missing_auth_headers", "X-API-Key, X-Timestamp, X-Signature are required")
		}

		tsInt, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			return response.Unauthorized(c, "bad_timestamp", "X-Timestamp must be unix seconds")
		}
		now := time.Now().Unix()
		if abs(now-tsInt) > int64(skew.Seconds()) {
			return response.Unauthorized(c, "timestamp_skew", "X-Timestamp outside acceptable skew")
		}

		ctx, cancel := context.WithTimeout(c.UserContext(), 2*time.Second)
		defer cancel()
		merchant, err := merchants.GetByAPIKey(ctx, apiKey)
		if err != nil {
			return response.Unauthorized(c, "unknown_api_key", "API key not recognized")
		}
		if merchant.Status != domain.MerchantStatusActive {
			return response.Unauthorized(c, "merchant_suspended", "merchant is not active")
		}

		body := c.Body()
		signed := append([]byte(ts+"."), body...)
		if !hmac.Verify(merchant.SecretKey, signed, sig) {
			return response.Unauthorized(c, "bad_signature", "signature does not match")
		}

		c.Locals(LocalsMerchant, merchant)
		c.SetUserContext(logger.WithMerchantID(c.UserContext(), merchant.MerchantID))
		return c.Next()
	}
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

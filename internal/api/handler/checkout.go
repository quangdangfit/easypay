package handler

import (
	"errors"
	"fmt"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/service"
	"github.com/quangdangfit/easypay/pkg/checkouttoken"
	"github.com/quangdangfit/easypay/pkg/logger"
)

type CheckoutHandler struct {
	resolver    *service.CheckoutResolver
	tokenSecret string
}

// NewCheckoutHandler. tokenSecret should be a stable secret shared across
// pods; if empty, /pay/:id accepts any order_id (dev only).
func NewCheckoutHandler(r *service.CheckoutResolver, tokenSecret string) *CheckoutHandler {
	return &CheckoutHandler{resolver: r, tokenSecret: tokenSecret}
}

// Redirect handles GET /pay/:id?t=<token>.
//
// Outcomes:
//   - happy path → 302 → Stripe checkout (sub-100ms p99)
//   - order not yet committed → 503 + auto-refreshing HTML (2s)
//   - circuit open / rate limited → 503 + branded "try again" HTML
//   - bad/expired token → 410 Gone with branded HTML
func (h *CheckoutHandler) Redirect(c *fiber.Ctx) error {
	orderID := c.Params("id")
	if orderID == "" {
		return h.sendInvalid(c)
	}

	if h.tokenSecret != "" {
		token := c.Query("t")
		if token == "" {
			return h.sendInvalid(c)
		}
		if err := checkouttoken.Verify(h.tokenSecret, orderID, token); err != nil {
			logger.With(c.UserContext()).Info("checkout token rejected", "order_id", orderID, "err", err)
			return h.sendInvalid(c)
		}
	}

	url, err := h.resolver.Resolve(c.UserContext(), orderID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrOrderNotReady):
			return h.sendNotReady(c)
		case errors.Is(err, service.ErrUnavailable):
			return h.sendUnavailable(c)
		default:
			logger.With(c.UserContext()).Warn("checkout resolve failed", "order_id", orderID, "err", err)
			return h.sendUnavailable(c)
		}
	}

	// Don't cache the redirect — Stripe Sessions can change/expire.
	c.Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	c.Set("Pragma", "no-cache")
	return c.Redirect(url, fiber.StatusFound)
}

func (h *CheckoutHandler) sendNotReady(c *fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	c.Set("Cache-Control", "no-store")
	c.Set("Retry-After", "2")
	return c.Status(fiber.StatusServiceUnavailable).SendString(checkoutNotReadyHTML)
}

func (h *CheckoutHandler) sendUnavailable(c *fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	c.Set("Cache-Control", "no-store")
	c.Set("Retry-After", "30")
	ref := logger.RequestID(c.UserContext())
	return c.Status(fiber.StatusServiceUnavailable).SendString(fmt.Sprintf(checkoutErrorHTML, ref))
}

func (h *CheckoutHandler) sendInvalid(c *fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	c.Set("Cache-Control", "no-store")
	return c.Status(fiber.StatusGone).SendString(checkoutInvalidHTML)
}

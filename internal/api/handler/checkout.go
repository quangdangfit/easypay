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
	resolver    service.Checkouts
	tokenSecret string
}

// NewCheckoutHandler. tokenSecret should be a stable secret shared across
// pods; if empty, /pay/:id accepts any order_id (dev only).
func NewCheckoutHandler(r service.Checkouts, tokenSecret string) *CheckoutHandler {
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
		log := logger.With(c.UserContext()).With("order_id", orderID, "err", err.Error())
		switch {
		case errors.Is(err, service.ErrOrderNotReady):
			log.Info("checkout resolve: order not ready")
			return h.sendNotReady(c)
		case errors.Is(err, service.ErrUnavailable):
			log.Warn("checkout resolve: provider unavailable (rate-limit / breaker)")
			return h.sendUnavailable(c)
		default:
			log.Warn("checkout resolve: unexpected error")
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

// Success serves /checkout/success — Stripe redirects users here after a
// successful payment. We don't trust this signal for state changes (the
// webhook does that), this page is purely UX.
func (h *CheckoutHandler) Success(c *fiber.Ctx) error {
	orderID := c.Query("order_id")
	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	c.Set("Cache-Control", "no-store")
	return c.SendString(fmt.Sprintf(checkoutSuccessHTML, orderID))
}

// Cancel serves /checkout/cancel — Stripe redirects users here when they
// abort the payment. Same caveat as Success: UX only.
func (h *CheckoutHandler) Cancel(c *fiber.Ctx) error {
	orderID := c.Query("order_id")
	c.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	c.Set("Cache-Control", "no-store")
	return c.SendString(fmt.Sprintf(checkoutCancelHTML, orderID))
}

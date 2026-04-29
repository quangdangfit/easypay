package handler

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/service"
	"github.com/quangdangfit/easypay/pkg/response"
)

type CheckoutHandler struct {
	resolver *service.CheckoutResolver
}

func NewCheckoutHandler(r *service.CheckoutResolver) *CheckoutHandler {
	return &CheckoutHandler{resolver: r}
}

// Redirect handles GET /pay/:id. Resolves (or lazily creates) the Stripe
// Checkout URL for the order, then 302-redirects the user to it.
//
// If the order isn't yet visible (consumer lag), respond 503 with a
// Retry-After header so the user/browser can retry quickly.
func (h *CheckoutHandler) Redirect(c *fiber.Ctx) error {
	id := c.Params("id")
	if id == "" {
		return response.BadRequest(c, "missing_id", "order id required")
	}
	url, err := h.resolver.Resolve(c.UserContext(), id)
	if err != nil {
		if errors.Is(err, service.ErrOrderNotReady) {
			c.Set("Retry-After", "1")
			return response.Fail(c, fiber.StatusServiceUnavailable, "not_ready", "order not yet available, retry shortly")
		}
		return response.InternalError(c, "resolve_failed", err.Error())
	}
	return c.Redirect(url, fiber.StatusFound)
}

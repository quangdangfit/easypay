package handler

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/api/middleware"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/repository"
	"github.com/quangdangfit/easypay/pkg/response"
)

type PaymentStatusHandler struct {
	repo repository.OrderRepository
}

func NewPaymentStatusHandler(repo repository.OrderRepository) *PaymentStatusHandler {
	return &PaymentStatusHandler{repo: repo}
}

type paymentStatusResponse struct {
	OrderID               string `json:"order_id"`
	TransactionID         string `json:"transaction_id"`
	Status                string `json:"status"`
	Amount                int64  `json:"amount"`
	Currency              string `json:"currency"`
	PaymentMethod         string `json:"payment_method,omitempty"`
	StripeSessionID       string `json:"stripe_session_id,omitempty"`
	StripePaymentIntentID string `json:"stripe_payment_intent_id,omitempty"`
	CheckoutURL           string `json:"checkout_url,omitempty"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
}

// Get handles GET /api/payments/:id. Authorisation: a merchant can only
// retrieve their own orders.
func (h *PaymentStatusHandler) Get(c *fiber.Ctx) error {
	id := c.Params("id")
	if id == "" {
		return response.BadRequest(c, "missing_id", "order id required")
	}
	merchant, ok := c.Locals(middleware.LocalsMerchant).(*domain.Merchant)
	if !ok || merchant == nil {
		return response.Unauthorized(c, "missing_merchant", "merchant not authenticated")
	}

	order, err := h.repo.GetByOrderID(c.UserContext(), id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return response.Fail(c, fiber.StatusNotFound, "not_found", "order not found")
		}
		return response.InternalError(c, "lookup_failed", err.Error())
	}
	if order.MerchantID != merchant.MerchantID {
		// Don't leak existence — same code as not found.
		return response.Fail(c, fiber.StatusNotFound, "not_found", "order not found")
	}

	out := paymentStatusResponse{
		OrderID:               order.OrderID,
		TransactionID:         order.TransactionID,
		Status:                string(order.Status),
		Amount:                order.Amount,
		Currency:              order.Currency,
		PaymentMethod:         order.PaymentMethod,
		StripeSessionID:       order.StripeSessionID,
		StripePaymentIntentID: order.StripePaymentIntentID,
		CheckoutURL:           order.CheckoutURL,
		CreatedAt:             order.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:             order.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	return response.OK(c, out)
}

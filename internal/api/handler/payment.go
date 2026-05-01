package handler

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/api/middleware"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/service"
	"github.com/quangdangfit/easypay/pkg/response"
)

type PaymentHandler struct {
	svc service.Payments
}

func NewPaymentHandler(svc service.Payments) *PaymentHandler {
	return &PaymentHandler{svc: svc}
}

type createPaymentRequest struct {
	TransactionID      string   `json:"transaction_id"`
	Amount             int64    `json:"amount"`
	Currency           string   `json:"currency"`
	PaymentMethodTypes []string `json:"payment_method_types"`
	CustomerEmail      string   `json:"customer_email"`
	SuccessURL         string   `json:"success_url"`
	CancelURL          string   `json:"cancel_url"`
	CallbackURL        string   `json:"callback_url"`
}

// Create handles POST /api/payments. ?method=crypto routes to the on-chain flow.
func (h *PaymentHandler) Create(c *fiber.Ctx) error {
	merchant, ok := c.Locals(middleware.LocalsMerchant).(*domain.Merchant)
	if !ok || merchant == nil {
		return response.Unauthorized(c, "missing_merchant", "merchant not authenticated")
	}

	var req createPaymentRequest
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "bad_json", "invalid JSON body")
	}

	in := service.CreatePaymentInput{
		Merchant:           merchant,
		TransactionID:      req.TransactionID,
		Amount:             req.Amount,
		Currency:           req.Currency,
		PaymentMethodTypes: req.PaymentMethodTypes,
		CustomerEmail:      req.CustomerEmail,
		SuccessURL:         req.SuccessURL,
		CancelURL:          req.CancelURL,
		CallbackURL:        firstNonEmpty(req.CallbackURL, merchant.CallbackURL),
		Method:             c.Query("method"),
	}

	res, err := h.svc.Create(c.UserContext(), in)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidRequest):
			return response.BadRequest(c, "invalid_request", err.Error())
		case errors.Is(err, service.ErrTransactionConflict):
			return response.Conflict(c, "transaction_conflict", err.Error())
		default:
			return response.InternalError(c, "create_failed", err.Error())
		}
	}
	return response.Accepted(c, res)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

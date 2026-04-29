package handler

import (
	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/api/middleware"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/service"
	"github.com/quangdangfit/easypay/pkg/logger"
	"github.com/quangdangfit/easypay/pkg/response"
)

type RefundHandler struct {
	svc *service.WebhookService
}

func NewRefundHandler(svc *service.WebhookService) *RefundHandler {
	return &RefundHandler{svc: svc}
}

type refundRequest struct {
	Amount int64  `json:"amount"`
	Reason string `json:"reason"`
}

// Create handles POST /api/payments/:id/refund.
func (h *RefundHandler) Create(c *fiber.Ctx) error {
	merchant, ok := c.Locals(middleware.LocalsMerchant).(*domain.Merchant)
	if !ok || merchant == nil {
		return response.Unauthorized(c, "missing_merchant", "merchant not authenticated")
	}
	id := c.Params("id")
	if id == "" {
		return response.BadRequest(c, "missing_id", "order id required")
	}
	var req refundRequest
	if err := c.BodyParser(&req); err != nil {
		// Empty body is allowed (full refund).
		req = refundRequest{}
	}
	idemKey := "refund:" + id + ":" + logger.RequestID(c.UserContext())
	res, err := h.svc.CreateRefund(c.UserContext(), service.RefundInput{
		Merchant: merchant,
		OrderID:  id,
		Amount:   req.Amount,
		Reason:   req.Reason,
		IdemKey:  idemKey,
	})
	if err != nil {
		return response.InternalError(c, "refund_failed", err.Error())
	}
	return response.Accepted(c, res)
}

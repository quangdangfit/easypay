package handler

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/service"
	"github.com/quangdangfit/easypay/pkg/response"
)

// MerchantHandler is the operator-facing handler for managing merchants.
// Mounted under /admin behind AdminAuth middleware.
type MerchantHandler struct {
	svc service.Merchants
}

func NewMerchantHandler(svc service.Merchants) *MerchantHandler {
	return &MerchantHandler{svc: svc}
}

type createMerchantRequest struct {
	MerchantID  string `json:"merchant_id"`
	Name        string `json:"name"`
	CallbackURL string `json:"callback_url"`
	RateLimit   int    `json:"rate_limit"`
}

// Create handles POST /admin/merchants. Returns the freshly minted api_key
// and secret_key in plaintext — they exist only in this response and the
// merchants table.
func (h *MerchantHandler) Create(c *fiber.Ctx) error {
	var req createMerchantRequest
	if err := c.BodyParser(&req); err != nil {
		return response.BadRequest(c, "bad_json", "invalid JSON body")
	}
	res, err := h.svc.Create(c.UserContext(), service.CreateMerchantInput{
		MerchantID:  req.MerchantID,
		Name:        req.Name,
		CallbackURL: req.CallbackURL,
		RateLimit:   req.RateLimit,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidRequest):
			return response.BadRequest(c, "invalid_request", err.Error())
		default:
			return response.InternalError(c, "create_failed", err.Error())
		}
	}
	return response.Created(c, res)
}

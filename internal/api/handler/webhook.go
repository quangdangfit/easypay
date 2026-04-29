package handler

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/service"
	"github.com/quangdangfit/easypay/pkg/logger"
)

const stripeSignatureHeader = "Stripe-Signature"

type WebhookHandler struct {
	svc service.Webhooks
}

func NewWebhookHandler(svc service.Webhooks) *WebhookHandler {
	return &WebhookHandler{svc: svc}
}

// Stripe handles POST /webhook/stripe. We must respond 2xx in <5s; any heavy
// downstream work happens in Kafka consumers.
func (h *WebhookHandler) Stripe(c *fiber.Ctx) error {
	sig := c.Get(stripeSignatureHeader)
	body := c.Body()

	err := h.svc.Process(c.UserContext(), body, sig)
	if err == nil {
		return c.SendStatus(fiber.StatusOK)
	}
	if errors.Is(err, service.ErrWebhookDuplicate) {
		// Duplicate is success — Stripe should stop retrying.
		return c.SendStatus(fiber.StatusOK)
	}
	logger.With(c.UserContext()).Warn("stripe webhook rejected", "err", err)
	// 400 — Stripe will retry. Use 4xx for verifiable client errors (bad sig)
	// to make it explicit; persistent transient errors use 5xx upstream.
	return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
}

package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/api/middleware"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/service"
)

type fakeWebhooks struct {
	processErr error
	refund     *service.RefundResult
	refundErr  error
}

func (f *fakeWebhooks) Process(ctx context.Context, payload []byte, sig string) error {
	return f.processErr
}
func (f *fakeWebhooks) CreateRefund(ctx context.Context, in service.RefundInput) (*service.RefundResult, error) {
	return f.refund, f.refundErr
}

func newRefundApp(svc service.Webhooks) *fiber.App {
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(middleware.LocalsMerchant, &domain.Merchant{MerchantID: "M1", Status: domain.MerchantStatusActive})
		return c.Next()
	})
	h := NewRefundHandler(svc)
	app.Post("/api/payments/:id/refund", h.Create)
	return app
}

func TestRefundHandler_HappyPath(t *testing.T) {
	app := newRefundApp(&fakeWebhooks{refund: &service.RefundResult{
		OrderID: "ORD-1", RefundID: "re_x", Status: "succeeded", Amount: 500, Currency: "usd",
	}})
	req := httptest.NewRequest("POST", "/api/payments/ORD-1/refund", strings.NewReader(`{"amount":500,"reason":"requested_by_customer"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 202 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRefundHandler_EmptyBodyAllowed(t *testing.T) {
	app := newRefundApp(&fakeWebhooks{refund: &service.RefundResult{OrderID: "x", RefundID: "re"}})
	req := httptest.NewRequest("POST", "/api/payments/ORD-1/refund", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != 202 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRefundHandler_PropagatesError(t *testing.T) {
	app := newRefundApp(&fakeWebhooks{refundErr: errors.New("stripe down")})
	req := httptest.NewRequest("POST", "/api/payments/ORD-1/refund", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != 500 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRefundHandler_RejectsMissingMerchant(t *testing.T) {
	app := fiber.New()
	h := NewRefundHandler(&fakeWebhooks{})
	app.Post("/api/payments/:id/refund", h.Create)
	resp, _ := app.Test(httptest.NewRequest("POST", "/api/payments/x/refund", nil))
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRefundHandler_RejectsMissingID(t *testing.T) {
	app := newRefundApp(&fakeWebhooks{})
	app.Post("/api/payments//refund", func(c *fiber.Ctx) error {
		// Fiber strips empty params, so we hit the catch route to test BadRequest path explicitly.
		return NewRefundHandler(&fakeWebhooks{}).Create(c)
	})
	req := httptest.NewRequest("POST", "/api/payments//refund", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != 400 && resp.StatusCode != 404 {
		t.Fatalf("expected 400/404, got %d", resp.StatusCode)
	}
}

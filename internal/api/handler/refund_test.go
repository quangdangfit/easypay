package handler

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/mock/gomock"

	"github.com/quangdangfit/easypay/internal/api/middleware"
	"github.com/quangdangfit/easypay/internal/domain"
	svcmock "github.com/quangdangfit/easypay/internal/mocks/service"
	"github.com/quangdangfit/easypay/internal/service"
)

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
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockWebhooks(ctrl)
	svc.EXPECT().CreateRefund(gomock.Any(), gomock.Any()).
		Return(&service.RefundResult{OrderID: "ord-1", RefundID: "re_x", Status: "succeeded", Amount: 500, Currency: "usd"}, nil)

	app := newRefundApp(svc)
	req := httptest.NewRequest("POST", "/api/payments/ord-1/refund", strings.NewReader(`{"amount":500,"reason":"requested_by_customer"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 202 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRefundHandler_EmptyBodyAllowed(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockWebhooks(ctrl)
	svc.EXPECT().CreateRefund(gomock.Any(), gomock.Any()).
		Return(&service.RefundResult{OrderID: "x", RefundID: "re"}, nil)

	app := newRefundApp(svc)
	req := httptest.NewRequest("POST", "/api/payments/ord-1/refund", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != 202 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRefundHandler_PropagatesError(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockWebhooks(ctrl)
	svc.EXPECT().CreateRefund(gomock.Any(), gomock.Any()).Return(nil, errors.New("stripe down"))

	app := newRefundApp(svc)
	req := httptest.NewRequest("POST", "/api/payments/ord-1/refund", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != 500 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRefundHandler_RejectsMissingMerchant(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockWebhooks(ctrl)

	app := fiber.New()
	h := NewRefundHandler(svc)
	app.Post("/api/payments/:id/refund", h.Create)
	resp, _ := app.Test(httptest.NewRequest("POST", "/api/payments/x/refund", nil))
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRefundHandler_RejectsMissingID(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockWebhooks(ctrl)

	app := newRefundApp(svc)
	app.Post("/api/payments//refund", func(c *fiber.Ctx) error {
		// Fiber strips empty params, so we hit the catch route to test BadRequest path explicitly.
		return NewRefundHandler(svc).Create(c)
	})
	req := httptest.NewRequest("POST", "/api/payments//refund", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != 400 && resp.StatusCode != 404 {
		t.Fatalf("expected 400/404, got %d", resp.StatusCode)
	}
}

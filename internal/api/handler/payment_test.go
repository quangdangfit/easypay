package handler

import (
	"context"
	"errors"
	"io"
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

func newPaymentApp(svc service.Payments) (*fiber.App, *domain.Merchant) {
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s", Status: domain.MerchantStatusActive}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(middleware.LocalsMerchant, merchant)
		return c.Next()
	})
	h := NewPaymentHandler(svc)
	app.Post("/api/payments", h.Create)
	return app, merchant
}

func TestPaymentHandler_HappyPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockPayments(ctrl)
	var captured service.CreatePaymentInput
	svc.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, in service.CreatePaymentInput) (*service.CreatePaymentResult, error) {
			captured = in
			return &service.CreatePaymentResult{OrderID: "ord-1", Status: "accepted"}, nil
		})

	app, _ := newPaymentApp(svc)
	body := `{"order_id":"ORDER-1","amount":1500,"currency":"USD"}`
	req := httptest.NewRequest("POST", "/api/payments", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 202 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	if captured.OrderID != "ORDER-1" {
		t.Fatalf("svc not invoked properly: %+v", captured)
	}
}

func TestPaymentHandler_RejectsBadJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockPayments(ctrl)
	// Bad JSON is rejected by the handler before reaching the service.

	app, _ := newPaymentApp(svc)
	req := httptest.NewRequest("POST", "/api/payments", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPaymentHandler_PropagatesInvalidRequest(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockPayments(ctrl)
	svc.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, service.ErrInvalidRequest)

	app, _ := newPaymentApp(svc)
	req := httptest.NewRequest("POST", "/api/payments", strings.NewReader(`{"order_id":"X"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPaymentHandler_PropagatesGenericError(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockPayments(ctrl)
	svc.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, errors.New("kaboom"))

	app, _ := newPaymentApp(svc)
	req := httptest.NewRequest("POST", "/api/payments", strings.NewReader(`{"order_id":"X","amount":1}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 500 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPaymentHandler_RejectsMissingMerchant(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockPayments(ctrl)

	app := fiber.New()
	h := NewPaymentHandler(svc)
	app.Post("/api/payments", h.Create) // no merchant injected
	req := httptest.NewRequest("POST", "/api/payments", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPaymentHandler_QueryMethodPropagated(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockPayments(ctrl)
	var captured service.CreatePaymentInput
	svc.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, in service.CreatePaymentInput) (*service.CreatePaymentResult, error) {
			captured = in
			return &service.CreatePaymentResult{OrderID: "ord-1"}, nil
		})

	app, _ := newPaymentApp(svc)
	req := httptest.NewRequest("POST", "/api/payments?method=crypto", strings.NewReader(`{"order_id":"X","amount":1}`))
	req.Header.Set("Content-Type", "application/json")
	if _, err := app.Test(req); err != nil {
		t.Fatal(err)
	}
	if captured.Method != "crypto" {
		t.Fatalf("method: %s", captured.Method)
	}
}

package handler

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/api/middleware"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/service"
)

type fakePayments struct {
	last service.CreatePaymentInput
	res  *service.CreatePaymentResult
	err  error
}

func (f *fakePayments) Create(ctx context.Context, in service.CreatePaymentInput) (*service.CreatePaymentResult, error) {
	f.last = in
	return f.res, f.err
}

func newPaymentApp(svc service.Payments) (*fiber.App, *domain.Merchant) {
	merchant := &domain.Merchant{MerchantID: "M1", SecretKey: "s", Status: domain.MerchantStatusActive}
	app := fiber.New()
	// Inject merchant via Locals so we don't need real HMAC middleware here.
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(middleware.LocalsMerchant, merchant)
		return c.Next()
	})
	h := NewPaymentHandler(svc)
	app.Post("/api/payments", h.Create)
	return app, merchant
}

func TestPaymentHandler_HappyPath(t *testing.T) {
	svc := &fakePayments{res: &service.CreatePaymentResult{OrderID: "ORD-1", Status: "accepted"}}
	app, _ := newPaymentApp(svc)

	body := `{"transaction_id":"TXN-1","amount":1500,"currency":"USD"}`
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
	if svc.last.TransactionID != "TXN-1" {
		t.Fatalf("svc not invoked properly: %+v", svc.last)
	}
}

func TestPaymentHandler_RejectsBadJSON(t *testing.T) {
	app, _ := newPaymentApp(&fakePayments{})
	req := httptest.NewRequest("POST", "/api/payments", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPaymentHandler_PropagatesInvalidRequest(t *testing.T) {
	svc := &fakePayments{err: service.ErrInvalidRequest}
	app, _ := newPaymentApp(svc)
	req := httptest.NewRequest("POST", "/api/payments", strings.NewReader(`{"transaction_id":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPaymentHandler_PropagatesGenericError(t *testing.T) {
	svc := &fakePayments{err: errors.New("kaboom")}
	app, _ := newPaymentApp(svc)
	req := httptest.NewRequest("POST", "/api/payments", strings.NewReader(`{"transaction_id":"x","amount":1}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 500 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPaymentHandler_RejectsMissingMerchant(t *testing.T) {
	app := fiber.New()
	h := NewPaymentHandler(&fakePayments{})
	app.Post("/api/payments", h.Create) // no merchant injected
	req := httptest.NewRequest("POST", "/api/payments", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPaymentHandler_QueryMethodPropagated(t *testing.T) {
	svc := &fakePayments{res: &service.CreatePaymentResult{OrderID: "ORD-1"}}
	app, _ := newPaymentApp(svc)
	req := httptest.NewRequest("POST", "/api/payments?method=crypto", strings.NewReader(`{"transaction_id":"x","amount":1}`))
	req.Header.Set("Content-Type", "application/json")
	if _, err := app.Test(req); err != nil {
		t.Fatal(err)
	}
	if svc.last.Method != "crypto" {
		t.Fatalf("method: %s", svc.last.Method)
	}
}

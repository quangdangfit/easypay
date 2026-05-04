package handler

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/mock/gomock"

	svcmock "github.com/quangdangfit/easypay/internal/mocks/service"
	"github.com/quangdangfit/easypay/internal/service"
)

func TestWebhook_HappyPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockWebhooks(ctrl)
	svc.EXPECT().Process(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	app := fiber.New()
	app.Post("/webhook/stripe", NewWebhookHandler(svc).Stripe)
	resp, _ := app.Test(httptest.NewRequest("POST", "/webhook/stripe", strings.NewReader("{}")))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestWebhook_DuplicateReturns200(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockWebhooks(ctrl)
	svc.EXPECT().Process(gomock.Any(), gomock.Any(), gomock.Any()).Return(service.ErrWebhookDuplicate)

	app := fiber.New()
	app.Post("/webhook/stripe", NewWebhookHandler(svc).Stripe)
	resp, _ := app.Test(httptest.NewRequest("POST", "/webhook/stripe", strings.NewReader("{}")))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d (duplicate should still be 200)", resp.StatusCode)
	}
}

func TestWebhook_InvalidReturns400(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockWebhooks(ctrl)
	svc.EXPECT().Process(gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("bad sig"))

	app := fiber.New()
	app.Post("/webhook/stripe", NewWebhookHandler(svc).Stripe)
	resp, _ := app.Test(httptest.NewRequest("POST", "/webhook/stripe", strings.NewReader("{}")))
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestWebhook_OrderMissingReturns500(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockWebhooks(ctrl)
	svc.EXPECT().Process(gomock.Any(), gomock.Any(), gomock.Any()).Return(service.ErrWebhookOrderMissing)

	app := fiber.New()
	app.Post("/webhook/stripe", NewWebhookHandler(svc).Stripe)
	resp, _ := app.Test(httptest.NewRequest("POST", "/webhook/stripe", strings.NewReader("{}")))
	if resp.StatusCode != 500 {
		t.Fatalf("status %d, want 500 for order missing", resp.StatusCode)
	}
}

func TestPrometheusHandler(t *testing.T) {
	app := fiber.New()
	app.Get("/metrics", PrometheusHandler())
	resp, _ := app.Test(httptest.NewRequest("GET", "/metrics", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

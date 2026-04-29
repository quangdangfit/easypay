package handler

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/service"
)

func TestWebhook_HappyPath(t *testing.T) {
	app := fiber.New()
	h := NewWebhookHandler(&fakeWebhooks{})
	app.Post("/webhook/stripe", h.Stripe)

	resp, _ := app.Test(httptest.NewRequest("POST", "/webhook/stripe", strings.NewReader("{}")))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestWebhook_DuplicateReturns200(t *testing.T) {
	app := fiber.New()
	h := NewWebhookHandler(&fakeWebhooks{processErr: service.ErrWebhookDuplicate})
	app.Post("/webhook/stripe", h.Stripe)
	resp, _ := app.Test(httptest.NewRequest("POST", "/webhook/stripe", strings.NewReader("{}")))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d (duplicate should still be 200)", resp.StatusCode)
	}
}

func TestWebhook_InvalidReturns400(t *testing.T) {
	app := fiber.New()
	h := NewWebhookHandler(&fakeWebhooks{processErr: errors.New("bad sig")})
	app.Post("/webhook/stripe", h.Stripe)
	resp, _ := app.Test(httptest.NewRequest("POST", "/webhook/stripe", strings.NewReader("{}")))
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
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

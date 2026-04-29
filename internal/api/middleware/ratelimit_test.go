package middleware

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/domain"
)

type fakeRL struct {
	allow bool
	err   error
}

func (f *fakeRL) Allow(ctx context.Context, key string, limit int, _ time.Duration) (bool, int, error) {
	return f.allow, limit, f.err
}

func newRLApp(rl *fakeRL, merchant *domain.Merchant) *fiber.App {
	app := fiber.New()
	if merchant != nil {
		app.Use(func(c *fiber.Ctx) error {
			c.Locals(LocalsMerchant, merchant)
			return c.Next()
		})
	}
	app.Use(RateLimit(rl))
	app.Get("/", func(c *fiber.Ctx) error { return c.SendStatus(200) })
	return app
}

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	app := newRLApp(&fakeRL{allow: true}, &domain.Merchant{MerchantID: "M1", RateLimit: 100})
	resp, _ := app.Test(httptest.NewRequest("GET", "/", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRateLimit_BlocksOverLimit(t *testing.T) {
	app := newRLApp(&fakeRL{allow: false}, &domain.Merchant{MerchantID: "M1", RateLimit: 1})
	resp, _ := app.Test(httptest.NewRequest("GET", "/", nil))
	if resp.StatusCode != 429 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRateLimit_FailsOpenOnError(t *testing.T) {
	app := newRLApp(&fakeRL{allow: false, err: errAuth}, &domain.Merchant{MerchantID: "M1", RateLimit: 1})
	resp, _ := app.Test(httptest.NewRequest("GET", "/", nil))
	// Implementation fails open on cache errors.
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRateLimit_NoMerchantSkips(t *testing.T) {
	app := newRLApp(&fakeRL{allow: false}, nil)
	resp, _ := app.Test(httptest.NewRequest("GET", "/", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRateLimit_DefaultsRateLimitWhenZero(t *testing.T) {
	app := newRLApp(&fakeRL{allow: true}, &domain.Merchant{MerchantID: "M1", RateLimit: 0})
	resp, _ := app.Test(httptest.NewRequest("GET", "/", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPrometheusMiddleware(t *testing.T) {
	app := fiber.New()
	app.Use(PrometheusMiddleware())
	app.Get("/x", func(c *fiber.Ctx) error { return c.SendStatus(200) })
	if resp, _ := app.Test(httptest.NewRequest("GET", "/x", nil)); resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

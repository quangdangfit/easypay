package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/mock/gomock"

	"github.com/quangdangfit/easypay/internal/domain"
	cachemock "github.com/quangdangfit/easypay/internal/mocks/cache"
)

// rlAllowing returns a RateLimiter mock that always allows; rlBlocking always blocks;
// rlErroring always returns errAuth.
func rlAllowing(t *testing.T) *cachemock.MockRateLimiter {
	t.Helper()
	rl := cachemock.NewMockRateLimiter(gomock.NewController(t))
	rl.EXPECT().Allow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_, _, limit, _ any) (bool, int, error) {
			return true, limit.(int), nil
		}).AnyTimes()
	return rl
}

func rlBlocking(t *testing.T) *cachemock.MockRateLimiter {
	t.Helper()
	rl := cachemock.NewMockRateLimiter(gomock.NewController(t))
	rl.EXPECT().Allow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(false, 0, nil).AnyTimes()
	return rl
}

func rlErroring(t *testing.T) *cachemock.MockRateLimiter {
	t.Helper()
	rl := cachemock.NewMockRateLimiter(gomock.NewController(t))
	rl.EXPECT().Allow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(false, 0, errAuth).AnyTimes()
	return rl
}

func newRLApp(rl *cachemock.MockRateLimiter, merchant *domain.Merchant) *fiber.App {
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
	app := newRLApp(rlAllowing(t), &domain.Merchant{MerchantID: "M1", RateLimit: 100})
	resp, _ := app.Test(httptest.NewRequest("GET", "/", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRateLimit_BlocksOverLimit(t *testing.T) {
	app := newRLApp(rlBlocking(t), &domain.Merchant{MerchantID: "M1", RateLimit: 1})
	resp, _ := app.Test(httptest.NewRequest("GET", "/", nil))
	if resp.StatusCode != 429 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRateLimit_FailsOpenOnError(t *testing.T) {
	app := newRLApp(rlErroring(t), &domain.Merchant{MerchantID: "M1", RateLimit: 1})
	resp, _ := app.Test(httptest.NewRequest("GET", "/", nil))
	// Implementation fails open on cache errors.
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRateLimit_NoMerchantSkips(t *testing.T) {
	app := newRLApp(rlBlocking(t), nil)
	resp, _ := app.Test(httptest.NewRequest("GET", "/", nil))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRateLimit_DefaultsRateLimitWhenZero(t *testing.T) {
	app := newRLApp(rlAllowing(t), &domain.Merchant{MerchantID: "M1", RateLimit: 0})
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

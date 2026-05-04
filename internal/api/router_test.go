package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/quangdangfit/easypay/internal/api/handler"
	"github.com/quangdangfit/easypay/internal/api/middleware"
	cachemock "github.com/quangdangfit/easypay/internal/mocks/cache"
	repomock "github.com/quangdangfit/easypay/internal/mocks/repo"
	svcmock "github.com/quangdangfit/easypay/internal/mocks/service"
)

func TestNewRouter_HealthAndMetrics(t *testing.T) {
	app := NewRouter(Deps{
		Health: handler.NewHealthHandler(nil, nil, nil),
	})
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		resp, err := app.Test(httptest.NewRequest("GET", path, nil))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if resp.StatusCode == 0 {
			t.Fatalf("%s: zero status", path)
		}
	}
}

func TestNewRouter_NoOptionalHandlersInstalled(t *testing.T) {
	// All optional fields nil — the router must still boot.
	app := NewRouter(Deps{Health: handler.NewHealthHandler(nil, nil, nil)})
	resp, _ := app.Test(httptest.NewRequest("POST", "/api/payments", nil))
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// When AdminAPIKey is set and Merchant is wired, /admin/merchants must be
// mounted behind AdminAuth. A request without the key returns 401; the route
// itself exists (i.e. not 404), proving the conditional mount fired.
func TestNewRouter_AdminMountedWhenKeyAndHandlerSet(t *testing.T) {
	ctrl := gomock.NewController(t)
	merchSvc := svcmock.NewMockMerchants(ctrl)

	app := NewRouter(Deps{
		Health:      handler.NewHealthHandler(nil, nil, nil),
		Merchant:    handler.NewMerchantHandler(merchSvc),
		AdminAPIKey: "super-secret",
	})

	// Without the admin header → 401, not 404 (route is mounted).
	resp, err := app.Test(httptest.NewRequest("POST", "/admin/merchants", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("status=%d, want 401 from AdminAuth", resp.StatusCode)
	}
}

// AdminAPIKey empty → /admin/* not mounted at all (404, not 401).
func TestNewRouter_AdminNotMountedWhenKeyEmpty(t *testing.T) {
	ctrl := gomock.NewController(t)
	merchSvc := svcmock.NewMockMerchants(ctrl)
	app := NewRouter(Deps{
		Health:   handler.NewHealthHandler(nil, nil, nil),
		Merchant: handler.NewMerchantHandler(merchSvc),
	})
	req := httptest.NewRequest("POST", "/admin/merchants", nil)
	req.Header.Set(middleware.HeaderAdminKey, "anything")
	resp, _ := app.Test(req)
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d, want 404 (route not mounted)", resp.StatusCode)
	}
}

// Merchant API group mounts HMACAuth + RateLimit middlewares when their deps
// are present. A request missing X-API-Key must be rejected by HMACAuth (401)
// rather than reaching the handler.
func TestNewRouter_MerchantAPIRunsHMACAndRateLimit(t *testing.T) {
	ctrl := gomock.NewController(t)
	merchants := repomock.NewMockMerchantRepository(ctrl)
	rl := cachemock.NewMockRateLimiter(ctrl)

	app := NewRouter(Deps{
		Health:      handler.NewHealthHandler(nil, nil, nil),
		Merchants:   merchants,
		RateLimiter: rl,
	})

	resp, err := app.Test(httptest.NewRequest("POST", "/api/payments", strings.NewReader("{}")))
	if err != nil {
		t.Fatal(err)
	}
	// HMACAuth fires first; missing headers → 401.
	if resp.StatusCode != 401 {
		t.Fatalf("status=%d, want 401 from HMACAuth", resp.StatusCode)
	}
}

// Test optional handlers mounting conditions
func TestNewRouter_WebhookNotMountedWhenNil(t *testing.T) {
	app := NewRouter(Deps{
		Health: handler.NewHealthHandler(nil, nil, nil),
		// Webhook: nil
	})
	resp, _ := app.Test(httptest.NewRequest("POST", "/webhook/stripe", strings.NewReader("{}")))
	if resp.StatusCode != 404 {
		t.Fatalf("webhook should not be mounted, got status=%d", resp.StatusCode)
	}
}

func TestNewRouter_PaymentNotMountedWhenNil(t *testing.T) {
	app := NewRouter(Deps{
		Health: handler.NewHealthHandler(nil, nil, nil),
		// Payment: nil
	})
	resp, _ := app.Test(httptest.NewRequest("POST", "/api/payments", strings.NewReader("{}")))
	if resp.StatusCode != 404 {
		t.Fatalf("payment route should not be mounted, got status=%d (no auth, so 404 expected)", resp.StatusCode)
	}
}

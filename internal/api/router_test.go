package api

import (
	"net/http/httptest"
	"testing"

	"github.com/quangdangfit/easypay/internal/api/handler"
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

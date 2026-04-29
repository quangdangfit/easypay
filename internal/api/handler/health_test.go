package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

type fakePinger struct{ err error }

func (f *fakePinger) Ping(ctx context.Context) error { return f.err }

func TestHealth_LivenessAlwaysOK(t *testing.T) {
	app := fiber.New()
	h := NewHealthHandler(nil, nil, nil)
	app.Get("/healthz", h.Liveness)
	resp, err := app.Test(httptest.NewRequest("GET", "/healthz", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHealth_ReadinessAllOK(t *testing.T) {
	app := fiber.New()
	h := NewHealthHandler(&fakePinger{}, &fakePinger{}, &fakePinger{})
	app.Get("/readyz", h.Readiness)
	resp, err := app.Test(httptest.NewRequest("GET", "/readyz", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHealth_ReadinessOneDown(t *testing.T) {
	app := fiber.New()
	h := NewHealthHandler(&fakePinger{}, &fakePinger{err: errors.New("redis down")}, &fakePinger{})
	app.Get("/readyz", h.Readiness)
	resp, err := app.Test(httptest.NewRequest("GET", "/readyz", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

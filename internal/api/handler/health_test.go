package handler

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/mock/gomock"

	handlermock "github.com/quangdangfit/easypay/internal/mocks/handler"
)

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
	ctrl := gomock.NewController(t)
	db := handlermock.NewMockPinger(ctrl)
	rd := handlermock.NewMockPinger(ctrl)
	kf := handlermock.NewMockPinger(ctrl)
	db.EXPECT().Ping(gomock.Any()).Return(nil)
	rd.EXPECT().Ping(gomock.Any()).Return(nil)
	kf.EXPECT().Ping(gomock.Any()).Return(nil)

	app := fiber.New()
	app.Get("/readyz", NewHealthHandler(db, rd, kf).Readiness)
	resp, err := app.Test(httptest.NewRequest("GET", "/readyz", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHealth_ReadinessOneDown(t *testing.T) {
	ctrl := gomock.NewController(t)
	db := handlermock.NewMockPinger(ctrl)
	rd := handlermock.NewMockPinger(ctrl)
	kf := handlermock.NewMockPinger(ctrl)
	// All three pings are issued in parallel; the handler waits on all of them.
	db.EXPECT().Ping(gomock.Any()).Return(nil)
	rd.EXPECT().Ping(gomock.Any()).Return(errors.New("redis down"))
	kf.EXPECT().Ping(gomock.Any()).Return(nil)

	app := fiber.New()
	app.Get("/readyz", NewHealthHandler(db, rd, kf).Readiness)
	resp, err := app.Test(httptest.NewRequest("GET", "/readyz", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

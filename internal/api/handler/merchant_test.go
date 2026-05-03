package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/mock/gomock"

	svcmock "github.com/quangdangfit/easypay/internal/mocks/service"
	"github.com/quangdangfit/easypay/internal/service"
)

func newMerchantApp(svc service.Merchants) *fiber.App {
	app := fiber.New()
	h := NewMerchantHandler(svc)
	app.Post("/admin/merchants", h.Create)
	return app
}

func TestMerchantHandler_HappyPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockMerchants(ctrl)
	var captured service.CreateMerchantInput
	svc.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, in service.CreateMerchantInput) (*service.CreateMerchantResult, error) {
			captured = in
			return &service.CreateMerchantResult{
				MerchantID: in.MerchantID,
				Name:       in.Name,
				APIKey:     "ak", SecretKey: "sk",
				CallbackURL: in.CallbackURL,
				RateLimit:   in.RateLimit,
			}, nil
		})

	app := newMerchantApp(svc)
	body := `{"merchant_id":"M1","name":"Acme","callback_url":"https://x/cb","rate_limit":42}`
	req := httptest.NewRequest("POST", "/admin/merchants", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	if captured.MerchantID != "M1" || captured.Name != "Acme" ||
		captured.CallbackURL != "https://x/cb" || captured.RateLimit != 42 {
		t.Fatalf("captured: %+v", captured)
	}

	// Check the response carries the api/secret keys plaintext.
	var env struct {
		Data struct {
			APIKey    string `json:"api_key"`
			SecretKey string `json:"secret_key"`
		} `json:"data"`
	}
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.APIKey != "ak" || env.Data.SecretKey != "sk" {
		t.Fatalf("envelope: %+v", env)
	}
}

func TestMerchantHandler_RejectsBadJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockMerchants(ctrl)
	app := newMerchantApp(svc)
	req := httptest.NewRequest("POST", "/admin/merchants", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestMerchantHandler_PropagatesInvalidRequest(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockMerchants(ctrl)
	svc.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, service.ErrInvalidRequest)
	app := newMerchantApp(svc)
	req := httptest.NewRequest("POST", "/admin/merchants", strings.NewReader(`{"merchant_id":""}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestMerchantHandler_PropagatesGenericError(t *testing.T) {
	ctrl := gomock.NewController(t)
	svc := svcmock.NewMockMerchants(ctrl)
	svc.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, errors.New("db down"))
	app := newMerchantApp(svc)
	req := httptest.NewRequest("POST", "/admin/merchants", strings.NewReader(`{"merchant_id":"M1","name":"Acme"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 500 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

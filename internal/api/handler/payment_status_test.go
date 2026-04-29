package handler

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/api/middleware"
	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/internal/repository"
)

type fakeStatusRepo struct {
	byID map[string]*domain.Order
}

func (f *fakeStatusRepo) Create(context.Context, *domain.Order) error { return nil }
func (f *fakeStatusRepo) GetByOrderID(_ context.Context, id string) (*domain.Order, error) {
	if o, ok := f.byID[id]; ok {
		return o, nil
	}
	return nil, repository.ErrNotFound
}
func (f *fakeStatusRepo) GetByPaymentIntentID(context.Context, string) (*domain.Order, error) {
	return nil, repository.ErrNotFound
}
func (f *fakeStatusRepo) UpdateStatus(context.Context, string, domain.OrderStatus, string) error {
	return nil
}
func (f *fakeStatusRepo) UpdateCheckout(context.Context, string, string, string, string) error {
	return nil
}
func (f *fakeStatusRepo) BatchCreate(context.Context, []*domain.Order) error { return nil }
func (f *fakeStatusRepo) GetPendingBefore(context.Context, time.Time, int) ([]*domain.Order, error) {
	return nil, nil
}

func newStatusApp(repo repository.OrderRepository, merchantID string) *fiber.App {
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(middleware.LocalsMerchant, &domain.Merchant{MerchantID: merchantID, Status: domain.MerchantStatusActive})
		return c.Next()
	})
	h := NewPaymentStatusHandler(repo)
	app.Get("/api/payments/:id", h.Get)
	return app
}

func TestPaymentStatus_HappyPath(t *testing.T) {
	repo := &fakeStatusRepo{byID: map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", MerchantID: "M1", Amount: 1500, Currency: "USD", Status: domain.OrderStatusPaid},
	}}
	app := newStatusApp(repo, "M1")
	resp, err := app.Test(httptest.NewRequest("GET", "/api/payments/ORD-1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPaymentStatus_NotFound(t *testing.T) {
	app := newStatusApp(&fakeStatusRepo{}, "M1")
	resp, _ := app.Test(httptest.NewRequest("GET", "/api/payments/missing", nil))
	if resp.StatusCode != 404 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPaymentStatus_OtherMerchantHidden(t *testing.T) {
	repo := &fakeStatusRepo{byID: map[string]*domain.Order{
		"ORD-1": {OrderID: "ORD-1", MerchantID: "M2"},
	}}
	app := newStatusApp(repo, "M1")
	resp, _ := app.Test(httptest.NewRequest("GET", "/api/payments/ORD-1", nil))
	if resp.StatusCode != 404 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPaymentStatus_RejectsMissingMerchant(t *testing.T) {
	app := fiber.New()
	h := NewPaymentStatusHandler(&fakeStatusRepo{})
	app.Get("/api/payments/:id", h.Get)
	resp, _ := app.Test(httptest.NewRequest("GET", "/api/payments/x", nil))
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

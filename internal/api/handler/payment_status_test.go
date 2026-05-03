package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/mock/gomock"

	"github.com/quangdangfit/easypay/internal/api/middleware"
	"github.com/quangdangfit/easypay/internal/domain"
	repomock "github.com/quangdangfit/easypay/internal/mocks/repo"
	"github.com/quangdangfit/easypay/internal/repository"
)

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
	ctrl := gomock.NewController(t)
	repo := repomock.NewMockOrderRepository(ctrl)
	repo.EXPECT().GetByMerchantOrderID(gomock.Any(), gomock.Any(), "M1", "ord-1").
		Return(&domain.Order{OrderID: "ord-1", MerchantID: "M1", Amount: 1500, Currency: "USD", Status: domain.OrderStatusPaid}, nil)

	app := newStatusApp(repo, "M1")
	resp, err := app.Test(httptest.NewRequest("GET", "/api/payments/ord-1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPaymentStatus_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := repomock.NewMockOrderRepository(ctrl)
	repo.EXPECT().GetByMerchantOrderID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, repository.ErrNotFound)

	app := newStatusApp(repo, "M1")
	resp, _ := app.Test(httptest.NewRequest("GET", "/api/payments/missing", nil))
	if resp.StatusCode != 404 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// TestPaymentStatus_BadOrderIDIsNotFound: invalid order_id char set returns
// 404 (we don't leak structural validation back to the merchant).
func TestPaymentStatus_BadOrderIDIsNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := repomock.NewMockOrderRepository(ctrl)
	repo.EXPECT().GetByMerchantOrderID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, domain.ErrInvalidOrderID).AnyTimes()

	app := newStatusApp(repo, "M1")
	resp, _ := app.Test(httptest.NewRequest("GET", "/api/payments/has%20space", nil))
	if resp.StatusCode != 404 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPaymentStatus_RejectsMissingMerchant(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := repomock.NewMockOrderRepository(ctrl)
	// Handler bails out at merchant check before any repo call.

	app := fiber.New()
	h := NewPaymentStatusHandler(repo)
	app.Get("/api/payments/:id", h.Get)
	resp, _ := app.Test(httptest.NewRequest("GET", "/api/payments/x", nil))
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

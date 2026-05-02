package handler

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/mock/gomock"

	svcmock "github.com/quangdangfit/easypay/internal/mocks/service"
	"github.com/quangdangfit/easypay/internal/service"
	"github.com/quangdangfit/easypay/pkg/checkouttoken"
)

// payRoute is the new URL pattern: /pay/:merchant_id/:order_id.
const payRoute = "/pay/:merchant_id/:order_id"

func TestCheckout_HappyPathRedirect(t *testing.T) {
	ctrl := gomock.NewController(t)
	co := svcmock.NewMockCheckouts(ctrl)
	co.EXPECT().Resolve(gomock.Any(), "M1", "ord-1").Return("https://checkout.stripe.com/x", nil)

	app := fiber.New()
	app.Get(payRoute, NewCheckoutHandler(co, "").Redirect)
	resp, err := app.Test(httptest.NewRequest("GET", "/pay/M1/ord-1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 302 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "https://checkout.stripe.com/x" {
		t.Fatalf("location: %s", got)
	}
}

func TestCheckout_NotReadyShowsHTML(t *testing.T) {
	ctrl := gomock.NewController(t)
	co := svcmock.NewMockCheckouts(ctrl)
	co.EXPECT().Resolve(gomock.Any(), gomock.Any(), gomock.Any()).Return("", service.ErrOrderNotReady)

	app := fiber.New()
	app.Get(payRoute, NewCheckoutHandler(co, "").Redirect)
	resp, err := app.Test(httptest.NewRequest("GET", "/pay/M1/ord-1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Fatalf("missing Retry-After")
	}
}

func TestCheckout_UnavailableShowsHTML(t *testing.T) {
	ctrl := gomock.NewController(t)
	co := svcmock.NewMockCheckouts(ctrl)
	co.EXPECT().Resolve(gomock.Any(), gomock.Any(), gomock.Any()).Return("", service.ErrUnavailable)

	app := fiber.New()
	app.Get(payRoute, NewCheckoutHandler(co, "").Redirect)
	resp, err := app.Test(httptest.NewRequest("GET", "/pay/M1/ord-1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestCheckout_UnexpectedErrorShowsUnavailable(t *testing.T) {
	ctrl := gomock.NewController(t)
	co := svcmock.NewMockCheckouts(ctrl)
	co.EXPECT().Resolve(gomock.Any(), gomock.Any(), gomock.Any()).Return("", errors.New("boom"))

	app := fiber.New()
	app.Get(payRoute, NewCheckoutHandler(co, "").Redirect)
	resp, err := app.Test(httptest.NewRequest("GET", "/pay/M1/ord-1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestCheckout_TokenRequiredWhenSecretSet(t *testing.T) {
	ctrl := gomock.NewController(t)
	co := svcmock.NewMockCheckouts(ctrl)
	// Token check fails before Resolve is reached.

	app := fiber.New()
	app.Get(payRoute, NewCheckoutHandler(co, "secret").Redirect)
	resp, err := app.Test(httptest.NewRequest("GET", "/pay/M1/ord-1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 410 {
		t.Fatalf("expected 410, got %d", resp.StatusCode)
	}
}

func TestCheckout_BadTokenRejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	co := svcmock.NewMockCheckouts(ctrl)

	app := fiber.New()
	app.Get(payRoute, NewCheckoutHandler(co, "secret").Redirect)
	resp, _ := app.Test(httptest.NewRequest("GET", "/pay/M1/ord-1?t=garbage", nil))
	if resp.StatusCode != 410 {
		t.Fatalf("expected 410, got %d", resp.StatusCode)
	}
}

func TestCheckout_GoodTokenAccepted(t *testing.T) {
	ctrl := gomock.NewController(t)
	co := svcmock.NewMockCheckouts(ctrl)
	co.EXPECT().Resolve(gomock.Any(), "M1", "ord-1").Return("https://x", nil)

	tok := checkouttoken.Sign("secret", "M1", "ord-1", time.Hour)
	app := fiber.New()
	app.Get(payRoute, NewCheckoutHandler(co, "secret").Redirect)
	resp, _ := app.Test(httptest.NewRequest("GET", "/pay/M1/ord-1?t="+tok, nil))
	if resp.StatusCode != 302 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestCheckout_SuccessAndCancelPages(t *testing.T) {
	ctrl := gomock.NewController(t)
	co := svcmock.NewMockCheckouts(ctrl)

	app := fiber.New()
	h := NewCheckoutHandler(co, "")
	app.Get("/checkout/success", h.Success)
	app.Get("/checkout/cancel", h.Cancel)

	for _, path := range []string{"/checkout/success?order_id=ord-1", "/checkout/cancel?order_id=ord-1"} {
		resp, err := app.Test(httptest.NewRequest("GET", path, nil))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("%s: status %d", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("%s: wrong content-type %q", path, ct)
		}
	}
}

package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/service"
	"github.com/quangdangfit/easypay/pkg/checkouttoken"
)

type fakeCheckouts struct {
	url string
	err error
}

func (f *fakeCheckouts) Resolve(ctx context.Context, orderID string) (string, error) {
	return f.url, f.err
}

func TestCheckout_HappyPathRedirect(t *testing.T) {
	app := fiber.New()
	h := NewCheckoutHandler(&fakeCheckouts{url: "https://checkout.stripe.com/x"}, "")
	app.Get("/pay/:id", h.Redirect)
	resp, err := app.Test(httptest.NewRequest("GET", "/pay/ORD-1", nil))
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
	app := fiber.New()
	h := NewCheckoutHandler(&fakeCheckouts{err: service.ErrOrderNotReady}, "")
	app.Get("/pay/:id", h.Redirect)
	resp, err := app.Test(httptest.NewRequest("GET", "/pay/ORD-1", nil))
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
	app := fiber.New()
	h := NewCheckoutHandler(&fakeCheckouts{err: service.ErrUnavailable}, "")
	app.Get("/pay/:id", h.Redirect)
	resp, err := app.Test(httptest.NewRequest("GET", "/pay/ORD-1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestCheckout_UnexpectedErrorShowsUnavailable(t *testing.T) {
	app := fiber.New()
	h := NewCheckoutHandler(&fakeCheckouts{err: errors.New("boom")}, "")
	app.Get("/pay/:id", h.Redirect)
	resp, err := app.Test(httptest.NewRequest("GET", "/pay/ORD-1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestCheckout_TokenRequiredWhenSecretSet(t *testing.T) {
	app := fiber.New()
	h := NewCheckoutHandler(&fakeCheckouts{url: "https://x"}, "secret")
	app.Get("/pay/:id", h.Redirect)
	resp, err := app.Test(httptest.NewRequest("GET", "/pay/ORD-1", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 410 {
		t.Fatalf("expected 410, got %d", resp.StatusCode)
	}
}

func TestCheckout_BadTokenRejected(t *testing.T) {
	app := fiber.New()
	h := NewCheckoutHandler(&fakeCheckouts{url: "https://x"}, "secret")
	app.Get("/pay/:id", h.Redirect)
	resp, _ := app.Test(httptest.NewRequest("GET", "/pay/ORD-1?t=garbage", nil))
	if resp.StatusCode != 410 {
		t.Fatalf("expected 410, got %d", resp.StatusCode)
	}
}

func TestCheckout_GoodTokenAccepted(t *testing.T) {
	tok := checkouttoken.Sign("secret", "ORD-1", time.Hour)
	app := fiber.New()
	h := NewCheckoutHandler(&fakeCheckouts{url: "https://x"}, "secret")
	app.Get("/pay/:id", h.Redirect)
	resp, _ := app.Test(httptest.NewRequest("GET", "/pay/ORD-1?t="+tok, nil))
	if resp.StatusCode != 302 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestCheckout_SuccessAndCancelPages(t *testing.T) {
	app := fiber.New()
	h := NewCheckoutHandler(&fakeCheckouts{}, "")
	app.Get("/checkout/success", h.Success)
	app.Get("/checkout/cancel", h.Cancel)

	for _, path := range []string{"/checkout/success?order_id=ORD-1", "/checkout/cancel?order_id=ORD-1"} {
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

package middleware

import (
	"context"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/quangdangfit/easypay/internal/domain"
	"github.com/quangdangfit/easypay/pkg/hmac"
)

// --- RequestID ---

func TestRequestID_GeneratesWhenMissing(t *testing.T) {
	app := fiber.New()
	var seen string
	app.Use(RequestID())
	app.Get("/", func(c *fiber.Ctx) error {
		// Middleware stores the id in Locals + the response header.
		seen, _ = c.Locals("request_id").(string)
		return c.SendStatus(200)
	})
	resp, err := app.Test(httptest.NewRequest("GET", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if seen == "" {
		t.Fatal("expected generated request id in Locals")
	}
	if got := resp.Header.Get(HeaderRequestID); got != seen {
		t.Fatalf("response header mismatch: %q vs %q", got, seen)
	}
}

func TestRequestID_PreservesIncoming(t *testing.T) {
	app := fiber.New()
	app.Use(RequestID())
	app.Get("/", func(c *fiber.Ctx) error { return c.SendStatus(200) })
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(HeaderRequestID, "incoming-42")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get(HeaderRequestID); got != "incoming-42" {
		t.Fatalf("got %q want incoming-42", got)
	}
}

// --- HMAC Auth ---

type fakeMerchants struct {
	m map[string]*domain.Merchant
}

func (f *fakeMerchants) GetByAPIKey(ctx context.Context, apiKey string) (*domain.Merchant, error) {
	if v, ok := f.m[apiKey]; ok {
		return v, nil
	}
	return nil, errAuth
}

var errAuth = &fauxErr{}

type fauxErr struct{}

func (e *fauxErr) Error() string { return "not found" }

func newAuthApp(merchants *fakeMerchants) *fiber.App {
	app := fiber.New()
	app.Use(HMACAuth(merchants, 5*time.Minute))
	app.Post("/", func(c *fiber.Ctx) error {
		m := c.Locals(LocalsMerchant).(*domain.Merchant)
		return c.SendString(m.MerchantID)
	})
	return app
}

func sign(secret, ts, body string) string {
	return hmac.Sign(secret, []byte(ts+"."+body))
}

func TestHMAC_HappyPath(t *testing.T) {
	merchant := &domain.Merchant{
		MerchantID: "M1", APIKey: "k", SecretKey: "s",
		Status: domain.MerchantStatusActive,
	}
	app := newAuthApp(&fakeMerchants{m: map[string]*domain.Merchant{"k": merchant}})
	body := `{"a":1}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set(HeaderAPIKey, "k")
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderSignature, sign("s", ts, body))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
}

func TestHMAC_RejectsMissingHeaders(t *testing.T) {
	app := newAuthApp(&fakeMerchants{})
	resp, _ := app.Test(httptest.NewRequest("POST", "/", strings.NewReader("{}")))
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHMAC_RejectsBadTimestamp(t *testing.T) {
	merchant := &domain.Merchant{MerchantID: "M1", APIKey: "k", SecretKey: "s", Status: domain.MerchantStatusActive}
	app := newAuthApp(&fakeMerchants{m: map[string]*domain.Merchant{"k": merchant}})

	body := "{}"
	old := strconv.FormatInt(time.Now().Add(-1*time.Hour).Unix(), 10)
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set(HeaderAPIKey, "k")
	req.Header.Set(HeaderTimestamp, old)
	req.Header.Set(HeaderSignature, sign("s", old, body))
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHMAC_RejectsBadSignature(t *testing.T) {
	merchant := &domain.Merchant{MerchantID: "M1", APIKey: "k", SecretKey: "s", Status: domain.MerchantStatusActive}
	app := newAuthApp(&fakeMerchants{m: map[string]*domain.Merchant{"k": merchant}})
	body := "{}"
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set(HeaderAPIKey, "k")
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderSignature, "deadbeef")
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHMAC_RejectsSuspended(t *testing.T) {
	merchant := &domain.Merchant{MerchantID: "M1", APIKey: "k", SecretKey: "s", Status: domain.MerchantStatusSuspended}
	app := newAuthApp(&fakeMerchants{m: map[string]*domain.Merchant{"k": merchant}})
	body := "{}"
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set(HeaderAPIKey, "k")
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderSignature, sign("s", ts, body))
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

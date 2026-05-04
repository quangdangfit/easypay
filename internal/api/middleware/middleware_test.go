package middleware

import (
	"errors"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/mock/gomock"

	"github.com/quangdangfit/easypay/internal/domain"
	repomock "github.com/quangdangfit/easypay/internal/mocks/repo"
	"github.com/quangdangfit/easypay/pkg/hmac"
)

// errAuth is a sentinel returned by GetByAPIKey when the merchant doesn't exist.
// Shared with ratelimit_test.go.
var errAuth = errors.New("merchant not found")

// --- RequestID ---

func TestRequestID_GeneratesWhenMissing(t *testing.T) {
	app := fiber.New()
	var seen string
	app.Use(RequestID())
	app.Get("/", func(c *fiber.Ctx) error {
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

// authApp builds a Fiber app with HMACAuth wired to a mock MerchantRepository
// that returns merchant for "k" and errAuth otherwise.
func authApp(t *testing.T, merchant *domain.Merchant) *fiber.App {
	t.Helper()
	ctrl := gomock.NewController(t)
	repo := repomock.NewMockMerchantRepository(ctrl)
	if merchant != nil {
		repo.EXPECT().GetByAPIKey(gomock.Any(), "k").Return(merchant, nil).AnyTimes()
	}
	repo.EXPECT().GetByAPIKey(gomock.Any(), gomock.Not("k")).Return(nil, errAuth).AnyTimes()

	app := fiber.New()
	app.Use(HMACAuth(repo, 5*time.Minute))
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
	app := authApp(t, merchant)
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
	app := authApp(t, nil)
	resp, _ := app.Test(httptest.NewRequest("POST", "/", strings.NewReader("{}")))
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHMAC_RejectsBadTimestamp(t *testing.T) {
	merchant := &domain.Merchant{MerchantID: "M1", APIKey: "k", SecretKey: "s", Status: domain.MerchantStatusActive}
	app := authApp(t, merchant)

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
	app := authApp(t, merchant)
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
	app := authApp(t, merchant)
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

func TestHMAC_RejectsFutureTimestamp(t *testing.T) {
	merchant := &domain.Merchant{MerchantID: "M1", APIKey: "k", SecretKey: "s", Status: domain.MerchantStatusActive}
	app := authApp(t, merchant)
	body := "{}"
	future := strconv.FormatInt(time.Now().Add(1*time.Hour).Unix(), 10)
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set(HeaderAPIKey, "k")
	req.Header.Set(HeaderTimestamp, future)
	req.Header.Set(HeaderSignature, sign("s", future, body))
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHMAC_RejectsMissingAPIKey(t *testing.T) {
	app := authApp(t, nil)
	body := "{}"
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderSignature, "sig")
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHMAC_RejectsMissingTimestamp(t *testing.T) {
	app := authApp(t, nil)
	req := httptest.NewRequest("POST", "/", strings.NewReader("{}"))
	req.Header.Set(HeaderAPIKey, "k")
	req.Header.Set(HeaderSignature, "sig")
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHMAC_RejectsMissingSignature(t *testing.T) {
	app := authApp(t, nil)
	req := httptest.NewRequest("POST", "/", strings.NewReader("{}"))
	req.Header.Set(HeaderAPIKey, "k")
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(time.Now().Unix(), 10))
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestHMAC_RejectsUnknownAPIKey(t *testing.T) {
	app := authApp(t, nil)
	body := "{}"
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set(HeaderAPIKey, "unknown-key")
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderSignature, sign("s", ts, body))
	resp, _ := app.Test(req)
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

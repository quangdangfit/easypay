package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func newAdminApp(expected string) *fiber.App {
	app := fiber.New()
	app.Use(AdminAuth(expected))
	app.Get("/admin/ping", func(c *fiber.Ctx) error { return c.SendStatus(200) })
	return app
}

func TestAdminAuth_HappyPath(t *testing.T) {
	app := newAdminApp("super-secret")
	req := httptest.NewRequest("GET", "/admin/ping", nil)
	req.Header.Set(HeaderAdminKey, "super-secret")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestAdminAuth_RejectsMissingKey(t *testing.T) {
	app := newAdminApp("super-secret")
	resp, err := app.Test(httptest.NewRequest("GET", "/admin/ping", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestAdminAuth_RejectsBadKey(t *testing.T) {
	app := newAdminApp("super-secret")
	req := httptest.NewRequest("GET", "/admin/ping", nil)
	req.Header.Set(HeaderAdminKey, "wrong")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// AdminAuth with empty expected refuses every request — admin endpoints
// should not be mounted at all in that mode, but if the middleware does
// run it must fail closed.
func TestAdminAuth_EmptyExpectedFailsClosed(t *testing.T) {
	app := newAdminApp("")
	req := httptest.NewRequest("GET", "/admin/ping", nil)
	req.Header.Set(HeaderAdminKey, "anything")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

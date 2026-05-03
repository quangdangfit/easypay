package response

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func runHandler(t *testing.T, h fiber.Handler) *httptest.ResponseRecorder {
	t.Helper()
	app := fiber.New()
	app.Get("/", h)
	req := httptest.NewRequest("GET", "/", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	rec := httptest.NewRecorder()
	rec.Code = resp.StatusCode
	body, _ := io.ReadAll(resp.Body)
	rec.Body.Write(body)
	return rec
}

func TestOK(t *testing.T) {
	rec := runHandler(t, func(c *fiber.Ctx) error { return OK(c, map[string]string{"k": "v"}) })
	if rec.Code != 200 {
		t.Fatalf("status: %d", rec.Code)
	}
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data == nil || env.Error != nil {
		t.Fatalf("envelope: %+v", env)
	}
}

func TestAcceptedAndCreated(t *testing.T) {
	cases := []struct {
		name string
		fn   fiber.Handler
		code int
	}{
		{"accepted", func(c *fiber.Ctx) error { return Accepted(c, struct{}{}) }, 202},
		{"created", func(c *fiber.Ctx) error { return Created(c, struct{}{}) }, 201},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := runHandler(t, tc.fn)
			if rec.Code != tc.code {
				t.Fatalf("status: %d want %d", rec.Code, tc.code)
			}
		})
	}
}

func TestErrorHelpers(t *testing.T) {
	cases := []struct {
		name string
		fn   fiber.Handler
		code int
		err  string
	}{
		{"bad_request", func(c *fiber.Ctx) error { return BadRequest(c, "bad", "x") }, 400, "bad"},
		{"unauthorized", func(c *fiber.Ctx) error { return Unauthorized(c, "auth", "x") }, 401, "auth"},
		{"conflict", func(c *fiber.Ctx) error { return Conflict(c, "dup", "x") }, 409, "dup"},
		{"too_many", func(c *fiber.Ctx) error { return TooManyRequests(c, "rl", "x") }, 429, "rl"},
		{"internal", func(c *fiber.Ctx) error { return InternalError(c, "boom", "x") }, 500, "boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := runHandler(t, tc.fn)
			if rec.Code != tc.code {
				t.Fatalf("status: %d want %d", rec.Code, tc.code)
			}
			var env Envelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatal(err)
			}
			if env.Error == nil || env.Error.Code != tc.err {
				t.Fatalf("error envelope: %+v", env.Error)
			}
		})
	}
}

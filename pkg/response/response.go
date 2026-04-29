package response

import "github.com/gofiber/fiber/v2"

type Envelope struct {
	Data  any    `json:"data,omitempty"`
	Error *Error `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func OK(c *fiber.Ctx, data any) error {
	return c.Status(fiber.StatusOK).JSON(Envelope{Data: data})
}

func Accepted(c *fiber.Ctx, data any) error {
	return c.Status(fiber.StatusAccepted).JSON(Envelope{Data: data})
}

func Created(c *fiber.Ctx, data any) error {
	return c.Status(fiber.StatusCreated).JSON(Envelope{Data: data})
}

func Fail(c *fiber.Ctx, status int, code, message string) error {
	return c.Status(status).JSON(Envelope{Error: &Error{Code: code, Message: message}})
}

func BadRequest(c *fiber.Ctx, code, message string) error {
	return Fail(c, fiber.StatusBadRequest, code, message)
}

func Unauthorized(c *fiber.Ctx, code, message string) error {
	return Fail(c, fiber.StatusUnauthorized, code, message)
}

func TooManyRequests(c *fiber.Ctx, code, message string) error {
	return Fail(c, fiber.StatusTooManyRequests, code, message)
}

func InternalError(c *fiber.Ctx, code, message string) error {
	return Fail(c, fiber.StatusInternalServerError, code, message)
}

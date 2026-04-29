package handler

import (
	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

// PrometheusHandler exposes /metrics. We bridge net/http via fasthttpadaptor
// because Fiber sits on fasthttp.
func PrometheusHandler() fiber.Handler {
	h := fasthttpadaptor.NewFastHTTPHandler(promhttp.Handler())
	return func(c *fiber.Ctx) error {
		h(c.Context())
		return nil
	}
}

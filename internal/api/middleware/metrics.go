package middleware

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests by route, method, status.",
	}, []string{"route", "method", "status"})

	HTTPRequestDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency by route, method.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route", "method"})

	StripeAPIErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "stripe_api_errors_total",
		Help: "Stripe SDK errors by category.",
	}, []string{"op", "category"})

	KafkaConsumerLag = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kafka_consumer_lag",
		Help: "Estimated kafka consumer lag in messages.",
	}, []string{"topic"})

	StuckOrdersTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "stuck_orders_total",
		Help: "Pending orders older than the safety threshold.",
	})

	OnChainConfirmationLag = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "blockchain_confirmation_lag_blocks",
		Help: "Last observed gap between chain head and oldest pending tx block.",
	})
)

// PrometheusMiddleware records request count + latency.
func PrometheusMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		route := c.Route().Path
		if route == "" {
			route = c.Path()
		}
		status := strconv.Itoa(c.Response().StatusCode())
		HTTPRequestsTotal.WithLabelValues(route, c.Method(), status).Inc()
		HTTPRequestDurationSeconds.WithLabelValues(route, c.Method()).Observe(time.Since(start).Seconds())
		return err
	}
}

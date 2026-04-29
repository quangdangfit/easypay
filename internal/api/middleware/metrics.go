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

	// Lazy checkout — per-stage latency lets us see whether resolves are
	// served from the in-process URL cache (sub-ms), Redis snapshot (ms),
	// MySQL (ms), or Stripe (50-300ms).
	CheckoutResolveDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "checkout_resolve_duration_seconds",
		Help:    "Time spent resolving a hosted-checkout URL, by stage.",
		Buckets: []float64{0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	}, []string{"stage"})

	// "result" classifies how a /pay/:id resolution completed.
	CheckoutResolveResult = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "checkout_resolve_result_total",
		Help: "Outcome of /pay/:id resolutions.",
	}, []string{"result"}) // cached_local | cached_db | created | rate_limited | breaker_open | not_ready | failed

	StripeRateLimited = promauto.NewCounter(prometheus.CounterOpts{
		Name: "stripe_rate_limited_total",
		Help: "Times the local token bucket prevented a Stripe call.",
	})

	StripeBreakerState = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "stripe_breaker_state",
		Help: "Stripe circuit-breaker state: 0=closed, 1=half-open, 2=open.",
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

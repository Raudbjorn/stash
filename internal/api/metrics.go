package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Prometheus metrics for the HTTP server. These are opt-in: the /metrics
// endpoint and this middleware are only wired up when metrics_enabled is set.
// The default Go/process collectors are exposed automatically by the default
// registry that promhttp.Handler() serves.
var (
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stash_http_requests_total",
			Help: "Total number of HTTP requests, labeled by method, matched route and status code.",
		},
		[]string{"method", "route", "code"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "stash_http_request_duration_seconds",
			Help:    "Duration of HTTP requests in seconds, labeled by method and matched route.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "route"},
	)
)

// metricsHandler serves the Prometheus text exposition of all registered metrics.
func metricsHandler() http.Handler {
	return promhttp.Handler()
}

// MetricsMiddleware records request counts and latencies for Prometheus. It
// labels by the matched chi route pattern (not the raw path) to keep label
// cardinality bounded — otherwise every scene/image URL would create a series.
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}

		httpRequestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(ww.Status())).Inc()
		httpRequestDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus metrics for the HTTP server
//
// These metrics track:
// - HTTP request duration
// - Number of requests by endpoint/method/status

var (
	// httpRequestDuration measures HTTP request duration.
	// Labels:
	//   - handler: handler name (stats, topics, facts, etc.)
	//   - method: HTTP method (GET, POST)
	//   - status: HTTP status code (200, 404, 500)
	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "Duration of HTTP requests in seconds",
			// Buckets for typical web UI times: 10ms - 10s
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"handler", "method", "status"},
	)

	// httpRequestsTotal counts HTTP requests.
	// Labels:
	//   - handler: handler name
	//   - method: HTTP method
	//   - status: HTTP status code
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Total number of HTTP requests",
		},
		[]string{"handler", "method", "status"},
	)
)

// responseWriter wraps http.ResponseWriter to capture status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

// newResponseWriter creates a new responseWriter.
func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK, // default
	}
}

// WriteHeader captures the status code.
func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher for SSE support.
func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// metricsMiddleware records metrics for HTTP requests.
// handlerName is used as a label to identify the endpoint.
func metricsMiddleware(handlerName string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		rw := newResponseWriter(w)
		next.ServeHTTP(rw, r)

		duration := time.Since(start).Seconds()
		status := strconv.Itoa(rw.statusCode)

		httpRequestDuration.WithLabelValues(handlerName, r.Method, status).Observe(duration)
		httpRequestsTotal.WithLabelValues(handlerName, r.Method, status).Inc()
	}
}

// instrumentHandler wraps a handler with metrics.
func instrumentHandler(name string, handler http.HandlerFunc) http.HandlerFunc {
	return metricsMiddleware(name, handler)
}

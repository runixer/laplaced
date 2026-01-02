package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus метрики для HTTP сервера
//
// Метрики позволяют отслеживать:
// - Время выполнения HTTP запросов
// - Количество запросов по endpoint/method/status

var (
	// httpRequestDuration измеряет время выполнения HTTP запросов.
	// Labels:
	//   - handler: название handler'а (stats, topics, facts, etc.)
	//   - method: HTTP метод (GET, POST)
	//   - status: HTTP status code (200, 404, 500)
	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "Duration of HTTP requests in seconds",
			// Buckets для типичных времён web UI: 10ms - 10s
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"handler", "method", "status"},
	)

	// httpRequestsTotal считает количество HTTP запросов.
	// Labels:
	//   - handler: название handler'а
	//   - method: HTTP метод
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

// metricsMiddleware записывает метрики для HTTP запросов.
// handlerName используется как label для идентификации endpoint'а.
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

// instrumentHandler оборачивает handler с метриками.
func instrumentHandler(name string, handler http.HandlerFunc) http.HandlerFunc {
	return metricsMiddleware(name, handler)
}

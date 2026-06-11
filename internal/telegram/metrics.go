package telegram

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus metrics for the Telegram HTTP client
//
// These metrics track:
// - Telegram API request duration (histogram)
// - Number of errors by type (counter)
// - Number of retry attempts (counter)
// - Current long polling state (gauge)

const metricsNamespace = "laplaced"

var (
	// telegramRequestDuration measures Telegram API request duration.
	// Labels:
	//   - method: API method name (sendMessage, sendChatAction, getUpdates, etc.)
	//   - status: request outcome (success, error, timeout, retry)
	telegramRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "telegram",
			Name:      "request_duration_seconds",
			Help:      "Duration of Telegram API requests in seconds",
			// Buckets are optimized for typical Telegram API response times:
			// - Short requests (sendMessage): 0.1-1s
			// - Long polling (getUpdates): 25-35s
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 30, 35},
		},
		[]string{"method", "status"},
	)

	// telegramRequestsTotal counts the total number of Telegram API requests.
	// Labels:
	//   - method: API method name
	//   - status: outcome (success, error, timeout)
	telegramRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "telegram",
			Name:      "requests_total",
			Help:      "Total number of Telegram API requests",
		},
		[]string{"method", "status"},
	)

	// telegramRetriesTotal counts retry attempts.
	// Labels:
	//   - method: API method name
	telegramRetriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "telegram",
			Name:      "retries_total",
			Help:      "Total number of retry attempts for Telegram API requests",
		},
		[]string{"method"},
	)

	// telegramErrorsTotal counts errors by type.
	// Labels:
	//   - method: API method name
	//   - error_type: error type (network, timeout, api_error, decode_error)
	telegramErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "telegram",
			Name:      "errors_total",
			Help:      "Total number of errors by type",
		},
		[]string{"method", "error_type"},
	)

	// telegramLongPollingActive shows whether long polling is active.
	// Value 1 = active, 0 = inactive
	telegramLongPollingActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: "telegram",
			Name:      "long_polling_active",
			Help:      "Whether long polling is currently active (1 = active, 0 = inactive)",
		},
	)

	// telegramLongPollingUpdates counts the number of updates received.
	telegramLongPollingUpdates = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "telegram",
			Name:      "long_polling_updates_total",
			Help:      "Total number of updates received via long polling",
		},
	)
)

// Metric status constants
const (
	statusSuccess = "success"
	statusError   = "error"
	statusTimeout = "timeout"
	statusRetry   = "retry"
)

// Error type constants
const (
	errorTypeNetwork = "network"
	errorTypeTimeout = "timeout"
	errorTypeAPI     = "api_error"
	errorTypeDecode  = "decode_error"
)

// recordRequestDuration records request duration
func recordRequestDuration(method, status string, durationSeconds float64) {
	telegramRequestDuration.WithLabelValues(method, status).Observe(durationSeconds)
	telegramRequestsTotal.WithLabelValues(method, status).Inc()
}

// recordRetry records a retry attempt
func recordRetry(method string) {
	telegramRetriesTotal.WithLabelValues(method).Inc()
}

// recordError records an error of the given type
func recordError(method, errorType string) {
	telegramErrorsTotal.WithLabelValues(method, errorType).Inc()
}

// setLongPollingActive sets the long polling status
func setLongPollingActive(active bool) {
	if active {
		telegramLongPollingActive.Set(1)
	} else {
		telegramLongPollingActive.Set(0)
	}
}

// recordLongPollingUpdates records the number of updates received
func recordLongPollingUpdates(count int) {
	telegramLongPollingUpdates.Add(float64(count))
}

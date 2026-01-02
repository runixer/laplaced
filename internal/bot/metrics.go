package bot

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus метрики для Bot
//
// Метрики позволяют отслеживать:
// - Количество активных сессий
// - Время обработки сообщений (end-to-end)
// - Количество обработанных сообщений

const metricsNamespace = "laplaced"

var (
	// activeSessions показывает текущее количество активных сессий.
	activeSessions = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "active_sessions",
			Help:      "Current number of active chat sessions",
		},
	)

	// messageProcessingDuration измеряет end-to-end время обработки сообщения.
	// От получения сообщения до отправки ответа.
	messageProcessingDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "message_processing_duration_seconds",
			Help:      "End-to-end duration of message processing in seconds",
			// Buckets для типичных времён обработки: 0.5s - 30s
			Buckets: []float64{0.5, 1, 2, 3, 5, 7, 10, 15, 20, 30},
		},
	)

	// messagesProcessedTotal считает количество обработанных сообщений.
	// Labels:
	//   - status: результат (success, error)
	messagesProcessedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "messages_processed_total",
			Help:      "Total number of processed messages",
		},
		[]string{"status"},
	)

	// contextTokens отслеживает размер контекста в токенах.
	// Помогает понять, насколько большие контексты мы отправляем в LLM.
	contextTokens = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "context_tokens",
			Help:      "Approximate number of tokens in context sent to LLM",
			// Buckets для размеров контекста: 1K - 100K tokens
			Buckets: []float64{1000, 2000, 5000, 10000, 20000, 30000, 50000, 75000, 100000},
		},
	)
)

const (
	statusSuccess = "success"
	statusError   = "error"
)

// SetActiveSessions устанавливает текущее количество активных сессий.
func SetActiveSessions(count int) {
	activeSessions.Set(float64(count))
}

// IncActiveSessions увеличивает счётчик активных сессий на 1.
func IncActiveSessions() {
	activeSessions.Inc()
}

// DecActiveSessions уменьшает счётчик активных сессий на 1.
func DecActiveSessions() {
	activeSessions.Dec()
}

// RecordMessageProcessing записывает метрики обработки сообщения.
func RecordMessageProcessing(durationSeconds float64, success bool) {
	messageProcessingDuration.Observe(durationSeconds)

	status := statusSuccess
	if !success {
		status = statusError
	}
	messagesProcessedTotal.WithLabelValues(status).Inc()
}

// RecordContextTokens записывает размер контекста в токенах.
func RecordContextTokens(tokens int) {
	contextTokens.Observe(float64(tokens))
}

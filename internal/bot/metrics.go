package bot

import (
	"strconv"

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
	// Labels:
	//   - user_id: идентификатор пользователя
	messageProcessingDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "message_processing_duration_seconds",
			Help:      "End-to-end duration of message processing in seconds",
			// Buckets для типичных времён обработки: 0.5s - 120s
			// (LLM генерация может занимать до минуты)
			Buckets: []float64{0.5, 1, 2, 3, 5, 7, 10, 15, 20, 30, 45, 60, 90, 120},
		},
		[]string{"user_id"},
	)

	// messagesProcessedTotal считает количество обработанных сообщений.
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - status: результат (success, error)
	messagesProcessedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "messages_processed_total",
			Help:      "Total number of processed messages",
		},
		[]string{"user_id", "status"},
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

	// contextTokensBySource отслеживает токены по источнику контекста.
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - source: profile, topics, session
	// Помогает понять распределение контекста между источниками.
	contextTokensBySource = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "context_tokens_by_source",
			Help:      "Approximate number of tokens in context by source",
			// Buckets для каждого источника: 100 - 50K tokens
			Buckets: []float64{100, 500, 1000, 2000, 5000, 10000, 20000, 30000, 50000},
		},
		[]string{"user_id", "source"},
	)

	// ============================================================
	// Per-message breakdown metrics (for complete latency analysis)
	// ============================================================

	// messageLLMDuration tracks total LLM time per message (sum of all LLM calls in Tool Loop).
	// Labels:
	//   - user_id: user identifier
	messageLLMDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "message_llm_duration_seconds",
			Help:      "Total LLM duration per message (sum of all calls in Tool Loop)",
			Buckets:   []float64{1, 2, 5, 10, 15, 20, 30, 45, 60, 90, 120},
		},
		[]string{"user_id"},
	)

	// messageLLMCalls tracks number of LLM calls per message (Tool Loop iterations).
	// Labels:
	//   - user_id: user identifier
	messageLLMCalls = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "message_llm_calls",
			Help:      "Number of LLM calls per message (Tool Loop iterations)",
			Buckets:   []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		},
		[]string{"user_id"},
	)

	// messageToolDuration tracks total tool execution time per message.
	// Labels:
	//   - user_id: user identifier
	messageToolDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "message_tool_duration_seconds",
			Help:      "Total tool execution duration per message",
			Buckets:   []float64{0.1, 0.5, 1, 2, 3, 5, 7, 10, 15, 20, 30},
		},
		[]string{"user_id"},
	)

	// messageToolCalls tracks number of tool calls per message.
	// Labels:
	//   - user_id: user identifier
	messageToolCalls = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "message_tool_calls",
			Help:      "Number of tool calls per message",
			Buckets:   []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		},
		[]string{"user_id"},
	)

	// messageTelegramDuration tracks total Telegram API time per message (sending responses).
	// Labels:
	//   - user_id: user identifier
	messageTelegramDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "message_telegram_duration_seconds",
			Help:      "Total Telegram API duration per message (sending responses)",
			Buckets:   []float64{0.05, 0.1, 0.2, 0.5, 1, 2, 3, 5, 10},
		},
		[]string{"user_id"},
	)

	// messageTelegramCalls tracks number of Telegram API calls per message.
	// Labels:
	//   - user_id: user identifier
	messageTelegramCalls = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "message_telegram_calls",
			Help:      "Number of Telegram send calls per message",
			Buckets:   []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		},
		[]string{"user_id"},
	)

	// messageReactionDuration tracks SetMessageReaction time (will include Flash LLM call).
	// Labels:
	//   - user_id: user identifier
	messageReactionDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "message_reaction_duration_seconds",
			Help:      "Duration of SetMessageReaction call (includes future Flash LLM)",
			Buckets:   []float64{0.05, 0.1, 0.2, 0.5, 1, 2, 3, 5},
		},
		[]string{"user_id"},
	)

	// llmAnomaliesTotal tracks LLM response anomalies.
	// Labels:
	//   - user_id: user identifier
	//   - type: anomaly type (empty_response, sanitized, retry_success, retry_failed)
	llmAnomaliesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "llm_anomalies_total",
			Help:      "Total number of LLM response anomalies (empty responses, hallucinations)",
		},
		[]string{"user_id", "type"},
	)
)

const (
	statusSuccess = "success"
	statusError   = "error"
)

// LLM anomaly types
const (
	AnomalyEmptyResponse = "empty_response"
	AnomalySanitized     = "sanitized"
	AnomalyRetrySuccess  = "retry_success"
	AnomalyRetryFailed   = "retry_failed"
)

// Context source types
const (
	ContextSourceProfile = "profile"
	ContextSourceTopics  = "topics"
	ContextSourceSession = "session"
)

// formatUserID converts user ID to string for metric labels.
func formatUserID(userID int64) string {
	return strconv.FormatInt(userID, 10)
}

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
func RecordMessageProcessing(userID int64, durationSeconds float64, success bool) {
	uid := formatUserID(userID)
	messageProcessingDuration.WithLabelValues(uid).Observe(durationSeconds)

	status := statusSuccess
	if !success {
		status = statusError
	}
	messagesProcessedTotal.WithLabelValues(uid, status).Inc()
}

// RecordContextTokens записывает размер контекста в токенах.
func RecordContextTokens(tokens int) {
	contextTokens.Observe(float64(tokens))
}

// RecordContextTokensBySource записывает токены по источнику контекста.
func RecordContextTokensBySource(userID int64, source string, tokens int) {
	uid := formatUserID(userID)
	contextTokensBySource.WithLabelValues(uid, source).Observe(float64(tokens))
}

// ============================================================
// Per-message breakdown recording functions
// ============================================================

// RecordMessageLLM records total LLM duration and call count for a message.
func RecordMessageLLM(userID int64, totalDuration float64, callCount int) {
	uid := formatUserID(userID)
	messageLLMDuration.WithLabelValues(uid).Observe(totalDuration)
	messageLLMCalls.WithLabelValues(uid).Observe(float64(callCount))
}

// RecordMessageTools records total tool execution duration and call count for a message.
func RecordMessageTools(userID int64, totalDuration float64, callCount int) {
	uid := formatUserID(userID)
	messageToolDuration.WithLabelValues(uid).Observe(totalDuration)
	messageToolCalls.WithLabelValues(uid).Observe(float64(callCount))
}

// RecordMessageTelegram records total Telegram send duration and call count for a message.
func RecordMessageTelegram(userID int64, totalDuration float64, callCount int) {
	uid := formatUserID(userID)
	messageTelegramDuration.WithLabelValues(uid).Observe(totalDuration)
	messageTelegramCalls.WithLabelValues(uid).Observe(float64(callCount))
}

// RecordMessageReaction records SetMessageReaction duration.
func RecordMessageReaction(userID int64, duration float64) {
	uid := formatUserID(userID)
	messageReactionDuration.WithLabelValues(uid).Observe(duration)
}

// RecordLLMAnomaly records an LLM response anomaly.
// anomalyType should be one of: AnomalyEmptyResponse, AnomalySanitized, AnomalyRetrySuccess, AnomalyRetryFailed
func RecordLLMAnomaly(userID int64, anomalyType string) {
	uid := formatUserID(userID)
	llmAnomaliesTotal.WithLabelValues(uid, anomalyType).Inc()
}

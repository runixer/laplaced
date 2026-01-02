package openrouter

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus метрики для OpenRouter LLM клиента
//
// Метрики позволяют отслеживать:
// - Время выполнения LLM запросов
// - Использование токенов (prompt/completion)
// - Стоимость LLM запросов

const metricsNamespace = "laplaced"

var (
	// llmRequestDuration измеряет время выполнения LLM запросов.
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - model: название модели
	//   - status: результат (success, error)
	llmRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "llm",
			Name:      "request_duration_seconds",
			Help:      "Duration of LLM API requests in seconds",
			// Buckets для типичных времён LLM: 0.5s - 60s
			Buckets: []float64{0.5, 1, 2, 3, 5, 7, 10, 15, 20, 30, 45, 60},
		},
		[]string{"user_id", "model", "status"},
	)

	// llmRequestsTotal считает количество LLM запросов.
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - model: название модели
	//   - status: результат (success, error)
	llmRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "llm",
			Name:      "requests_total",
			Help:      "Total number of LLM API requests",
		},
		[]string{"user_id", "model", "status"},
	)

	// llmTokensTotal считает использованные токены.
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - model: название модели
	//   - type: тип токенов (prompt, completion)
	llmTokensTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "llm",
			Name:      "tokens_total",
			Help:      "Total number of tokens used for LLM requests",
		},
		[]string{"user_id", "model", "type"},
	)

	// llmCostTotal отслеживает кумулятивную стоимость LLM запросов (USD).
	// Labels:
	//   - user_id: идентификатор пользователя
	//   - model: название модели
	llmCostTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "llm",
			Name:      "cost_usd_total",
			Help:      "Total cost of LLM API requests in USD",
		},
		[]string{"user_id", "model"},
	)

	// llmRetriesTotal считает количество retry-попыток.
	// Labels:
	//   - model: название модели
	llmRetriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "llm",
			Name:      "retries_total",
			Help:      "Total number of retry attempts for LLM requests",
		},
		[]string{"model"},
	)
)

const (
	statusSuccess   = "success"
	statusError     = "error"
	tokenTypePrompt = "prompt"
	tokenTypeCompl  = "completion"
)

// formatUserID converts user ID to string for metric labels.
func formatUserID(userID int64) string {
	return strconv.FormatInt(userID, 10)
}

// RecordLLMRequest записывает метрики LLM запроса.
func RecordLLMRequest(userID int64, model string, durationSeconds float64, success bool, promptTokens, completionTokens int, cost *float64) {
	status := statusSuccess
	if !success {
		status = statusError
	}

	uid := formatUserID(userID)
	llmRequestDuration.WithLabelValues(uid, model, status).Observe(durationSeconds)
	llmRequestsTotal.WithLabelValues(uid, model, status).Inc()

	if success {
		if promptTokens > 0 {
			llmTokensTotal.WithLabelValues(uid, model, tokenTypePrompt).Add(float64(promptTokens))
		}
		if completionTokens > 0 {
			llmTokensTotal.WithLabelValues(uid, model, tokenTypeCompl).Add(float64(completionTokens))
		}
		if cost != nil && *cost > 0 {
			llmCostTotal.WithLabelValues(uid, model).Add(*cost)
		}
	}
}

// RecordLLMRetry записывает retry-попытку.
func RecordLLMRetry(model string) {
	llmRetriesTotal.WithLabelValues(model).Inc()
}

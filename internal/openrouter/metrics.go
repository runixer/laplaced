package openrouter

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus metrics for the OpenRouter LLM client
//
// These metrics track:
// - LLM request duration
// - Token usage (prompt/completion)
// - LLM request cost

const metricsNamespace = "laplaced"

var (
	// llmRequestDuration measures LLM request duration.
	// Labels:
	//   - user_id: user identifier
	//   - model: model name
	//   - status: outcome (success, error)
	//   - job_type: operation type (interactive, background)
	llmRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "llm",
			Name:      "request_duration_seconds",
			Help:      "Duration of LLM API requests in seconds",
			// Buckets for typical LLM times: 0.5s - 120s
			// Extended to 120s for background jobs (archiver can take 60+ seconds)
			Buckets: []float64{0.5, 1, 2, 3, 5, 7, 10, 15, 20, 30, 45, 60, 90, 120},
		},
		[]string{"user_id", "model", "status", "job_type"},
	)

	// llmRequestsTotal counts LLM requests.
	// Labels:
	//   - user_id: user identifier
	//   - model: model name
	//   - status: outcome (success, error)
	//   - job_type: operation type (interactive, background)
	llmRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "llm",
			Name:      "requests_total",
			Help:      "Total number of LLM API requests",
		},
		[]string{"user_id", "model", "status", "job_type"},
	)

	// llmTokensTotal counts tokens used.
	// Labels:
	//   - user_id: user identifier
	//   - model: model name
	//   - type: token type (prompt, completion)
	//   - job_type: operation type (interactive, background)
	llmTokensTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "llm",
			Name:      "tokens_total",
			Help:      "Total number of tokens used for LLM requests",
		},
		[]string{"user_id", "model", "type", "job_type"},
	)

	// llmCostTotal tracks cumulative LLM request cost (USD).
	// Labels:
	//   - user_id: user identifier
	//   - model: model name
	//   - job_type: operation type (interactive, background)
	llmCostTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "llm",
			Name:      "cost_usd_total",
			Help:      "Total cost of LLM API requests in USD",
		},
		[]string{"user_id", "model", "job_type"},
	)

	// llmRetriesTotal counts retry attempts.
	// Labels:
	//   - model: model name
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
func formatUserID(userID string) string {
	return userID
}

// RecordLLMRequest records LLM request metrics.
// jobType should be "interactive" or "background" (use jobtype.JobType constants).
func RecordLLMRequest(userID string, model string, durationSeconds float64, success bool, promptTokens, completionTokens int, cost *float64, jobType string) {
	status := statusSuccess
	if !success {
		status = statusError
	}

	uid := formatUserID(userID)
	llmRequestDuration.WithLabelValues(uid, model, status, jobType).Observe(durationSeconds)
	llmRequestsTotal.WithLabelValues(uid, model, status, jobType).Inc()

	if success {
		if promptTokens > 0 {
			llmTokensTotal.WithLabelValues(uid, model, tokenTypePrompt, jobType).Add(float64(promptTokens))
		}
		if completionTokens > 0 {
			llmTokensTotal.WithLabelValues(uid, model, tokenTypeCompl, jobType).Add(float64(completionTokens))
		}
		if cost != nil && *cost > 0 {
			llmCostTotal.WithLabelValues(uid, model, jobType).Add(*cost)
		}
	}
}

// RecordLLMRetry records a retry attempt.
func RecordLLMRetry(model string) {
	llmRetriesTotal.WithLabelValues(model).Inc()
}

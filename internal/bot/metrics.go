package bot

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/runixer/laplaced/internal/storage"
)

// Prometheus metrics for Bot
//
// These metrics track:
// - Number of active sessions
// - Message processing time (end-to-end)
// - Number of processed messages

const metricsNamespace = "laplaced"

var (
	// activeSessions shows the current number of active sessions.
	activeSessions = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "active_sessions",
			Help:      "Current number of active chat sessions",
		},
	)

	// messageProcessingDuration measures end-to-end message processing time.
	// From receiving the message to sending the response.
	// Labels:
	//   - user_id: user identifier
	messageProcessingDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "message_processing_duration_seconds",
			Help:      "End-to-end duration of message processing in seconds",
			// Buckets for typical processing times: 0.5s - 120s
			// (LLM generation can take up to a minute)
			Buckets: []float64{0.5, 1, 2, 3, 5, 7, 10, 15, 20, 30, 45, 60, 90, 120},
		},
		[]string{"user_id"},
	)

	// messagesProcessedTotal counts the number of processed messages.
	// Labels:
	//   - user_id: user identifier
	//   - status: outcome (success, error)
	messagesProcessedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "messages_processed_total",
			Help:      "Total number of processed messages",
		},
		[]string{"user_id", "status"},
	)

	// contextTokens tracks context size in tokens.
	// Helps understand how large the contexts we send to the LLM are.
	contextTokens = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "context_tokens",
			Help:      "Approximate number of tokens in context sent to LLM",
			// Buckets for context sizes: 1K - 100K tokens
			Buckets: []float64{1000, 2000, 5000, 10000, 20000, 30000, 50000, 75000, 100000},
		},
	)

	// contextTokensBySource tracks tokens by context source.
	// Labels:
	//   - user_id: user identifier
	//   - source: profile, topics, session
	// Helps understand context distribution across sources.
	contextTokensBySource = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "context_tokens_by_source",
			Help:      "Approximate number of tokens in context by source",
			// Buckets for each source: 100 - 50K tokens
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
	// messageLLMFirstTokenDuration measures time-to-first-content in stream
	// mode: from the moment the agent begins the final-iteration LLM call to
	// the first user-visible delta.content (reasoning chunks don't count).
	// Only recorded when streaming is enabled and the final iteration emits
	// at least one content delta.
	// Labels:
	//   - user_id: user identifier
	messageLLMFirstTokenDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "message_llm_first_token_seconds",
			Help:      "Time from final-iteration LLM call start to first user-visible content delta (stream mode)",
			Buckets:   []float64{0.5, 1, 2, 3, 5, 7, 10, 15, 20, 30, 45, 60},
		},
		[]string{"user_id"},
	)

	// messageTelegramEditCount counts the number of editMessageText calls
	// the streaming sink issued per message. Useful for monitoring whether
	// throttle settings keep us under Telegram's edit rate limit.
	// Labels:
	//   - user_id: user identifier
	messageTelegramEditCount = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: "bot",
			Name:      "message_telegram_edit_count",
			Help:      "Number of editMessageText calls per message (stream mode)",
			Buckets:   []float64{0, 1, 2, 3, 5, 8, 12, 20, 30, 50},
		},
		[]string{"user_id"},
	)
)

const (
	statusSuccess = "success"
	statusError   = "error"
)

// Context source types
const (
	ContextSourceSystem  = "system"
	ContextSourceProfile = "profile"
	ContextSourceTopics  = "topics"
	ContextSourceSession = "session"
)

// formatUserID converts user ID to string for metric labels.
func formatUserID(userID storage.ScopeID) string {
	return string(userID)
}

// SetActiveSessions sets the current number of active sessions.
func SetActiveSessions(count int) {
	activeSessions.Set(float64(count))
}

// IncActiveSessions increments the active sessions counter by 1.
func IncActiveSessions() {
	activeSessions.Inc()
}

// DecActiveSessions decrements the active sessions counter by 1.
func DecActiveSessions() {
	activeSessions.Dec()
}

// RecordMessageProcessing records message processing metrics.
func RecordMessageProcessing(userID storage.ScopeID, durationSeconds float64, success bool) {
	uid := formatUserID(userID)
	messageProcessingDuration.WithLabelValues(uid).Observe(durationSeconds)

	status := statusSuccess
	if !success {
		status = statusError
	}
	messagesProcessedTotal.WithLabelValues(uid, status).Inc()
}

// RecordContextTokens records context size in tokens.
func RecordContextTokens(tokens int) {
	contextTokens.Observe(float64(tokens))
}

// RecordContextTokensBySource records tokens by context source.
func RecordContextTokensBySource(userID storage.ScopeID, source string, tokens int) {
	uid := formatUserID(userID)
	contextTokensBySource.WithLabelValues(uid, source).Observe(float64(tokens))
}

// ============================================================
// Per-message breakdown recording functions
// ============================================================

// RecordMessageLLM records total LLM duration and call count for a message.
func RecordMessageLLM(userID storage.ScopeID, totalDuration float64, callCount int) {
	uid := formatUserID(userID)
	messageLLMDuration.WithLabelValues(uid).Observe(totalDuration)
	messageLLMCalls.WithLabelValues(uid).Observe(float64(callCount))
}

// RecordMessageTools records total tool execution duration and call count for a message.
func RecordMessageTools(userID storage.ScopeID, totalDuration float64, callCount int) {
	uid := formatUserID(userID)
	messageToolDuration.WithLabelValues(uid).Observe(totalDuration)
	messageToolCalls.WithLabelValues(uid).Observe(float64(callCount))
}

// RecordMessageTelegram records total Telegram send duration and call count for a message.
func RecordMessageTelegram(userID storage.ScopeID, totalDuration float64, callCount int) {
	uid := formatUserID(userID)
	messageTelegramDuration.WithLabelValues(uid).Observe(totalDuration)
	messageTelegramCalls.WithLabelValues(uid).Observe(float64(callCount))
}

// RecordMessageReaction records SetMessageReaction duration.
func RecordMessageReaction(userID storage.ScopeID, duration float64) {
	uid := formatUserID(userID)
	messageReactionDuration.WithLabelValues(uid).Observe(duration)
}

// RecordMessageLLMFirstToken records time-to-first-content in stream mode.
// Only call when streaming actually produced at least one content delta;
// reasoning-only or empty responses should not be observed here.
func RecordMessageLLMFirstToken(userID storage.ScopeID, durationSeconds float64) {
	uid := formatUserID(userID)
	messageLLMFirstTokenDuration.WithLabelValues(uid).Observe(durationSeconds)
}

// RecordMessageTelegramEditCount records the number of editMessageText calls
// the streaming sink issued for a single message turn.
func RecordMessageTelegramEditCount(userID storage.ScopeID, count int) {
	uid := formatUserID(userID)
	messageTelegramEditCount.WithLabelValues(uid).Observe(float64(count))
}

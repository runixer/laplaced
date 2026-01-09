// Package agentlog provides unified logging for all LLM agents in the system.
package agentlog

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/runixer/laplaced/internal/storage"
)

// AgentType represents the type of LLM agent.
type AgentType string

// ConversationTurn represents one request-response cycle in a multi-turn conversation.
type ConversationTurn struct {
	Iteration  int         `json:"iteration"`
	Request    interface{} `json:"request"`  // Raw OpenRouter request
	Response   interface{} `json:"response"` // Raw OpenRouter response
	DurationMs int         `json:"duration_ms"`
}

// ConversationTurns holds all turns for multi-turn agents (e.g., Reranker with tool calls).
type ConversationTurns struct {
	Turns                 []ConversationTurn `json:"turns"`
	TotalDurationMs       int                `json:"total_duration_ms"`
	TotalPromptTokens     int                `json:"total_prompt_tokens"`
	TotalCompletionTokens int                `json:"total_completion_tokens"`
	TotalCost             *float64           `json:"total_cost,omitempty"`
}

const (
	AgentLaplace      AgentType = "laplace"
	AgentReranker     AgentType = "reranker"
	AgentSplitter     AgentType = "splitter"
	AgentMerger       AgentType = "merger"
	AgentEnricher     AgentType = "enricher"
	AgentArchivist    AgentType = "archivist"
	AgentDeduplicator AgentType = "deduplicator"
	AgentScout        AgentType = "scout"
)

// Entry represents a log entry for an agent call.
type Entry struct {
	UserID           int64
	AgentType        AgentType
	InputPrompt      string
	InputContext     interface{} // Will be JSON serialized (full API request)
	OutputResponse   string
	OutputParsed     interface{} // Will be JSON serialized
	OutputContext    interface{} // Will be JSON serialized (full API response)
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalCost        *float64
	DurationMs       int
	Metadata         interface{} // Agent-specific data, will be JSON serialized
	Success          bool
	ErrorMessage     string

	// ConversationTurns holds all request/response pairs for multi-turn agents.
	// Used by Reranker (tool calls) and potentially Laplace (agentic mode).
	ConversationTurns *ConversationTurns
}

// Logger handles logging for LLM agents.
type Logger struct {
	repo    storage.AgentLogRepository
	logger  *slog.Logger
	enabled bool
}

// NewLogger creates a new agent logger.
// If enabled is false, Log() calls will be no-ops.
func NewLogger(repo storage.AgentLogRepository, logger *slog.Logger, enabled bool) *Logger {
	return &Logger{
		repo:    repo,
		logger:  logger,
		enabled: enabled,
	}
}

// Log records an agent call entry.
// If the logger is disabled or repo is nil, this is a no-op.
func (l *Logger) Log(ctx context.Context, entry Entry) {
	if !l.enabled || l.repo == nil {
		return
	}

	// Serialize JSON fields
	inputContext := serializeJSON(entry.InputContext)
	outputParsed := serializeJSON(entry.OutputParsed)
	outputContext := serializeJSON(entry.OutputContext)
	metadata := serializeJSON(entry.Metadata)
	conversationTurns := serializeJSON(entry.ConversationTurns)

	log := storage.AgentLog{
		UserID:            entry.UserID,
		AgentType:         string(entry.AgentType),
		InputPrompt:       entry.InputPrompt,
		InputContext:      inputContext,
		OutputResponse:    entry.OutputResponse,
		OutputParsed:      outputParsed,
		OutputContext:     outputContext,
		Model:             entry.Model,
		PromptTokens:      entry.PromptTokens,
		CompletionTokens:  entry.CompletionTokens,
		TotalCost:         entry.TotalCost,
		DurationMs:        entry.DurationMs,
		Metadata:          metadata,
		Success:           entry.Success,
		ErrorMessage:      entry.ErrorMessage,
		ConversationTurns: conversationTurns,
		CreatedAt:         time.Now(),
	}

	if err := l.repo.AddAgentLog(log); err != nil {
		l.logger.Warn("failed to save agent log",
			"agent_type", entry.AgentType,
			"user_id", entry.UserID,
			"error", err,
		)
	}
}

// LogSuccess is a convenience method for logging successful agent calls.
func (l *Logger) LogSuccess(ctx context.Context, userID int64, agentType AgentType, inputPrompt string, inputContext interface{}, outputResponse string, outputParsed interface{}, outputContext interface{}, model string, promptTokens, completionTokens int, totalCost *float64, durationMs int, metadata interface{}) {
	l.Log(ctx, Entry{
		UserID:           userID,
		AgentType:        agentType,
		InputPrompt:      inputPrompt,
		InputContext:     inputContext,
		OutputResponse:   outputResponse,
		OutputParsed:     outputParsed,
		OutputContext:    outputContext,
		Model:            model,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalCost:        totalCost,
		DurationMs:       durationMs,
		Metadata:         metadata,
		Success:          true,
	})
}

// LogError is a convenience method for logging failed agent calls.
func (l *Logger) LogError(ctx context.Context, userID int64, agentType AgentType, inputPrompt string, inputContext interface{}, errorMessage string, model string, durationMs int, metadata interface{}) {
	l.Log(ctx, Entry{
		UserID:       userID,
		AgentType:    agentType,
		InputPrompt:  inputPrompt,
		InputContext: inputContext,
		Model:        model,
		DurationMs:   durationMs,
		Metadata:     metadata,
		Success:      false,
		ErrorMessage: errorMessage,
	})
}

// serializeJSON converts interface{} to JSON string.
// Returns empty string for nil or on error.
func serializeJSON(v interface{}) string {
	if v == nil {
		return ""
	}

	// If already a string, return as-is
	if s, ok := v.(string); ok {
		return s
	}

	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

// Enabled returns whether logging is enabled.
func (l *Logger) Enabled() bool {
	return l.enabled
}

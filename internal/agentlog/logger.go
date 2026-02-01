// Package agentlog provides unified logging for all LLM agents in the system.
package agentlog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
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
	AgentLaplace   AgentType = "laplace"
	AgentReranker  AgentType = "reranker"
	AgentSplitter  AgentType = "splitter"
	AgentMerger    AgentType = "merger"
	AgentEnricher  AgentType = "enricher"
	AgentArchivist AgentType = "archivist"
	AgentScout     AgentType = "scout"
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

	// Sanitize InputContext to remove large binary content (base64 images, audio, files)
	sanitizedInputContext := SanitizeInputContext(entry.InputContext)

	// Sanitize ConversationTurns to remove base64 data from Request/Response
	sanitizedConversationTurns := SanitizeConversationTurns(entry.ConversationTurns)

	// Serialize JSON fields
	inputContext := serializeJSON(sanitizedInputContext)
	outputParsed := serializeJSON(entry.OutputParsed)
	outputContext := serializeJSON(entry.OutputContext)
	metadata := serializeJSON(entry.Metadata)
	conversationTurns := serializeJSON(sanitizedConversationTurns)

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

	// Check for nil pointer wrapped in interface{}
	// This is needed because a nil pointer converted to interface{} is NOT nil
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr && rv.IsNil() {
		return ""
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

// SanitizeInputContext removes large binary content from InputContext to reduce DB size.
// It recursively walks through the JSON structure and replaces file/document data with placeholders.
// v0.6.0: Only handles FilePart format (type="file" with nested file object).
func SanitizeInputContext(data interface{}) interface{} {
	if data == nil {
		return nil
	}

	// If already a string, try to parse as JSON
	if s, ok := data.(string); ok {
		// Try to parse as JSON array or object
		var parsed interface{}
		if err := json.Unmarshal([]byte(s), &parsed); err == nil {
			return SanitizeInputContext(parsed)
		}
		// Not valid JSON, return as-is
		return s
	}

	// Handle map[string]interface{} (JSON objects)
	if m, ok := data.(map[string]interface{}); ok {
		// Check for multimodal content parts
		if typeVal, hasType := m["type"]; hasType {
			typeStr, _ := typeVal.(string)

			// Handle file/document type - replace large content with placeholder
			if typeStr == "file" || typeStr == "document" {
				// Check for nested "file" object (FilePart format)
				if fileObj, hasFile := m["file"]; hasFile {
					if fileMap, ok := fileObj.(map[string]interface{}); ok {
						var filename string
						var sizeBytes int

						// Get filename from nested file object
						if fn, ok := fileMap["filename"].(string); ok {
							filename = fn
						}

						// Check for file_data with base64 content
						if fileData, ok := fileMap["file_data"].(string); ok {
							if len(fileData) > 100 && strings.HasPrefix(fileData, "data:") {
								sizeBytes = len(fileData)

								// Extract mime type for format info
								format := "file"
								if strings.HasPrefix(fileData, "data:application/pdf") {
									format = "PDF"
								} else if strings.HasPrefix(fileData, "data:image/") {
									format = "image"
								} else if parts := strings.Split(fileData, ";"); len(parts) > 0 {
									if mimeParts := strings.Split(parts[0], "/"); len(mimeParts) > 1 {
										format = mimeParts[1]
									}
								}

								sizeKB := sizeBytes / 1024
								placeholder := fmt.Sprintf("FILE: %s (%s, %d KB base64)", filenameOrType(filename, format), format, sizeKB)

								// Return sanitized structure
								result := map[string]interface{}{
									"type": typeStr,
									"file": map[string]interface{}{
										"filename":  filenameOrType(filename, format),
										"file_data": placeholder,
									},
								}
								return result
							}
						}
					}
				}
			}
		}

		// Recursively sanitize nested maps and slices
		result := make(map[string]interface{})
		for k, v := range m {
			result[k] = SanitizeInputContext(v)
		}
		return result
	}

	// Handle []interface{} (JSON arrays)
	if arr, ok := data.([]interface{}); ok {
		result := make([]interface{}, len(arr))
		for i, v := range arr {
			result[i] = SanitizeInputContext(v)
		}
		return result
	}

	// Return primitive types as-is
	return data
}

// filenameOrType returns the filename if available, otherwise the type
func filenameOrType(filename, fileType string) string {
	if filename != "" {
		return filename
	}
	return fileType
}

// SanitizeConversationTurns creates a sanitized copy of ConversationTurns
// by sanitizing the Request/Response strings in each turn.
// It sanitizes the JSON content and marshals it back to a string.
func SanitizeConversationTurns(ct *ConversationTurns) *ConversationTurns {
	if ct == nil {
		return nil
	}
	sanitized := &ConversationTurns{
		TotalDurationMs:       ct.TotalDurationMs,
		TotalPromptTokens:     ct.TotalPromptTokens,
		TotalCompletionTokens: ct.TotalCompletionTokens,
		TotalCost:             ct.TotalCost,
		Turns:                 make([]ConversationTurn, len(ct.Turns)),
	}
	for i, turn := range ct.Turns {
		// Sanitize and marshal back to JSON string
		sanitizedRequest := serializeJSON(SanitizeInputContext(turn.Request))
		sanitizedResponse := serializeJSON(SanitizeInputContext(turn.Response))

		sanitized.Turns[i] = ConversationTurn{
			Iteration:  turn.Iteration,
			Request:    sanitizedRequest,
			Response:   sanitizedResponse,
			DurationMs: turn.DurationMs,
		}
	}
	return sanitized
}

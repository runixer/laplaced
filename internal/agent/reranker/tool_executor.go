// Package reranker provides the Reranker agent that uses tool calls to select
// the most relevant topics from vector search candidates.
package reranker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/storage"
)

// MessageRepository is the interface for loading topic messages.
type MessageRepository interface {
	GetMessagesByTopicID(ctx context.Context, topicID int64) ([]storage.Message, error)
}

// parseToolCallIDs extracts topic IDs from tool call arguments.
// Expected JSON format: {"topic_ids": [42, 18, 5]}
func parseToolCallIDs(arguments string) ([]int64, error) {
	var args struct {
		TopicIDs []int64 `json:"topic_ids"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, fmt.Errorf("failed to parse tool call arguments: %w", err)
	}
	return args.TopicIDs, nil
}

// loadTopicsContent loads full topic content for tool call response.
// Returns formatted content block and updates trace with stats.
func loadTopicsContent(
	ctx context.Context,
	userID int64,
	ids []int64,
	candidateMap map[int64]Candidate,
	msgRepo MessageRepository,
	logger *slog.Logger,
	tr *trace,
) string {
	var sb strings.Builder
	var totalChars int
	loadedCount := 0

	for _, id := range ids {
		candidate, ok := candidateMap[id]
		if !ok {
			if logger != nil {
				logger.Warn("reranker requested topic not in candidates", "topic_id", id)
			}
			continue
		}

		topic := candidate.Topic

		msgs, err := msgRepo.GetMessagesByTopicID(ctx, topic.ID)
		if err != nil {
			if logger != nil {
				logger.Warn("failed to load messages for reranker", "topic_id", id, "error", err)
			}
			continue
		}

		date := topic.CreatedAt.Format("2006-01-02")
		charCount := 0
		for _, m := range msgs {
			charCount += len(m.Content)
		}
		totalChars += charCount
		loadedCount++

		// Update trace with size info
		for i := range tr.candidates {
			if tr.candidates[i].TopicID == id {
				tr.candidates[i].SizeChars = charCount
				break
			}
		}

		fmt.Fprintf(&sb, "=== Topic %d ===\n", id)
		fmt.Fprintf(&sb, "Date: %s | %d msgs | ~%dK chars\n", date, len(msgs), charCount/1000)
		fmt.Fprintf(&sb, "Theme: %s\n\n", topic.Summary)

		for _, m := range msgs {
			timestamp := m.CreatedAt.Format("2006-01-02 15:04:05")
			role := m.Role
			switch role {
			case "assistant":
				role = "Assistant"
			case "user":
				role = "User"
			}
			fmt.Fprintf(&sb, "[%s (%s)]: %s\n", role, timestamp, m.Content)
		}
		sb.WriteString("\n")
	}

	if loadedCount > warnToolCallTopics || totalChars > warnToolCallChars {
		if logger != nil {
			logger.Warn("reranker large tool call payload",
				"user_id", userID,
				"topics_requested", len(ids),
				"topics_loaded", loadedCount,
				"total_chars", totalChars,
			)
		}
	}

	return sb.String()
}

// buildCandidateMap creates a map for quick topic lookup.
func buildCandidateMap(candidates []Candidate) map[int64]Candidate {
	m := make(map[int64]Candidate, len(candidates))
	for _, c := range candidates {
		m[c.TopicID] = c
	}
	return m
}

// buildPeopleMap creates a map for quick person lookup (v0.5.1).
func buildPeopleMap(candidates []PersonCandidate) map[int64]PersonCandidate {
	m := make(map[int64]PersonCandidate, len(candidates))
	for _, c := range candidates {
		m[c.PersonID] = c
	}
	return m
}

// buildArtifactsMap creates a map for quick artifact lookup (v0.6.0).
func buildArtifactsMap(candidates []ArtifactCandidate) map[int64]ArtifactCandidate {
	m := make(map[int64]ArtifactCandidate, len(candidates))
	for _, c := range candidates {
		m[c.ArtifactID] = c
	}
	return m
}

// extractReasoningText parses reasoning blocks from multimodal LLM response.
func extractReasoningText(details interface{}) string {
	if details == nil {
		return ""
	}

	arr, ok := details.([]interface{})
	if !ok {
		return ""
	}

	var sb strings.Builder
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		if t, ok := m["type"].(string); ok && t == "reasoning.text" {
			if text, ok := m["text"].(string); ok && text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(text)
			}
		}
	}
	return sb.String()
}

// truncateForLog truncates a string for logging purposes.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// saveTrace saves the trace to storage for debugging.
func saveTrace(
	ctx context.Context,
	agentLogger *agentlog.Logger,
	logger *slog.Logger,
	userID int64,
	originalQuery string,
	contextualizedQuery string,
	tr *trace,
	startTime time.Time,
	model string,
) {
	if agentLogger == nil {
		return
	}

	toolCallsJSON, err := json.Marshal(tr.toolCalls)
	if err != nil {
		if logger != nil {
			logger.Warn("failed to marshal reranker tool calls", "error", err)
		}
		toolCallsJSON = []byte("[]")
	}

	selectedIDsJSON, err := json.Marshal(tr.selectedTopics)
	if err != nil {
		if logger != nil {
			logger.Warn("failed to marshal reranker selected topics", "error", err)
		}
		selectedIDsJSON = []byte("[]")
	}

	reasoningJSON, err := json.Marshal(tr.reasoning)
	if err != nil {
		if logger != nil {
			logger.Warn("failed to marshal reranker reasoning", "error", err)
		}
		reasoningJSON = []byte("[]")
	}

	duration := time.Since(startTime)
	isSuccess := tr.fallbackReason == ""
	errorMsg := ""
	if !isSuccess {
		errorMsg = "fallback: " + tr.fallbackReason
	}

	fullPrompt := fmt.Sprintf("=== SYSTEM PROMPT ===\n%s\n\n=== USER PROMPT ===\n%s",
		tr.systemPrompt, tr.userPrompt)

	promptTokens, completionTokens := tr.tracker.TotalTokens()

	agentLogger.Log(ctx, agentlog.Entry{
		UserID:            userID,
		AgentType:         agentlog.AgentReranker,
		InputPrompt:       fullPrompt,
		InputContext:      tr.tracker.FirstRequest(),
		OutputResponse:    string(selectedIDsJSON),
		OutputParsed:      string(reasoningJSON),
		OutputContext:     tr.tracker.LastResponse(),
		Model:             model,
		PromptTokens:      promptTokens,
		CompletionTokens:  completionTokens,
		TotalCost:         tr.tracker.TotalCost(),
		DurationMs:        int(duration.Milliseconds()),
		ConversationTurns: tr.tracker.Build(),
		Metadata: map[string]interface{}{
			"original_query": originalQuery,
			"enriched_query": contextualizedQuery,
			"tool_calls":     string(toolCallsJSON),
			"candidates_in":  len(tr.candidates),
			"candidates_out": len(tr.selectedTopics),
		},
		Success:      isSuccess,
		ErrorMessage: errorMsg,
	})
}

package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// RerankerResult contains the output of the reranker
type RerankerResult struct {
	TopicIDs  []int64 // Final selected topic IDs
	PeopleIDs []int64 // For future Social Graph (v0.5)
}

// rerankerCandidate is a topic candidate for reranking
type rerankerCandidate struct {
	TopicID      int64
	Score        float32
	Topic        storage.Topic
	MessageCount int
}

// rerankerState tracks progress during agentic reranking
type rerankerState struct {
	requestedIDs []int64 // IDs requested via tool calls (for fallback)
}

// rerankerTrace collects debug data for the reranker UI
type rerankerTrace struct {
	candidates     []storage.RerankerCandidate
	toolCalls      []storage.RerankerToolCall
	selectedIDs    []int64
	fallbackReason string
}

// rerankerResponse is the expected JSON response from Flash
type rerankerResponse struct {
	Topics []int64 `json:"topics"`
	IDs    []int64 `json:"ids"` // Fallback: LLM sometimes uses "ids" instead of "topics"
}

// rerankCandidates uses Flash LLM to filter and select the most relevant topics
// from vector search candidates. Returns selected topic IDs or falls back to
// top-N from requestedIDs or vector search results on error.
func (s *Service) rerankCandidates(
	ctx context.Context,
	userID int64,
	candidates []rerankerCandidate,
	contextualizedQuery string,
	originalQuery string,
	currentMessages string,
	userProfile string,
) (*RerankerResult, error) {
	cfg := s.cfg.RAG.Reranker
	if !cfg.Enabled || len(candidates) == 0 {
		// Return top-N by vector score
		return s.fallbackToVectorTop(candidates, cfg.MaxTopics), nil
	}

	// Parse timeout
	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil {
		timeout = 10 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	state := &rerankerState{}
	trace := &rerankerTrace{}
	startTime := time.Now()
	var totalCost float64 // Accumulate cost across all LLM calls

	// Build candidate map and collect trace data
	candidateMap := s.buildCandidateMap(candidates)
	for _, c := range candidates {
		trace.candidates = append(trace.candidates, storage.RerankerCandidate{
			TopicID:      c.TopicID,
			Summary:      c.Topic.Summary,
			Score:        c.Score,
			Date:         c.Topic.CreatedAt.Format("2006-01-02"),
			MessageCount: c.MessageCount,
			SizeChars:    0, // Will be filled if topic is loaded
		})
	}

	// Build initial prompt
	lang := s.cfg.Bot.Language
	systemPrompt := fmt.Sprintf(
		s.translator.Get(lang, "rag.reranker_system_prompt"),
		cfg.MaxTopics,
	)

	candidatesList := s.formatCandidatesForReranker(candidates)
	userPrompt := fmt.Sprintf(
		s.translator.Get(lang, "rag.reranker_user_prompt"),
		time.Now().Format("2006-01-02"),
		originalQuery,
		contextualizedQuery,
		currentMessages,
		userProfile,
		candidatesList,
	)

	// Define tool for loading topic content
	tools := []openrouter.Tool{
		{
			Type: "function",
			Function: openrouter.ToolFunction{
				Name:        "get_topics_content",
				Description: s.translator.Get(lang, "rag.reranker_tool_description"),
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"ids": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "integer"},
							"description": s.translator.Get(lang, "rag.reranker_tool_param_description"),
						},
					},
					"required": []string{"ids"},
				},
			},
		},
	}

	messages := []openrouter.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	// Agentic loop
	toolCallCount := 0

	for toolCallCount < cfg.MaxToolCalls {
		resp, err := s.client.CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
			Model:          cfg.Model,
			Messages:       messages,
			Tools:          tools,
			Plugins:        []openrouter.Plugin{{ID: "response-healing"}},
			ResponseFormat: openrouter.ResponseFormat{Type: "json_object"},
			UserID:         userID,
		})

		if err != nil {
			s.logger.Warn("reranker LLM call failed", "error", err, "tool_calls", toolCallCount)
			trace.fallbackReason = "llm_error"
			result := s.fallbackFromState(state, candidates, cfg.MaxTopics)
			trace.selectedIDs = result.TopicIDs
			s.saveRerankerTrace(userID, originalQuery, contextualizedQuery, trace, startTime)
			s.recordRerankerMetrics(userID, startTime, toolCallCount, len(result.TopicIDs), totalCost)
			return result, nil
		}

		// Accumulate cost from this LLM call
		if resp.Usage.Cost != nil {
			totalCost += *resp.Usage.Cost
		}

		if len(resp.Choices) == 0 {
			s.logger.Warn("reranker got empty response")
			trace.fallbackReason = "empty_response"
			result := s.fallbackFromState(state, candidates, cfg.MaxTopics)
			trace.selectedIDs = result.TopicIDs
			s.saveRerankerTrace(userID, originalQuery, contextualizedQuery, trace, startTime)
			s.recordRerankerMetrics(userID, startTime, toolCallCount, len(result.TopicIDs), totalCost)
			return result, nil
		}

		choice := resp.Choices[0]

		// Check for tool calls
		if len(choice.Message.ToolCalls) > 0 {
			toolCallCount++

			// Process tool calls and add to state
			var toolResults []openrouter.Message
			toolCall := storage.RerankerToolCall{Iteration: toolCallCount}

			for _, tc := range choice.Message.ToolCalls {
				if tc.Function.Name == "get_topics_content" {
					ids, err := s.parseToolCallIDs(tc.Function.Arguments)
					if err != nil {
						s.logger.Warn("failed to parse tool call arguments", "error", err)
						continue
					}

					// Track requested IDs for fallback
					state.requestedIDs = append(state.requestedIDs, ids...)

					// Track for trace with topic summaries
					toolCall.TopicIDs = append(toolCall.TopicIDs, ids...)
					for _, id := range ids {
						if c, ok := candidateMap[id]; ok {
							toolCall.Topics = append(toolCall.Topics, storage.RerankerToolCallTopic{
								ID:      id,
								Summary: c.Topic.Summary,
							})
						}
					}

					// Load topic content and update size in trace
					content := s.loadTopicsContentWithSize(ctx, userID, ids, candidateMap, trace)
					toolResults = append(toolResults, openrouter.Message{
						Role:       "tool",
						Content:    content,
						ToolCallID: tc.ID,
					})
				}
			}

			trace.toolCalls = append(trace.toolCalls, toolCall)

			// Add assistant message with tool calls
			messages = append(messages, openrouter.Message{
				Role:      "assistant",
				Content:   choice.Message.Content,
				ToolCalls: choice.Message.ToolCalls,
			})

			// Add tool results
			messages = append(messages, toolResults...)
			continue
		}

		// No tool calls - expect final JSON response
		result, err := s.parseRerankerResponse(choice.Message.Content)
		if err != nil {
			s.logger.Warn("failed to parse reranker response", "error", err, "content", choice.Message.Content)
			trace.fallbackReason = "parse_error"
			fallbackResult := s.fallbackFromState(state, candidates, cfg.MaxTopics)
			trace.selectedIDs = fallbackResult.TopicIDs
			s.saveRerankerTrace(userID, originalQuery, contextualizedQuery, trace, startTime)
			s.recordRerankerMetrics(userID, startTime, toolCallCount, len(fallbackResult.TopicIDs), totalCost)
			return fallbackResult, nil
		}

		// Record metrics and log success
		s.recordRerankerMetrics(userID, startTime, toolCallCount, len(result.TopicIDs), totalCost)
		s.logger.Info("reranker completed",
			"user_id", userID,
			"candidates_in", len(candidates),
			"candidates_out", len(result.TopicIDs),
			"tool_calls", toolCallCount,
			"duration_ms", int(time.Since(startTime).Milliseconds()),
			"cost_usd", totalCost,
		)

		// Save trace on success
		trace.selectedIDs = result.TopicIDs
		s.saveRerankerTrace(userID, originalQuery, contextualizedQuery, trace, startTime)

		return result, nil
	}

	// Max tool calls reached
	s.logger.Warn("reranker max tool calls reached", "max", cfg.MaxToolCalls)
	RecordRerankerFallback(userID, "max_tool_calls")
	trace.fallbackReason = "max_tool_calls"
	result := s.fallbackFromState(state, candidates, cfg.MaxTopics)
	trace.selectedIDs = result.TopicIDs
	s.saveRerankerTrace(userID, originalQuery, contextualizedQuery, trace, startTime)
	s.recordRerankerMetrics(userID, startTime, cfg.MaxToolCalls, len(result.TopicIDs), totalCost)
	return result, nil
}

// recordRerankerMetrics records all reranker metrics in one place
func (s *Service) recordRerankerMetrics(userID int64, startTime time.Time, toolCalls int, outputCount int, cost float64) {
	duration := time.Since(startTime).Seconds()
	RecordRerankerDuration(userID, duration)
	RecordRerankerToolCalls(userID, toolCalls)
	RecordRerankerCandidatesOutput(userID, outputCount)
	if cost > 0 {
		RecordRerankerCost(userID, cost)
	}
}

// formatCandidatesForReranker formats candidates for the prompt
func (s *Service) formatCandidatesForReranker(candidates []rerankerCandidate) string {
	var sb strings.Builder
	for _, c := range candidates {
		// Format: [ID:42] 2025-07-25 | 20 msgs | Topic summary
		date := c.Topic.CreatedAt.Format("2006-01-02")
		fmt.Fprintf(&sb, "[ID:%d] %s | %d msgs | %s\n",
			c.TopicID, date, c.MessageCount, c.Topic.Summary)
	}
	return sb.String()
}

// buildCandidateMap creates a map for quick topic lookup
func (s *Service) buildCandidateMap(candidates []rerankerCandidate) map[int64]rerankerCandidate {
	m := make(map[int64]rerankerCandidate, len(candidates))
	for _, c := range candidates {
		m[c.TopicID] = c
	}
	return m
}

// parseToolCallIDs extracts topic IDs from tool call arguments
func (s *Service) parseToolCallIDs(arguments string) ([]int64, error) {
	var args struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, err
	}
	return args.IDs, nil
}

// parseRerankerResponse parses the JSON response from Flash
func (s *Service) parseRerankerResponse(content string) (*RerankerResult, error) {
	var resp rerankerResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		return nil, err
	}

	// Use Topics if present, otherwise fall back to IDs (LLM sometimes confuses field names)
	topicIDs := resp.Topics
	if len(topicIDs) == 0 && len(resp.IDs) > 0 {
		topicIDs = resp.IDs
	}

	return &RerankerResult{
		TopicIDs:  topicIDs,
		PeopleIDs: nil, // Reserved for v0.5 Social Graph
	}, nil
}

// fallbackToVectorTop returns top-N candidates by vector score
func (s *Service) fallbackToVectorTop(candidates []rerankerCandidate, maxTopics int) *RerankerResult {
	if maxTopics <= 0 {
		maxTopics = 5
	}
	if len(candidates) > maxTopics {
		candidates = candidates[:maxTopics]
	}

	ids := make([]int64, len(candidates))
	for i, c := range candidates {
		ids[i] = c.TopicID
	}

	return &RerankerResult{TopicIDs: ids}
}

// fallbackFromState returns top-N from requestedIDs if available, otherwise vector top
func (s *Service) fallbackFromState(state *rerankerState, candidates []rerankerCandidate, maxTopics int) *RerankerResult {
	if maxTopics <= 0 {
		maxTopics = 5
	}

	// If Flash requested some IDs, use them (they're pre-selected by Flash)
	if len(state.requestedIDs) > 0 {
		RecordRerankerFallback(0, "requested_ids") // userID=0 for simplicity
		ids := state.requestedIDs
		if len(ids) > maxTopics {
			ids = ids[:maxTopics]
		}
		return &RerankerResult{TopicIDs: ids}
	}

	// Fallback to vector search top
	RecordRerankerFallback(0, "vector_top")
	return s.fallbackToVectorTop(candidates, maxTopics)
}

// loadTopicsContentWithSize loads topic content and updates trace with sizes
func (s *Service) loadTopicsContentWithSize(
	ctx context.Context,
	userID int64,
	ids []int64,
	candidateMap map[int64]rerankerCandidate,
	trace *rerankerTrace,
) string {
	var sb strings.Builder

	for _, id := range ids {
		candidate, ok := candidateMap[id]
		if !ok {
			continue
		}

		topic := candidate.Topic

		// Load messages for this topic
		msgs, err := s.msgRepo.GetMessagesInRange(ctx, userID, topic.StartMsgID, topic.EndMsgID)
		if err != nil {
			s.logger.Warn("failed to load messages for reranker", "topic_id", id, "error", err)
			continue
		}

		// Format topic header
		date := topic.CreatedAt.Format("2006-01-02")
		charCount := 0
		for _, m := range msgs {
			charCount += len(m.Content)
		}

		// Update trace with size info
		for i := range trace.candidates {
			if trace.candidates[i].TopicID == id {
				trace.candidates[i].SizeChars = charCount
				break
			}
		}

		fmt.Fprintf(&sb, "=== Topic %d ===\n", id)
		fmt.Fprintf(&sb, "Date: %s | %d msgs | ~%dK chars\n", date, len(msgs), charCount/1000)
		fmt.Fprintf(&sb, "Theme: %s\n\n", topic.Summary)

		// Format messages
		for _, m := range msgs {
			timestamp := m.CreatedAt.Format("2006-01-02 15:04:05")
			role := m.Role
			if role == "assistant" {
				role = "Assistant"
			} else if role == "user" {
				role = "User"
			}
			fmt.Fprintf(&sb, "[%s (%s)]: %s\n", role, timestamp, m.Content)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// formatSessionMessages formats the current session history for the reranker prompt
func (s *Service) formatSessionMessages(history []storage.Message) string {
	if len(history) == 0 {
		return "(no current session messages)"
	}

	var sb strings.Builder
	// Take last N messages to avoid overwhelming the reranker
	maxMessages := 10
	start := 0
	if len(history) > maxMessages {
		start = len(history) - maxMessages
		fmt.Fprintf(&sb, "... (%d earlier messages omitted)\n\n", start)
	}

	for _, m := range history[start:] {
		role := m.Role
		if role == "assistant" {
			role = "Assistant"
		} else if role == "user" {
			role = "User"
		}
		// Truncate very long messages
		content := m.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		fmt.Fprintf(&sb, "[%s]: %s\n", role, content)
	}
	return sb.String()
}

// saveRerankerTrace saves the trace to storage for debugging
func (s *Service) saveRerankerTrace(
	userID int64,
	originalQuery string,
	contextualizedQuery string,
	trace *rerankerTrace,
	startTime time.Time,
) {
	if s.rerankerLogRepo == nil {
		return
	}

	// Serialize candidates
	candidatesJSON, err := json.Marshal(trace.candidates)
	if err != nil {
		s.logger.Warn("failed to marshal reranker candidates", "error", err)
		candidatesJSON = []byte("[]")
	}

	// Serialize tool calls
	toolCallsJSON, err := json.Marshal(trace.toolCalls)
	if err != nil {
		s.logger.Warn("failed to marshal reranker tool calls", "error", err)
		toolCallsJSON = []byte("[]")
	}

	// Serialize selected IDs
	selectedIDsJSON, err := json.Marshal(trace.selectedIDs)
	if err != nil {
		s.logger.Warn("failed to marshal reranker selected IDs", "error", err)
		selectedIDsJSON = []byte("[]")
	}

	log := storage.RerankerLog{
		UserID:          userID,
		OriginalQuery:   originalQuery,
		EnrichedQuery:   contextualizedQuery,
		CandidatesJSON:  string(candidatesJSON),
		ToolCallsJSON:   string(toolCallsJSON),
		SelectedIDsJSON: string(selectedIDsJSON),
		DurationTotalMs: int(time.Since(startTime).Milliseconds()),
	}

	if trace.fallbackReason != "" {
		log.FallbackReason = &trace.fallbackReason
	}

	if err := s.rerankerLogRepo.AddRerankerLog(log); err != nil {
		s.logger.Warn("failed to save reranker trace", "error", err)
	}
}

// formatUserProfileForReranker creates a compact profile summary for the reranker.
// It focuses on identity facts that help assess topic relevance.
func (s *Service) formatUserProfileForReranker(ctx context.Context, userID int64) string {
	facts, err := s.factRepo.GetFacts(userID)
	if err != nil {
		s.logger.Warn("failed to load facts for reranker", "error", err)
		return ""
	}

	// Filter to identity and high-importance facts only
	var relevant []storage.Fact
	for _, f := range facts {
		if f.Type == "identity" || f.Importance >= 80 {
			relevant = append(relevant, f)
		}
	}

	if len(relevant) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, f := range relevant {
		fmt.Fprintf(&sb, "- [%s] %s\n", f.Entity, f.Content)
	}
	return sb.String()
}

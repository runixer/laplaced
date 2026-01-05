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

// Tool call payload thresholds for monitoring (soft limits - warning only)
const (
	warnToolCallTopics = 15      // Warn if more than 15 topics requested
	warnToolCallChars  = 200_000 // Warn if payload exceeds ~200K chars (~50K tokens)
)

// Excerpt validation thresholds (soft limits - warning only)
const (
	warnExcerptMinChars = 100    // Warn if excerpt is too short
	warnExcerptMaxRatio = 0.5    // Warn if excerpt is >50% of topic (should be extracting, not copying)
	largeTopicThreshold = 25_000 // Only validate excerpts for topics >25K chars
)

// TopicSelection represents a selected topic with explanation
type TopicSelection struct {
	ID      int64   `json:"id"`
	Reason  string  `json:"reason"`
	Excerpt *string `json:"excerpt,omitempty"`
}

// RerankerResult contains the output of the reranker
type RerankerResult struct {
	Topics    []TopicSelection // Final selected topics with reasons
	PeopleIDs []int64          // For future Social Graph (v0.5)
}

// TopicIDs returns just the topic IDs (for backward compatibility)
func (r *RerankerResult) TopicIDs() []int64 {
	ids := make([]int64, len(r.Topics))
	for i, t := range r.Topics {
		ids[i] = t.ID
	}
	return ids
}

// rerankerCandidate is a topic candidate for reranking
type rerankerCandidate struct {
	TopicID      int64
	Score        float32
	Topic        storage.Topic
	MessageCount int
	SizeChars    int // Estimated: MessageCount * avgCharsPerMessage
}

// rerankerState tracks progress during agentic reranking
type rerankerState struct {
	requestedIDs   []int64          // IDs requested via tool calls (for fallback)
	loadedContents map[int64]string // Content of loaded topics (for excerpt validation)
}

// RerankerReasoningEntry holds reasoning text for one iteration
type RerankerReasoningEntry struct {
	Iteration int    `json:"iteration"`
	Text      string `json:"text"`
}

// rerankerTrace collects debug data for the reranker UI
type rerankerTrace struct {
	candidates     []storage.RerankerCandidate
	toolCalls      []storage.RerankerToolCall
	selectedTopics []TopicSelection
	fallbackReason string
	reasoning      []RerankerReasoningEntry
}

// rerankerResponse is the expected JSON response from Flash
// Supports both old format {"topics": [42, 18]} and new format {"topics": [{"id": 42, "reason": "..."}]}
type rerankerResponse struct {
	TopicIDs json.RawMessage `json:"topic_ids"`
	Topics   json.RawMessage `json:"topics"` // Fallback: old format
	IDs      []int64         `json:"ids"`    // Fallback: LLM sometimes uses bare "ids"
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
	mediaParts []interface{}, // Multimodal content (images, audio) from current message
) (*RerankerResult, error) {
	cfg := s.cfg.RAG.Reranker
	if !cfg.Enabled || len(candidates) == 0 {
		// Return top-N by vector score
		return s.fallbackToVectorTop(candidates, cfg.MaxTopics), nil
	}

	// Parse overall timeout
	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil {
		timeout = 20 * time.Second
	}

	// Parse per-turn timeout (default: overall timeout / (max_tool_calls + 1))
	turnTimeout, err := time.ParseDuration(cfg.TurnTimeout)
	if err != nil || turnTimeout <= 0 {
		turnTimeout = timeout / time.Duration(cfg.MaxToolCalls+1)
	}

	// Determine thinking level (default: "low")
	thinkingLevel := cfg.ThinkingLevel
	if thinkingLevel == "" {
		thinkingLevel = "low"
	}

	// Target context budget (default: 25000 chars)
	targetContextChars := cfg.TargetContextChars
	if targetContextChars <= 0 {
		targetContextChars = 25000
	}
	budgetK := targetContextChars / 1000

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	state := &rerankerState{
		loadedContents: make(map[int64]string),
	}
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
			SizeChars:    c.SizeChars, // Estimated from message count
		})
	}

	// Build initial prompt
	lang := s.cfg.Bot.Language
	selectCandidatesMax := cfg.Candidates / 2
	if selectCandidatesMax < cfg.MaxTopics*2 {
		selectCandidatesMax = cfg.MaxTopics * 2
	}
	systemPrompt := fmt.Sprintf(
		s.translator.Get(lang, "rag.reranker_system_prompt"),
		cfg.MaxTopics,                      // %d in constraints (max topics)
		budgetK,                            // %d in constraints (budget)
		cfg.MaxTopics,                      // %d in algorithm (don't stop at first N)
		cfg.MaxTopics, selectCandidatesMax, // %d-%d in algorithm (select N-M candidates)
		budgetK, // %d in excerpt_policy (budget)
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
						"topic_ids": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "integer"},
							"description": s.translator.Get(lang, "rag.reranker_tool_param_description"),
						},
					},
					"required": []string{"topic_ids"},
				},
			},
		},
	}

	// Build user message content - multimodal if mediaParts provided
	var userMessageContent interface{}
	if len(mediaParts) > 0 {
		// Include media in user message so Flash can see what user is asking about
		parts := []interface{}{
			openrouter.TextPart{Type: "text", Text: userPrompt},
		}
		parts = append(parts, mediaParts...)
		userMessageContent = parts
	} else {
		userMessageContent = userPrompt
	}

	messages := []openrouter.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessageContent},
	}

	// Agentic loop
	toolCallCount := 0

	for toolCallCount < cfg.MaxToolCalls {
		// Force tool call on first iteration to ensure protocol compliance
		var toolChoice any
		var responseFormat interface{}

		if toolCallCount == 0 {
			// Force get_topics_content call - Flash MUST load content before selecting
			// Note: Gemini doesn't support tool_choice + response_format together
			toolChoice = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": "get_topics_content",
				},
			}
		} else {
			// After first tool call, enable JSON response format for final selection
			responseFormat = openrouter.ResponseFormat{Type: "json_object"}
		}

		// Per-turn timeout to prevent one slow turn from eating entire budget
		turnCtx, turnCancel := context.WithTimeout(ctx, turnTimeout)

		// Configure reasoning (nil disables thinking entirely)
		// OpenRouter requires "enabled": true for Gemini Flash to return reasoning_details
		var reasoning *openrouter.ReasoningConfig
		if thinkingLevel != "off" && thinkingLevel != "" {
			reasoning = &openrouter.ReasoningConfig{
				Enabled: true,
				Effort:  thinkingLevel,
			}
		}

		resp, err := s.client.CreateChatCompletion(turnCtx, openrouter.ChatCompletionRequest{
			Model:          cfg.Model,
			Messages:       messages,
			Tools:          tools,
			ToolChoice:     toolChoice,
			Plugins:        []openrouter.Plugin{{ID: "response-healing"}},
			ResponseFormat: responseFormat,
			Reasoning:      reasoning,
			UserID:         userID,
		})
		turnCancel() // Always cancel to free resources

		if err != nil {
			// Distinguish between turn timeout and other errors
			fallbackReason := "llm_error"
			if ctx.Err() == context.DeadlineExceeded {
				fallbackReason = "timeout"
			} else if turnCtx.Err() == context.DeadlineExceeded {
				fallbackReason = "turn_timeout"
			}
			s.logger.Warn("reranker LLM call failed",
				"error", err,
				"tool_calls", toolCallCount,
				"reason", fallbackReason,
			)
			trace.fallbackReason = fallbackReason
			RecordRerankerFallback(userID, fallbackReason)
			result := s.fallbackFromState(state, candidates, cfg.MaxTopics)
			trace.selectedTopics = result.Topics
			s.saveRerankerTrace(userID, originalQuery, contextualizedQuery, trace, startTime)
			s.recordRerankerMetrics(userID, startTime, toolCallCount, len(result.Topics), totalCost)
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
			trace.selectedTopics = result.Topics
			s.saveRerankerTrace(userID, originalQuery, contextualizedQuery, trace, startTime)
			s.recordRerankerMetrics(userID, startTime, toolCallCount, len(result.Topics), totalCost)
			return result, nil
		}

		choice := resp.Choices[0]

		// Log and collect reasoning details if present
		if choice.Message.ReasoningDetails != nil {
			reasoningText := extractReasoningText(choice.Message.ReasoningDetails)
			s.logger.Debug("reranker reasoning",
				"user_id", userID,
				"iteration", toolCallCount+1,
				"reasoning", openrouter.FilterReasoningForLog(choice.Message.ReasoningDetails),
			)
			if reasoningText != "" {
				trace.reasoning = append(trace.reasoning, RerankerReasoningEntry{
					Iteration: toolCallCount + 1,
					Text:      reasoningText,
				})
			}
		}

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

					// Track requested IDs for fallback (deduplicated)
					for _, id := range ids {
						isDuplicate := false
						for _, existing := range state.requestedIDs {
							if existing == id {
								isDuplicate = true
								break
							}
						}
						if !isDuplicate {
							state.requestedIDs = append(state.requestedIDs, id)
						}
					}

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
					content := s.loadTopicsContentWithSize(ctx, userID, ids, candidateMap, trace, state)
					toolResults = append(toolResults, openrouter.Message{
						Role:       "tool",
						Content:    content,
						ToolCallID: tc.ID,
					})
				}
			}

			trace.toolCalls = append(trace.toolCalls, toolCall)

			// Add assistant message with tool calls (include reasoning_details for continuity)
			messages = append(messages, openrouter.Message{
				Role:             "assistant",
				Content:          choice.Message.Content,
				ToolCalls:        choice.Message.ToolCalls,
				ReasoningDetails: choice.Message.ReasoningDetails,
			})

			// Add tool results
			messages = append(messages, toolResults...)
			continue
		}

		// No tool calls - expect final JSON response
		// Protocol validation: Flash MUST call get_topics_content before returning
		if toolCallCount == 0 {
			s.logger.Warn("reranker protocol violation: no tool calls before final response",
				"user_id", userID,
				"content_preview", truncateForLog(choice.Message.Content, 200),
			)
			trace.fallbackReason = "protocol_violation"
			RecordRerankerFallback(userID, "protocol_violation")
			fallbackResult := s.fallbackToVectorTop(candidates, cfg.MaxTopics)
			trace.selectedTopics = fallbackResult.Topics
			s.saveRerankerTrace(userID, originalQuery, contextualizedQuery, trace, startTime)
			s.recordRerankerMetrics(userID, startTime, toolCallCount, len(fallbackResult.Topics), totalCost)
			return fallbackResult, nil
		}

		result, err := s.parseRerankerResponse(choice.Message.Content)
		if err != nil {
			s.logger.Warn("failed to parse reranker response", "error", err, "content", choice.Message.Content)
			trace.fallbackReason = "parse_error"
			fallbackResult := s.fallbackFromState(state, candidates, cfg.MaxTopics)
			trace.selectedTopics = fallbackResult.Topics
			s.saveRerankerTrace(userID, originalQuery, contextualizedQuery, trace, startTime)
			s.recordRerankerMetrics(userID, startTime, toolCallCount, len(fallbackResult.Topics), totalCost)
			return fallbackResult, nil
		}

		// Validate excerpts for large topics (warning only)
		s.validateExcerpts(userID, result, candidateMap, state.loadedContents)

		// Record metrics and log success
		s.recordRerankerMetrics(userID, startTime, toolCallCount, len(result.Topics), totalCost)
		s.logger.Info("reranker completed",
			"user_id", userID,
			"candidates_in", len(candidates),
			"candidates_out", len(result.Topics),
			"tool_calls", toolCallCount,
			"duration_ms", int(time.Since(startTime).Milliseconds()),
			"cost_usd", totalCost,
		)

		// Save trace on success
		trace.selectedTopics = result.Topics
		s.saveRerankerTrace(userID, originalQuery, contextualizedQuery, trace, startTime)

		return result, nil
	}

	// Max tool calls reached
	s.logger.Warn("reranker max tool calls reached", "max", cfg.MaxToolCalls)
	RecordRerankerFallback(userID, "max_tool_calls")
	trace.fallbackReason = "max_tool_calls"
	result := s.fallbackFromState(state, candidates, cfg.MaxTopics)
	trace.selectedTopics = result.Topics
	s.saveRerankerTrace(userID, originalQuery, contextualizedQuery, trace, startTime)
	s.recordRerankerMetrics(userID, startTime, cfg.MaxToolCalls, len(result.Topics), totalCost)
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
		// Format: [ID:42] 2025-07-25 | 20 msgs, ~10K chars | Topic summary
		date := c.Topic.CreatedAt.Format("2006-01-02")
		sizeStr := formatSizeChars(c.SizeChars)
		fmt.Fprintf(&sb, "[ID:%d] %s | %d msgs, %s | %s\n",
			c.TopicID, date, c.MessageCount, sizeStr, c.Topic.Summary)
	}
	return sb.String()
}

// formatSizeChars formats character count in a human-readable way
func formatSizeChars(chars int) string {
	if chars >= 1000 {
		return fmt.Sprintf("~%dK chars", chars/1000)
	}
	return fmt.Sprintf("~%d chars", chars)
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
		TopicIDs []int64 `json:"topic_ids"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, err
	}
	return args.TopicIDs, nil
}

// parseRerankerResponse parses the JSON response from Flash
// Supports: [{"id": 42, "reason": "..."}] (bare array)
//
//	{"topic_ids": [{"id": 42, "reason": "..."}]} (wrapped)
//	{"topic_ids": [42, 18]} (simple array)
//	{"topics": [...]} (fallback)
//	{"ids": [...]} (fallback)
func (s *Service) parseRerankerResponse(content string) (*RerankerResult, error) {
	// First, try bare array format: [{"id": 42, "reason": "..."}]
	var bareArray []TopicSelection
	if err := json.Unmarshal([]byte(content), &bareArray); err == nil && len(bareArray) > 0 && bareArray[0].ID != 0 {
		return &RerankerResult{Topics: bareArray}, nil
	}

	// Try object format: {"topic_ids": [...]}
	var resp rerankerResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		return nil, err
	}

	var topics []TopicSelection

	// Try topic_ids first (primary format)
	if len(resp.TopicIDs) > 0 {
		topics = s.parseTopicArray(resp.TopicIDs)
	}

	// Fallback to topics field
	if len(topics) == 0 && len(resp.Topics) > 0 {
		topics = s.parseTopicArray(resp.Topics)
	}

	// Fallback: LLM sometimes uses bare "ids"
	if len(topics) == 0 && len(resp.IDs) > 0 {
		for _, id := range resp.IDs {
			topics = append(topics, TopicSelection{ID: id})
		}
	}

	return &RerankerResult{
		Topics:    topics,
		PeopleIDs: nil, // Reserved for v0.5 Social Graph
	}, nil
}

// parseTopicArray parses a JSON array that can be either:
// - [{"id": 42, "reason": "..."}] (object format)
// - [42, 18] (simple int array)
func (s *Service) parseTopicArray(data json.RawMessage) []TopicSelection {
	// First, try object format: [{"id": 42, "reason": "..."}]
	var objFormat []TopicSelection
	if err := json.Unmarshal(data, &objFormat); err == nil && len(objFormat) > 0 && objFormat[0].ID != 0 {
		return objFormat
	}

	// Fall back to simple int array: [42, 18]
	var intFormat []int64
	if err := json.Unmarshal(data, &intFormat); err == nil {
		topics := make([]TopicSelection, len(intFormat))
		for i, id := range intFormat {
			topics[i] = TopicSelection{ID: id}
		}
		return topics
	}

	return nil
}

// validateExcerpts logs warnings for invalid excerpts (soft validation - monitoring only)
func (s *Service) validateExcerpts(userID int64, result *RerankerResult, candidateMap map[int64]rerankerCandidate, loadedContents map[int64]string) {
	for _, topic := range result.Topics {
		candidate, ok := candidateMap[topic.ID]
		if !ok {
			continue
		}

		topicSize := candidate.SizeChars

		// Only validate excerpts for large topics (>25K chars)
		if topicSize < largeTopicThreshold {
			continue
		}

		// Large topic without excerpt — should provide one
		if topic.Excerpt == nil || *topic.Excerpt == "" {
			s.logger.Warn("large topic without excerpt",
				"user_id", userID,
				"topic_id", topic.ID,
				"topic_size_chars", topicSize,
				"threshold", largeTopicThreshold,
			)
			continue
		}

		excerptLen := len(*topic.Excerpt)

		// Excerpt too short
		if excerptLen < warnExcerptMinChars {
			s.logger.Warn("excerpt too short",
				"user_id", userID,
				"topic_id", topic.ID,
				"excerpt_chars", excerptLen,
				"min_chars", warnExcerptMinChars,
			)
		}

		// Excerpt too long (just copying instead of extracting)
		ratio := float64(excerptLen) / float64(topicSize)
		if ratio > warnExcerptMaxRatio {
			s.logger.Warn("excerpt too long",
				"user_id", userID,
				"topic_id", topic.ID,
				"excerpt_chars", excerptLen,
				"topic_size_chars", topicSize,
				"ratio_percent", int(ratio*100),
				"max_ratio_percent", int(warnExcerptMaxRatio*100),
			)
		}

		// Validate excerpt content is actually from the topic (not hallucinated)
		if loadedContents != nil {
			topicContent, hasContent := loadedContents[topic.ID]
			if hasContent && !excerptFoundInContent(*topic.Excerpt, topicContent) {
				s.logger.Warn("excerpt not found in topic content (possible hallucination)",
					"user_id", userID,
					"topic_id", topic.ID,
					"excerpt_preview", truncateForLog(*topic.Excerpt, 200),
				)
			}
		}
	}
}

// excerptFoundInContent checks if excerpt text is actually present in topic content
// Uses normalized comparison to handle minor formatting differences
func excerptFoundInContent(excerpt, content string) bool {
	// Normalize both strings: collapse whitespace, lowercase
	normalizedExcerpt := normalizeForComparison(excerpt)
	normalizedContent := normalizeForComparison(content)

	// Split by various ellipsis patterns that Flash might use
	// Replace [...] and ... with a common separator
	normalizedExcerpt = strings.ReplaceAll(normalizedExcerpt, "[...]", "|||")
	normalizedExcerpt = strings.ReplaceAll(normalizedExcerpt, "...", "|||")
	parts := strings.Split(normalizedExcerpt, "|||")

	// Clean up parts - remove leading/trailing ellipsis artifacts
	var cleanParts []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, ".")
		part = strings.TrimSpace(part)
		if len(part) >= 20 { // Only meaningful parts
			cleanParts = append(cleanParts, part)
		}
	}

	if len(cleanParts) == 0 {
		// Very short excerpt, try direct substring match
		// Remove all ellipsis for direct comparison
		cleanExcerpt := strings.ReplaceAll(normalizedExcerpt, "|||", " ")
		cleanExcerpt = strings.TrimSpace(cleanExcerpt)
		if len(cleanExcerpt) < 10 {
			return true // Too short to validate meaningfully
		}
		return strings.Contains(normalizedContent, cleanExcerpt)
	}

	// At least one meaningful part should be found in content
	for _, part := range cleanParts {
		if strings.Contains(normalizedContent, part) {
			return true
		}
	}

	return false
}

// normalizeForComparison normalizes text for fuzzy comparison
func normalizeForComparison(s string) string {
	// Lowercase
	s = strings.ToLower(s)
	// Collapse multiple whitespace to single space
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// fallbackToVectorTop returns top-N candidates by vector score
func (s *Service) fallbackToVectorTop(candidates []rerankerCandidate, maxTopics int) *RerankerResult {
	if maxTopics <= 0 {
		maxTopics = 5
	}
	if len(candidates) > maxTopics {
		candidates = candidates[:maxTopics]
	}

	topics := make([]TopicSelection, len(candidates))
	for i, c := range candidates {
		topics[i] = TopicSelection{ID: c.TopicID}
	}

	return &RerankerResult{Topics: topics}
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
		topics := make([]TopicSelection, len(ids))
		for i, id := range ids {
			topics[i] = TopicSelection{ID: id}
		}
		return &RerankerResult{Topics: topics}
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
	state *rerankerState,
) string {
	var sb strings.Builder
	var totalChars int
	loadedCount := 0

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
		totalChars += charCount
		loadedCount++

		// Update trace with size info
		for i := range trace.candidates {
			if trace.candidates[i].TopicID == id {
				trace.candidates[i].SizeChars = charCount
				break
			}
		}

		fmt.Fprintf(&sb, "=== Topic %d ===\n", id)
		threshold := s.cfg.RAG.Reranker.LargeTopicThreshold
		if charCount > threshold {
			fmt.Fprintf(&sb, "Date: %s | %d msgs | ~%dK chars ⚠️ LARGE TOPIC - PROVIDE EXCERPT IN RESPONSE!\n", date, len(msgs), charCount/1000)
		} else {
			fmt.Fprintf(&sb, "Date: %s | %d msgs | ~%dK chars\n", date, len(msgs), charCount/1000)
		}
		fmt.Fprintf(&sb, "Theme: %s\n\n", topic.Summary)

		// Format messages and save raw content for excerpt validation
		var topicContent strings.Builder
		for _, m := range msgs {
			timestamp := m.CreatedAt.Format("2006-01-02 15:04:05")
			role := m.Role
			if role == "assistant" {
				role = "Assistant"
			} else if role == "user" {
				role = "User"
			}
			line := fmt.Sprintf("[%s (%s)]: %s\n", role, timestamp, m.Content)
			sb.WriteString(line)
			topicContent.WriteString(line)
		}
		sb.WriteString("\n")

		// Save content for excerpt validation
		if state != nil {
			state.loadedContents[id] = topicContent.String()
		}
	}

	// Warn if payload is large (soft limit for monitoring)
	if loadedCount > warnToolCallTopics || totalChars > warnToolCallChars {
		s.logger.Warn("large tool call payload",
			"user_id", userID,
			"topics_requested", len(ids),
			"topics_loaded", loadedCount,
			"total_chars", totalChars,
			"warn_topics_threshold", warnToolCallTopics,
			"warn_chars_threshold", warnToolCallChars,
		)
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

	// Serialize selected topics (full TopicSelection with reason and excerpt)
	selectedIDsJSON, err := json.Marshal(trace.selectedTopics)
	if err != nil {
		s.logger.Warn("failed to marshal reranker selected topics", "error", err)
		selectedIDsJSON = []byte("[]")
	}

	// Serialize reasoning entries
	reasoningJSON, err := json.Marshal(trace.reasoning)
	if err != nil {
		s.logger.Warn("failed to marshal reranker reasoning", "error", err)
		reasoningJSON = []byte("[]")
	}

	log := storage.RerankerLog{
		UserID:          userID,
		OriginalQuery:   originalQuery,
		EnrichedQuery:   contextualizedQuery,
		CandidatesJSON:  string(candidatesJSON),
		ToolCallsJSON:   string(toolCallsJSON),
		SelectedIDsJSON: string(selectedIDsJSON),
		ReasoningJSON:   string(reasoningJSON),
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

// truncateForLog truncates a string for logging purposes
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// extractReasoningText extracts human-readable reasoning text from reasoning_details.
// reasoning_details is typically []interface{} containing maps with "type" and "text"/"data" fields.
// Only "reasoning.text" type entries contain readable text; other types have encrypted data.
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

		// Only extract "reasoning.text" type entries
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

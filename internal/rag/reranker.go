package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/agent/prompts"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// Tool call payload thresholds for monitoring (soft limits - warning only)
const (
	warnToolCallTopics = 15      // Warn if more than 15 topics requested
	warnToolCallChars  = 200_000 // Warn if payload exceeds ~200K chars (~50K tokens)
)

// TopicSelection represents a selected topic with explanation
type TopicSelection struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
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
	requestedIDs []int64 // IDs requested via tool calls (for fallback)
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
	systemPrompt   string                // Full system prompt for debug UI
	userPrompt     string                // Full user prompt for debug UI
	tracker        *agentlog.TurnTracker // Unified turn tracking for multi-turn visualization
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
	recentTopics string,
	mediaParts []interface{}, // Multimodal content (images, audio) from current message
) (*RerankerResult, error) {
	cfg := s.cfg.Agents.Reranker
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

	// Determine thinking level (default: "minimal")
	thinkingLevel := cfg.ThinkingLevel
	if thinkingLevel == "" {
		thinkingLevel = "minimal"
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	state := &rerankerState{}
	trace := &rerankerTrace{
		tracker: agentlog.NewTurnTracker(),
	}
	startTime := time.Now()

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

	systemPrompt, err := s.translator.GetTemplate(lang, "rag.reranker_system_prompt", prompts.RerankerParams{
		Profile:       userProfile,
		RecentTopics:  recentTopics,
		MaxTopics:     cfg.MaxTopics,
		MinCandidates: cfg.MaxTopics,
		MaxCandidates: selectCandidatesMax,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build reranker system prompt: %w", err)
	}

	candidatesList := s.formatCandidatesForReranker(candidates)
	var userPrompt string
	userPrompt, err = s.translator.GetTemplate(lang, "rag.reranker_user_prompt", prompts.RerankerUserParams{
		Date:            time.Now().Format("2006-01-02"),
		Query:           originalQuery,
		EnrichedQuery:   contextualizedQuery,
		CurrentMessages: currentMessages,
		Candidates:      candidatesList,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build reranker user prompt: %w", err)
	}

	// Store prompts in trace for debug UI
	trace.systemPrompt = systemPrompt
	trace.userPrompt = userPrompt

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

		// Track turn timing for multi-turn visualization
		trace.tracker.StartTurn()

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
			Model:          cfg.GetModel(s.cfg.Agents.Default.Model),
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
			s.recordRerankerMetrics(userID, startTime, toolCallCount, len(result.Topics), trace.tracker.TotalCostValue())
			return result, nil
		}

		// Record turn with TurnTracker (captures request/response, tokens, cost, duration)
		trace.tracker.EndTurn(
			resp.DebugRequestBody,
			resp.DebugResponseBody,
			resp.Usage.PromptTokens,
			resp.Usage.CompletionTokens,
			resp.Usage.Cost,
		)

		if len(resp.Choices) == 0 {
			s.logger.Warn("reranker got empty response")
			trace.fallbackReason = "empty_response"
			result := s.fallbackFromState(state, candidates, cfg.MaxTopics)
			trace.selectedTopics = result.Topics
			s.saveRerankerTrace(userID, originalQuery, contextualizedQuery, trace, startTime)
			s.recordRerankerMetrics(userID, startTime, toolCallCount, len(result.Topics), trace.tracker.TotalCostValue())
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
			s.recordRerankerMetrics(userID, startTime, toolCallCount, len(fallbackResult.Topics), trace.tracker.TotalCostValue())
			return fallbackResult, nil
		}

		result, err := s.parseRerankerResponse(choice.Message.Content)
		if err != nil {
			s.logger.Warn("failed to parse reranker response", "error", err, "content", choice.Message.Content)
			trace.fallbackReason = "parse_error"
			fallbackResult := s.fallbackFromState(state, candidates, cfg.MaxTopics)
			trace.selectedTopics = fallbackResult.Topics
			s.saveRerankerTrace(userID, originalQuery, contextualizedQuery, trace, startTime)
			s.recordRerankerMetrics(userID, startTime, toolCallCount, len(fallbackResult.Topics), trace.tracker.TotalCostValue())
			return fallbackResult, nil
		}

		// Validate that selected topics exist in candidates (filter out hallucinated IDs)
		result = s.filterValidTopics(userID, result, candidateMap)
		if len(result.Topics) == 0 {
			s.logger.Warn("reranker returned no valid topics (all hallucinated)", "user_id", userID)
			trace.fallbackReason = "all_hallucinated"
			RecordRerankerFallback(userID, "all_hallucinated")
			fallbackResult := s.fallbackFromState(state, candidates, cfg.MaxTopics)
			trace.selectedTopics = fallbackResult.Topics
			s.saveRerankerTrace(userID, originalQuery, contextualizedQuery, trace, startTime)
			s.recordRerankerMetrics(userID, startTime, toolCallCount, len(fallbackResult.Topics), trace.tracker.TotalCostValue())
			return fallbackResult, nil
		}

		// Record metrics and log success
		s.recordRerankerMetrics(userID, startTime, toolCallCount, len(result.Topics), trace.tracker.TotalCostValue())
		s.logger.Info("reranker completed",
			"user_id", userID,
			"candidates_in", len(candidates),
			"candidates_out", len(result.Topics),
			"tool_calls", toolCallCount,
			"duration_ms", int(time.Since(startTime).Milliseconds()),
			"cost_usd", trace.tracker.TotalCostValue(),
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
	s.recordRerankerMetrics(userID, startTime, cfg.MaxToolCalls, len(result.Topics), trace.tracker.TotalCostValue())
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

// extractJSONFromResponse extracts the first JSON array or object from the response.
// LLM sometimes adds text before/after JSON (e.g., "Here are the results:\n[...]\nGreat!")
// Returns the original content if no extraction is needed.
func extractJSONFromResponse(content string) string {
	// Find the first [ or { which starts the JSON
	startArray := strings.Index(content, "[")
	startObj := strings.Index(content, "{")

	var startIdx int
	var openBrace, closeBrace byte
	if startArray >= 0 && (startObj < 0 || startArray < startObj) {
		startIdx = startArray
		openBrace = '['
		closeBrace = ']'
	} else if startObj >= 0 {
		startIdx = startObj
		openBrace = '{'
		closeBrace = '}'
	} else {
		return content // No JSON found, return as-is
	}

	// Find the matching closing bracket using brace counting
	depth := 0
	inString := false
	escaped := false
	for i := startIdx; i < len(content); i++ {
		c := content[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if c == openBrace {
			depth++
		} else if c == closeBrace {
			depth--
			if depth == 0 {
				return content[startIdx : i+1]
			}
		}
	}

	return content // Unbalanced braces, return as-is and let JSON parser handle it
}

// parseRerankerResponse parses the JSON response from Flash
// Supports: [{"id": 42, "reason": "..."}] (bare array)
//
//	{"topic_ids": [{"id": 42, "reason": "..."}]} (wrapped)
//	[{"topic_ids": [...]}] (wrapped in array - LLM quirk)
//	{"topic_ids": [42, 18]} (simple array)
//	{"topics": [...]} (fallback)
//	{"ids": [...]} (fallback)
func (s *Service) parseRerankerResponse(content string) (*RerankerResult, error) {
	// Extract JSON from response (LLM may add text before/after)
	content = extractJSONFromResponse(content)

	// First, try bare array format: [{"id": 42, "reason": "..."}]
	var bareArray []TopicSelection
	if err := json.Unmarshal([]byte(content), &bareArray); err == nil && len(bareArray) > 0 && bareArray[0].ID != 0 {
		return &RerankerResult{Topics: bareArray}, nil
	}

	// Try object format: {"topic_ids": [...]}
	var resp rerankerResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		// LLM sometimes wraps the response in an array: [{"topic_ids": [...]}]
		var wrappedArray []rerankerResponse
		if arrErr := json.Unmarshal([]byte(content), &wrappedArray); arrErr == nil && len(wrappedArray) > 0 {
			resp = wrappedArray[0]
		} else {
			return nil, err
		}
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

// filterValidTopics removes hallucinated topic IDs that don't exist in candidates.
// Returns a new result with only valid topics.
func (s *Service) filterValidTopics(userID int64, result *RerankerResult, candidateMap map[int64]rerankerCandidate) *RerankerResult {
	var validTopics []TopicSelection
	var hallucinatedIDs []int64

	for _, topic := range result.Topics {
		if _, ok := candidateMap[topic.ID]; ok {
			validTopics = append(validTopics, topic)
		} else {
			hallucinatedIDs = append(hallucinatedIDs, topic.ID)
		}
	}

	if len(hallucinatedIDs) > 0 {
		s.logger.Warn("reranker hallucinated topic IDs",
			"user_id", userID,
			"hallucinated_ids", hallucinatedIDs,
			"valid_count", len(validTopics),
			"total_returned", len(result.Topics),
		)
		RecordRerankerHallucination(userID, len(hallucinatedIDs))
	}

	return &RerankerResult{
		Topics:    validTopics,
		PeopleIDs: result.PeopleIDs,
	}
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
		msgs, err := s.msgRepo.GetMessagesByTopicID(ctx, topic.ID)
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
		fmt.Fprintf(&sb, "Date: %s | %d msgs | ~%dK chars\n", date, len(msgs), charCount/1000)
		fmt.Fprintf(&sb, "Theme: %s\n\n", topic.Summary)

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
	if s.agentLogger == nil {
		return
	}

	// Serialize tool calls
	toolCallsJSON, err := json.Marshal(trace.toolCalls)
	if err != nil {
		s.logger.Warn("failed to marshal reranker tool calls", "error", err)
		toolCallsJSON = []byte("[]")
	}

	// Serialize selected topics
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

	// Log to agent_logs for unified debug UI
	if s.agentLogger != nil {
		duration := time.Since(startTime)
		isSuccess := trace.fallbackReason == ""
		errorMsg := ""
		if !isSuccess {
			errorMsg = "fallback: " + trace.fallbackReason
		}

		// Combine system and user prompts for full LLM input visibility
		fullPrompt := fmt.Sprintf("=== SYSTEM PROMPT ===\n%s\n\n=== USER PROMPT ===\n%s",
			trace.systemPrompt, trace.userPrompt)

		// Get token totals from tracker
		promptTokens, completionTokens := trace.tracker.TotalTokens()

		s.agentLogger.Log(context.Background(), agentlog.Entry{
			UserID:            userID,
			AgentType:         agentlog.AgentReranker,
			InputPrompt:       fullPrompt,
			InputContext:      trace.tracker.FirstRequest(), // Full API request JSON (first request)
			OutputResponse:    string(selectedIDsJSON),
			OutputParsed:      string(reasoningJSON),
			OutputContext:     trace.tracker.LastResponse(), // Full API response JSON (last response)
			Model:             s.cfg.Agents.Reranker.GetModel(s.cfg.Agents.Default.Model),
			PromptTokens:      promptTokens,
			CompletionTokens:  completionTokens,
			TotalCost:         trace.tracker.TotalCost(),
			DurationMs:        int(duration.Milliseconds()),
			ConversationTurns: trace.tracker.Build(), // All turns for multi-turn visualization
			Metadata: map[string]interface{}{
				"original_query": originalQuery,
				"enriched_query": contextualizedQuery,
				"tool_calls":     string(toolCallsJSON),
				"candidates_in":  len(trace.candidates),
				"candidates_out": len(trace.selectedTopics),
			},
			Success:      isSuccess,
			ErrorMessage: errorMsg,
		})
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

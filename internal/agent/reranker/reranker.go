package reranker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"log/slog"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/prompts"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// MessageRepository is the interface for loading topic messages.
type MessageRepository interface {
	GetMessagesByTopicID(ctx context.Context, topicID int64) ([]storage.Message, error)
}

// Reranker uses tool calls to select the most relevant topics from vector search candidates.
type Reranker struct {
	client      openrouter.Client
	cfg         *config.Config
	logger      *slog.Logger
	translator  *i18n.Translator
	msgRepo     MessageRepository
	agentLogger *agentlog.Logger
}

// New creates a new Reranker agent.
func New(
	client openrouter.Client,
	cfg *config.Config,
	logger *slog.Logger,
	translator *i18n.Translator,
	msgRepo MessageRepository,
	agentLogger *agentlog.Logger,
) *Reranker {
	return &Reranker{
		client:      client,
		cfg:         cfg,
		logger:      logger.With("component", "reranker"),
		translator:  translator,
		msgRepo:     msgRepo,
		agentLogger: agentLogger,
	}
}

// Type returns the agent type.
func (r *Reranker) Type() agent.AgentType {
	return agent.TypeReranker
}

// Capabilities returns the agent's capabilities.
func (r *Reranker) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		IsAgentic:    true,
		OutputFormat: "json",
	}
}

// Description returns a human-readable description.
func (r *Reranker) Description() string {
	return "Selects relevant topics from candidates using tool calls"
}

// Execute runs the reranker with the given request.
// Required params: candidates, contextualized_query, original_query, current_messages
// Optional params: media_parts
// Uses SharedContext for user_profile and recent_topics if available.
func (r *Reranker) Execute(ctx context.Context, req *agent.Request) (*agent.Response, error) {
	// Extract parameters
	candidates, ok := req.Params[ParamCandidates].([]Candidate)
	if !ok {
		return nil, fmt.Errorf("candidates parameter required")
	}

	contextualizedQuery, _ := req.Params[ParamContextualizedQuery].(string)
	originalQuery, _ := req.Params[ParamOriginalQuery].(string)
	currentMessages, _ := req.Params[ParamCurrentMessages].(string)
	mediaParts, _ := req.Params[ParamMediaParts].([]interface{})

	// Get user profile and recent topics from SharedContext
	var userProfile, recentTopics string
	var userID int64
	if req.Shared != nil {
		userID = req.Shared.UserID
		userProfile = req.Shared.Profile
		recentTopics = req.Shared.RecentTopics
	} else if shared := agent.FromContext(ctx); shared != nil {
		userID = shared.UserID
		userProfile = shared.Profile
		recentTopics = shared.RecentTopics
	}

	// Get userID from params if not in SharedContext
	if userID == 0 {
		if id, ok := req.Params["user_id"].(int64); ok {
			userID = id
		}
	}

	result, err := r.rerank(ctx, userID, candidates, contextualizedQuery, originalQuery, currentMessages, userProfile, recentTopics, mediaParts)
	if err != nil {
		return nil, err
	}

	return &agent.Response{
		Structured: result,
		Metadata: map[string]any{
			"topics_count": len(result.Topics),
		},
	}, nil
}

// rerank is the main reranking logic.
func (r *Reranker) rerank(
	ctx context.Context,
	userID int64,
	candidates []Candidate,
	contextualizedQuery string,
	originalQuery string,
	currentMessages string,
	userProfile string,
	recentTopics string,
	mediaParts []interface{},
) (*Result, error) {
	cfg := r.cfg.Agents.Reranker
	if !cfg.Enabled || len(candidates) == 0 {
		return r.fallbackToVectorTop(candidates, cfg.MaxTopics), nil
	}

	// Parse timeouts
	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil {
		timeout = 20 * time.Second
	}

	turnTimeout, err := time.ParseDuration(cfg.TurnTimeout)
	if err != nil || turnTimeout <= 0 {
		turnTimeout = timeout / time.Duration(cfg.MaxToolCalls+1)
	}

	// Determine thinking level
	thinkingLevel := cfg.ThinkingLevel
	if thinkingLevel == "" {
		thinkingLevel = "minimal"
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	st := &state{}
	tr := &trace{
		tracker: agentlog.NewTurnTracker(),
	}
	startTime := time.Now()

	// Build candidate map and collect trace data
	candidateMap := r.buildCandidateMap(candidates)
	for _, c := range candidates {
		tr.candidates = append(tr.candidates, storage.RerankerCandidate{
			TopicID:      c.TopicID,
			Summary:      c.Topic.Summary,
			Score:        c.Score,
			Date:         c.Topic.CreatedAt.Format("2006-01-02"),
			MessageCount: c.MessageCount,
			SizeChars:    c.SizeChars,
		})
	}

	// Build initial prompt
	lang := r.cfg.Bot.Language
	selectCandidatesMax := cfg.Candidates / 2
	if selectCandidatesMax < cfg.MaxTopics*2 {
		selectCandidatesMax = cfg.MaxTopics * 2
	}

	systemPrompt, err := r.translator.GetTemplate(lang, "rag.reranker_system_prompt", prompts.RerankerParams{
		Profile:       userProfile,
		RecentTopics:  recentTopics,
		MaxTopics:     cfg.MaxTopics,
		MinCandidates: cfg.MaxTopics,
		MaxCandidates: selectCandidatesMax,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build reranker system prompt: %w", err)
	}

	candidatesList := r.formatCandidatesForReranker(candidates)
	userPrompt, err := r.translator.GetTemplate(lang, "rag.reranker_user_prompt", prompts.RerankerUserParams{
		Date:            time.Now().Format("2006-01-02"),
		Query:           originalQuery,
		EnrichedQuery:   contextualizedQuery,
		CurrentMessages: currentMessages,
		Candidates:      candidatesList,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build reranker user prompt: %w", err)
	}

	tr.systemPrompt = systemPrompt
	tr.userPrompt = userPrompt

	// Define tool
	tools := []openrouter.Tool{
		{
			Type: "function",
			Function: openrouter.ToolFunction{
				Name:        "get_topics_content",
				Description: r.translator.Get(lang, "rag.reranker_tool_description"),
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"topic_ids": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "integer"},
							"description": r.translator.Get(lang, "rag.reranker_tool_param_description"),
						},
					},
					"required": []string{"topic_ids"},
				},
			},
		},
	}

	// Build user message content
	var userMessageContent interface{}
	if len(mediaParts) > 0 {
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
		var toolChoice any
		var responseFormat interface{}

		if toolCallCount == 0 {
			toolChoice = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": "get_topics_content",
				},
			}
		} else {
			responseFormat = openrouter.ResponseFormat{Type: "json_object"}
		}

		tr.tracker.StartTurn()

		turnCtx, turnCancel := context.WithTimeout(ctx, turnTimeout)

		var reasoning *openrouter.ReasoningConfig
		if thinkingLevel != "off" && thinkingLevel != "" {
			reasoning = &openrouter.ReasoningConfig{
				Enabled: true,
				Effort:  thinkingLevel,
			}
		}

		resp, err := r.client.CreateChatCompletion(turnCtx, openrouter.ChatCompletionRequest{
			Model:          cfg.GetModel(r.cfg.Agents.Default.Model),
			Messages:       messages,
			Tools:          tools,
			ToolChoice:     toolChoice,
			Plugins:        []openrouter.Plugin{{ID: "response-healing"}},
			ResponseFormat: responseFormat,
			Reasoning:      reasoning,
			UserID:         userID,
		})
		turnCancel()

		if err != nil {
			fallbackReason := "llm_error"
			if ctx.Err() == context.DeadlineExceeded {
				fallbackReason = "timeout"
			} else if turnCtx.Err() == context.DeadlineExceeded {
				fallbackReason = "turn_timeout"
			}
			r.logger.Warn("reranker LLM call failed",
				"error", err,
				"tool_calls", toolCallCount,
				"reason", fallbackReason,
			)
			tr.fallbackReason = fallbackReason
			result := r.fallbackFromState(st, candidates, cfg.MaxTopics)
			tr.selectedTopics = result.Topics
			r.saveTrace(userID, originalQuery, contextualizedQuery, tr, startTime)
			return result, nil
		}

		tr.tracker.EndTurn(
			resp.DebugRequestBody,
			resp.DebugResponseBody,
			resp.Usage.PromptTokens,
			resp.Usage.CompletionTokens,
			resp.Usage.Cost,
		)

		if len(resp.Choices) == 0 {
			r.logger.Warn("reranker got empty response")
			tr.fallbackReason = "empty_response"
			result := r.fallbackFromState(st, candidates, cfg.MaxTopics)
			tr.selectedTopics = result.Topics
			r.saveTrace(userID, originalQuery, contextualizedQuery, tr, startTime)
			return result, nil
		}

		choice := resp.Choices[0]

		// Log and collect reasoning details
		if choice.Message.ReasoningDetails != nil {
			reasoningText := extractReasoningText(choice.Message.ReasoningDetails)
			r.logger.Debug("reranker reasoning",
				"user_id", userID,
				"iteration", toolCallCount+1,
				"reasoning", openrouter.FilterReasoningForLog(choice.Message.ReasoningDetails),
			)
			if reasoningText != "" {
				tr.reasoning = append(tr.reasoning, ReasoningEntry{
					Iteration: toolCallCount + 1,
					Text:      reasoningText,
				})
			}
		}

		// Check for tool calls
		if len(choice.Message.ToolCalls) > 0 {
			toolCallCount++

			var toolResults []openrouter.Message
			toolCall := storage.RerankerToolCall{Iteration: toolCallCount}

			for _, tc := range choice.Message.ToolCalls {
				if tc.Function.Name == "get_topics_content" {
					ids, err := r.parseToolCallIDs(tc.Function.Arguments)
					if err != nil {
						r.logger.Warn("failed to parse tool call arguments", "error", err)
						continue
					}

					// Track requested IDs for fallback
					for _, id := range ids {
						isDuplicate := false
						for _, existing := range st.requestedIDs {
							if existing == id {
								isDuplicate = true
								break
							}
						}
						if !isDuplicate {
							st.requestedIDs = append(st.requestedIDs, id)
						}
					}

					toolCall.TopicIDs = append(toolCall.TopicIDs, ids...)
					for _, id := range ids {
						if c, ok := candidateMap[id]; ok {
							toolCall.Topics = append(toolCall.Topics, storage.RerankerToolCallTopic{
								ID:      id,
								Summary: c.Topic.Summary,
							})
						}
					}

					content := r.loadTopicsContent(ctx, userID, ids, candidateMap, tr, st)
					toolResults = append(toolResults, openrouter.Message{
						Role:       "tool",
						Content:    content,
						ToolCallID: tc.ID,
					})
				}
			}

			tr.toolCalls = append(tr.toolCalls, toolCall)

			messages = append(messages, openrouter.Message{
				Role:             "assistant",
				Content:          choice.Message.Content,
				ToolCalls:        choice.Message.ToolCalls,
				ReasoningDetails: choice.Message.ReasoningDetails,
			})
			messages = append(messages, toolResults...)
			continue
		}

		// No tool calls - expect final JSON response
		if toolCallCount == 0 {
			r.logger.Warn("reranker protocol violation: no tool calls before final response",
				"user_id", userID,
				"content_preview", truncateForLog(choice.Message.Content, 200),
			)
			tr.fallbackReason = "protocol_violation"
			fallbackResult := r.fallbackToVectorTop(candidates, cfg.MaxTopics)
			tr.selectedTopics = fallbackResult.Topics
			r.saveTrace(userID, originalQuery, contextualizedQuery, tr, startTime)
			return fallbackResult, nil
		}

		result, err := r.parseResponse(choice.Message.Content)
		if err != nil {
			r.logger.Warn("failed to parse reranker response", "error", err, "content", choice.Message.Content)
			tr.fallbackReason = "parse_error"
			fallbackResult := r.fallbackFromState(st, candidates, cfg.MaxTopics)
			tr.selectedTopics = fallbackResult.Topics
			r.saveTrace(userID, originalQuery, contextualizedQuery, tr, startTime)
			return fallbackResult, nil
		}

		// Validate topics
		result = r.filterValidTopics(userID, result, candidateMap)
		if len(result.Topics) == 0 {
			r.logger.Warn("reranker returned no valid topics (all hallucinated)", "user_id", userID)
			tr.fallbackReason = "all_hallucinated"
			fallbackResult := r.fallbackFromState(st, candidates, cfg.MaxTopics)
			tr.selectedTopics = fallbackResult.Topics
			r.saveTrace(userID, originalQuery, contextualizedQuery, tr, startTime)
			return fallbackResult, nil
		}

		r.logger.Info("reranker completed",
			"user_id", userID,
			"candidates_in", len(candidates),
			"candidates_out", len(result.Topics),
			"tool_calls", toolCallCount,
			"duration_ms", int(time.Since(startTime).Milliseconds()),
			"cost_usd", tr.tracker.TotalCostValue(),
		)

		tr.selectedTopics = result.Topics
		r.saveTrace(userID, originalQuery, contextualizedQuery, tr, startTime)

		return result, nil
	}

	// Max tool calls reached
	r.logger.Warn("reranker max tool calls reached", "max", cfg.MaxToolCalls)
	tr.fallbackReason = "max_tool_calls"
	result := r.fallbackFromState(st, candidates, cfg.MaxTopics)
	tr.selectedTopics = result.Topics
	r.saveTrace(userID, originalQuery, contextualizedQuery, tr, startTime)
	return result, nil
}

// buildCandidateMap creates a map for quick topic lookup.
func (r *Reranker) buildCandidateMap(candidates []Candidate) map[int64]Candidate {
	m := make(map[int64]Candidate, len(candidates))
	for _, c := range candidates {
		m[c.TopicID] = c
	}
	return m
}

// formatCandidatesForReranker formats candidates for the prompt.
func (r *Reranker) formatCandidatesForReranker(candidates []Candidate) string {
	var sb strings.Builder
	for _, c := range candidates {
		date := c.Topic.CreatedAt.Format("2006-01-02")
		sizeStr := formatSizeChars(c.SizeChars)
		fmt.Fprintf(&sb, "[ID:%d] %s | %d msgs, %s | %s\n",
			c.TopicID, date, c.MessageCount, sizeStr, c.Topic.Summary)
	}
	return sb.String()
}

func formatSizeChars(chars int) string {
	if chars >= 1000 {
		return fmt.Sprintf("~%dK chars", chars/1000)
	}
	return fmt.Sprintf("~%d chars", chars)
}

// parseToolCallIDs extracts topic IDs from tool call arguments.
func (r *Reranker) parseToolCallIDs(arguments string) ([]int64, error) {
	var args struct {
		TopicIDs []int64 `json:"topic_ids"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, err
	}
	return args.TopicIDs, nil
}

// loadTopicsContent loads topic content for tool call response.
func (r *Reranker) loadTopicsContent(
	ctx context.Context,
	userID int64,
	ids []int64,
	candidateMap map[int64]Candidate,
	tr *trace,
	st *state,
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

		msgs, err := r.msgRepo.GetMessagesByTopicID(ctx, topic.ID)
		if err != nil {
			r.logger.Warn("failed to load messages for reranker", "topic_id", id, "error", err)
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
			if role == "assistant" {
				role = "Assistant"
			} else if role == "user" {
				role = "User"
			}
			fmt.Fprintf(&sb, "[%s (%s)]: %s\n", role, timestamp, m.Content)
		}
		sb.WriteString("\n")
	}

	if loadedCount > warnToolCallTopics || totalChars > warnToolCallChars {
		r.logger.Warn("large tool call payload",
			"user_id", userID,
			"topics_requested", len(ids),
			"topics_loaded", loadedCount,
			"total_chars", totalChars,
		)
	}

	return sb.String()
}

// parseResponse parses the JSON response from Flash.
func (r *Reranker) parseResponse(content string) (*Result, error) {
	content = extractJSONFromResponse(content)

	// Try bare array format
	var bareArray []TopicSelection
	if err := json.Unmarshal([]byte(content), &bareArray); err == nil && len(bareArray) > 0 && bareArray[0].ID != 0 {
		return &Result{Topics: bareArray}, nil
	}

	// Try object format
	var resp response
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		var wrappedArray []response
		if arrErr := json.Unmarshal([]byte(content), &wrappedArray); arrErr == nil && len(wrappedArray) > 0 {
			resp = wrappedArray[0]
		} else {
			return nil, err
		}
	}

	var topics []TopicSelection

	if len(resp.TopicIDs) > 0 {
		topics = r.parseTopicArray(resp.TopicIDs)
	}

	if len(topics) == 0 && len(resp.Topics) > 0 {
		topics = r.parseTopicArray(resp.Topics)
	}

	if len(topics) == 0 && len(resp.IDs) > 0 {
		for _, id := range resp.IDs {
			topics = append(topics, TopicSelection{ID: id})
		}
	}

	return &Result{
		Topics:    topics,
		PeopleIDs: nil,
	}, nil
}

func (r *Reranker) parseTopicArray(data json.RawMessage) []TopicSelection {
	var objFormat []TopicSelection
	if err := json.Unmarshal(data, &objFormat); err == nil && len(objFormat) > 0 && objFormat[0].ID != 0 {
		return objFormat
	}

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

// filterValidTopics removes hallucinated topic IDs.
func (r *Reranker) filterValidTopics(userID int64, result *Result, candidateMap map[int64]Candidate) *Result {
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
		r.logger.Warn("reranker hallucinated topic IDs",
			"user_id", userID,
			"hallucinated_ids", hallucinatedIDs,
			"valid_count", len(validTopics),
			"total_returned", len(result.Topics),
		)
	}

	return &Result{
		Topics:    validTopics,
		PeopleIDs: result.PeopleIDs,
	}
}

// fallbackToVectorTop returns top-N candidates by vector score.
func (r *Reranker) fallbackToVectorTop(candidates []Candidate, maxTopics int) *Result {
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

	return &Result{Topics: topics}
}

// fallbackFromState returns top-N from requestedIDs if available.
func (r *Reranker) fallbackFromState(st *state, candidates []Candidate, maxTopics int) *Result {
	if maxTopics <= 0 {
		maxTopics = 5
	}

	if len(st.requestedIDs) > 0 {
		ids := st.requestedIDs
		if len(ids) > maxTopics {
			ids = ids[:maxTopics]
		}
		topics := make([]TopicSelection, len(ids))
		for i, id := range ids {
			topics[i] = TopicSelection{ID: id}
		}
		return &Result{Topics: topics}
	}

	return r.fallbackToVectorTop(candidates, maxTopics)
}

// saveTrace saves the trace to storage for debugging.
func (r *Reranker) saveTrace(
	userID int64,
	originalQuery string,
	contextualizedQuery string,
	tr *trace,
	startTime time.Time,
) {
	if r.agentLogger == nil {
		return
	}

	toolCallsJSON, err := json.Marshal(tr.toolCalls)
	if err != nil {
		r.logger.Warn("failed to marshal reranker tool calls", "error", err)
		toolCallsJSON = []byte("[]")
	}

	selectedIDsJSON, err := json.Marshal(tr.selectedTopics)
	if err != nil {
		r.logger.Warn("failed to marshal reranker selected topics", "error", err)
		selectedIDsJSON = []byte("[]")
	}

	reasoningJSON, err := json.Marshal(tr.reasoning)
	if err != nil {
		r.logger.Warn("failed to marshal reranker reasoning", "error", err)
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

	r.agentLogger.Log(context.Background(), agentlog.Entry{
		UserID:            userID,
		AgentType:         agentlog.AgentReranker,
		InputPrompt:       fullPrompt,
		InputContext:      tr.tracker.FirstRequest(),
		OutputResponse:    string(selectedIDsJSON),
		OutputParsed:      string(reasoningJSON),
		OutputContext:     tr.tracker.LastResponse(),
		Model:             r.cfg.Agents.Reranker.GetModel(r.cfg.Agents.Default.Model),
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

// Helper functions

func extractJSONFromResponse(content string) string {
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
		return content
	}

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

	return content
}

func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

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

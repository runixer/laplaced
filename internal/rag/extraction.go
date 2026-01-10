package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/enricher"
	"github.com/runixer/laplaced/internal/agent/prompts"
	"github.com/runixer/laplaced/internal/agent/splitter"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

type ExtractedTopic struct {
	Summary    string `json:"summary"`
	StartMsgID int64  `json:"start_msg_id"`
	EndMsgID   int64  `json:"end_msg_id"`
}

func (s *Service) extractTopics(ctx context.Context, userID int64, chunk []storage.Message) ([]ExtractedTopic, UsageInfo, error) {
	// Use splitter agent if available
	if s.splitterAgent != nil {
		return s.extractTopicsViaAgent(ctx, userID, chunk)
	}
	return s.extractTopicsLegacy(ctx, userID, chunk)
}

// extractTopicsViaAgent delegates topic extraction to the Splitter agent.
func (s *Service) extractTopicsViaAgent(ctx context.Context, userID int64, chunk []storage.Message) ([]ExtractedTopic, UsageInfo, error) {
	req := &agent.Request{
		Params: map[string]any{
			splitter.ParamMessages: chunk,
			"user_id":              userID,
		},
	}

	// Try to get SharedContext from ctx (will be nil for background jobs)
	if shared := agent.FromContext(ctx); shared != nil {
		req.Shared = shared
	}

	resp, err := s.splitterAgent.Execute(ctx, req)
	if err != nil {
		return nil, UsageInfo{}, err
	}

	result, ok := resp.Structured.(*splitter.Result)
	if !ok {
		return nil, UsageInfo{}, fmt.Errorf("unexpected result type from splitter agent")
	}

	// Convert splitter.ExtractedTopic to rag.ExtractedTopic
	topics := make([]ExtractedTopic, len(result.Topics))
	for i, t := range result.Topics {
		topics[i] = ExtractedTopic{
			Summary:    t.Summary,
			StartMsgID: t.StartMsgID,
			EndMsgID:   t.EndMsgID,
		}
	}

	usage := UsageInfo{
		PromptTokens:     resp.Tokens.Prompt,
		CompletionTokens: resp.Tokens.Completion,
		TotalTokens:      resp.Tokens.Total,
		Cost:             resp.Tokens.Cost,
	}

	return topics, usage, nil
}

// extractTopicsLegacy is the original implementation for backwards compatibility.
func (s *Service) extractTopicsLegacy(ctx context.Context, userID int64, chunk []storage.Message) ([]ExtractedTopic, UsageInfo, error) {
	startTime := time.Now()

	// Load user profile for context (unified format with tags)
	allFacts, err := s.factRepo.GetFacts(userID)
	var profile string
	if err == nil {
		profile = FormatUserProfile(FilterProfileFacts(allFacts))
	} else {
		s.logger.Warn("failed to load facts for splitter", "error", err)
		profile = FormatUserProfile(nil)
	}

	// Load recent topics for context
	var recentTopics string
	recentTopicsCount := s.cfg.RAG.GetRecentTopicsInContext()
	if recentTopicsCount > 0 {
		topics, err := s.GetRecentTopics(userID, recentTopicsCount)
		if err != nil {
			s.logger.Warn("failed to get recent topics for splitter", "error", err)
		}
		recentTopics = FormatRecentTopics(topics)
	} else {
		recentTopics = FormatRecentTopics(nil)
	}

	// Prepare JSON
	type MsgItem struct {
		ID      int64  `json:"id"`
		Role    string `json:"role"`
		Content string `json:"content"`
		Date    string `json:"date"`
	}
	var items []MsgItem
	for _, m := range chunk {
		items = append(items, MsgItem{
			ID:      m.ID,
			Role:    m.Role,
			Content: m.Content,
			Date:    m.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	itemsBytes, _ := json.Marshal(items)

	// Build system prompt with profile and recent topics (empty goal for regular mode)
	systemPrompt, err := s.translator.GetTemplate(s.cfg.Bot.Language, "rag.topic_extraction_prompt", prompts.SplitterParams{
		Profile:      profile,
		RecentTopics: recentTopics,
		Goal:         "", // empty goal for regular mode
	})
	if err != nil {
		return nil, UsageInfo{}, fmt.Errorf("failed to build splitter system prompt: %w", err)
	}

	// User message is just the chat log
	userMessage := fmt.Sprintf("Chat Log JSON:\n%s", string(itemsBytes))

	model := s.cfg.Agents.Archivist.GetModel(s.cfg.Agents.Default.Model)

	// Define JSON Schema for Structured Outputs
	schema := map[string]interface{}{
		"type": "json_schema",
		"json_schema": map[string]interface{}{
			"name":   "topic_extraction",
			"strict": true,
			"schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"topics": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"summary": map[string]interface{}{
									"type":        "string",
									"description": "Подробное описание темы обсуждения на русском языке. Сохраняй названия брендов и ПО на английском.",
								},
								"start_msg_id": map[string]interface{}{
									"type":        "integer",
									"description": "ID первого сообщения в теме.",
								},
								"end_msg_id": map[string]interface{}{
									"type":        "integer",
									"description": "ID последнего сообщения в теме.",
								},
							},
							"required":             []string{"summary", "start_msg_id", "end_msg_id"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"topics"},
				"additionalProperties": false,
			},
		},
	}

	resp, err := s.client.CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
		Model: model,
		Messages: []openrouter.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
		ResponseFormat: schema,
		UserID:         userID,
	})
	if err != nil {
		durationMs := int(time.Since(startTime).Milliseconds())
		if s.agentLogger != nil {
			s.agentLogger.LogError(ctx, userID, agentlog.AgentSplitter, systemPrompt, nil, err.Error(), model, durationMs, nil)
		}
		return nil, UsageInfo{}, err
	}

	usage := UsageInfo{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		Cost:             resp.Usage.Cost,
	}

	if len(resp.Choices) == 0 {
		return nil, usage, fmt.Errorf("empty response")
	}

	content := resp.Choices[0].Message.Content
	duration := time.Since(startTime)

	// Log to agent_logs for debugging
	if s.agentLogger != nil {
		s.agentLogger.Log(ctx, agentlog.Entry{
			UserID:           userID,
			AgentType:        agentlog.AgentSplitter,
			InputPrompt:      systemPrompt, // System prompt for display
			InputContext:     resp.DebugRequestBody,
			OutputResponse:   content,
			OutputContext:    resp.DebugResponseBody,
			Model:            model,
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalCost:        resp.Usage.Cost,
			DurationMs:       int(duration.Milliseconds()),
			Success:          true,
		})
	}

	var result struct {
		Topics []ExtractedTopic `json:"topics"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, usage, fmt.Errorf("json parse error: %w. Content: %s", err, content)
	}

	return result.Topics, usage, nil
}

func (s *Service) enrichQuery(ctx context.Context, userID int64, query string, history []storage.Message, mediaParts []interface{}) (string, string, int, error) {
	// Use enricher agent if available
	if s.enricherAgent != nil {
		return s.enrichQueryViaAgent(ctx, userID, query, history, mediaParts)
	}
	return s.enrichQueryLegacy(ctx, userID, query, history, mediaParts)
}

// enrichQueryViaAgent delegates query enrichment to the Enricher agent.
func (s *Service) enrichQueryViaAgent(ctx context.Context, userID int64, query string, history []storage.Message, mediaParts []interface{}) (string, string, int, error) {
	model := s.cfg.Agents.Enricher.GetModel(s.cfg.Agents.Default.Model)
	if model == "" {
		return query, "", 0, nil
	}

	req := &agent.Request{
		Query: query,
		Params: map[string]any{
			enricher.ParamHistory: history,
		},
	}

	// Add media parts if present
	if len(mediaParts) > 0 {
		req.Params[enricher.ParamMediaParts] = mediaParts
	}

	// Try to get SharedContext from ctx
	if shared := agent.FromContext(ctx); shared != nil {
		req.Shared = shared
	}

	resp, err := s.enricherAgent.Execute(ctx, req)
	if err != nil {
		return "", "", 0, err
	}

	tokens := resp.Tokens.Prompt + resp.Tokens.Completion
	enrichedQuery := strings.TrimSpace(resp.Content)

	// Get system prompt from metadata for debugging
	systemPrompt := ""
	if sp, ok := resp.Metadata["system_prompt"].(string); ok {
		systemPrompt = sp
	}

	return enrichedQuery, systemPrompt, tokens, nil
}

// enrichQueryLegacy is the original implementation for backwards compatibility.
func (s *Service) enrichQueryLegacy(ctx context.Context, userID int64, query string, history []storage.Message, mediaParts []interface{}) (string, string, int, error) {
	startTime := time.Now()

	model := s.cfg.Agents.Enricher.GetModel(s.cfg.Agents.Default.Model)
	if model == "" {
		return query, "", 0, nil
	}

	// Load user profile for context (unified format with tags)
	allFacts, err := s.factRepo.GetFacts(userID)
	var profile string
	if err == nil {
		profile = FormatUserProfile(FilterProfileFacts(allFacts))
	} else {
		s.logger.Warn("failed to load facts for enricher", "error", err)
		profile = FormatUserProfile(nil)
	}

	// Load recent topics for context
	var recentTopics string
	recentTopicsCount := s.cfg.RAG.GetRecentTopicsInContext()
	if recentTopicsCount > 0 {
		topics, err := s.GetRecentTopics(userID, recentTopicsCount)
		if err != nil {
			s.logger.Warn("failed to get recent topics for enricher", "error", err)
		}
		recentTopics = FormatRecentTopics(topics)
	} else {
		recentTopics = FormatRecentTopics(nil)
	}

	var historyStr strings.Builder
	for _, msg := range history {
		content := msg.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		content = strings.ReplaceAll(content, "\n", " ")
		historyStr.WriteString(fmt.Sprintf("- [%s]: %s\n", msg.Role, content))
	}

	// Build system prompt with date, profile and recent topics
	systemPrompt, err := s.translator.GetTemplate(s.cfg.Bot.Language, "rag.enrichment_system_prompt", prompts.EnricherParams{
		Date:         time.Now().Format("2006-01-02"),
		Profile:      profile,
		RecentTopics: recentTopics,
	})
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to build enricher system prompt: %w", err)
	}

	// Build user message with history and query
	userMessage, err := s.translator.GetTemplate(s.cfg.Bot.Language, "rag.enrichment_user_prompt", prompts.EnricherUserParams{
		History: historyStr.String(),
		Query:   query,
	})
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to build enricher user prompt: %w", err)
	}

	// Build user message content - multimodal if mediaParts provided
	var userContent interface{}
	if len(mediaParts) > 0 {
		// Add media description instruction for multimodal queries
		mediaInstruction := s.translator.Get(s.cfg.Bot.Language, "rag.enrichment_media_instruction")
		if mediaInstruction == "" {
			mediaInstruction = "If the user's message contains an image or audio, include relevant visual/audio details in your search query (objects, people, places, spoken content)."
		}

		// Build multimodal content: text prompt + media parts
		parts := []interface{}{
			openrouter.TextPart{Type: "text", Text: userMessage + "\n\n" + mediaInstruction},
		}
		parts = append(parts, mediaParts...)
		userContent = parts
	} else {
		userContent = userMessage
	}

	resp, err := s.client.CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
		Model: model,
		Messages: []openrouter.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userContent},
		},
		UserID: userID,
	})
	durationMs := int(time.Since(startTime).Milliseconds())

	if err != nil {
		if s.agentLogger != nil {
			s.agentLogger.LogError(ctx, userID, agentlog.AgentEnricher, systemPrompt, nil, err.Error(), model, durationMs, nil)
		}
		return "", systemPrompt, 0, err
	}
	if len(resp.Choices) == 0 {
		if s.agentLogger != nil {
			s.agentLogger.LogError(ctx, userID, agentlog.AgentEnricher, systemPrompt, resp.DebugRequestBody, "empty response", model, durationMs, nil)
		}
		return "", systemPrompt, 0, fmt.Errorf("empty response")
	}

	tokens := resp.Usage.PromptTokens + resp.Usage.CompletionTokens
	enrichedQuery := strings.TrimSpace(resp.Choices[0].Message.Content)

	// Log success
	if s.agentLogger != nil {
		s.agentLogger.LogSuccess(ctx, userID, agentlog.AgentEnricher, systemPrompt, resp.DebugRequestBody,
			enrichedQuery, nil, resp.DebugResponseBody, model,
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.Cost, durationMs,
			map[string]interface{}{
				"original_query":  query,
				"enriched_query":  enrichedQuery,
				"query_expansion": len(enrichedQuery) - len(query),
			})
	}

	return enrichedQuery, systemPrompt, tokens, nil
}

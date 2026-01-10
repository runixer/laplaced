// Package splitter provides the Splitter agent that segments conversation logs
// into distinct topics for storage and retrieval.
package splitter

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/prompts"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// Request parameters for Splitter agent.
const (
	// ParamMessages is the key for messages to process ([]storage.Message).
	ParamMessages = "messages"
	// ParamGoal is the key for optional goal text (string).
	ParamGoal = "goal"
)

// ExtractedTopic represents a topic extracted from a conversation chunk.
type ExtractedTopic struct {
	Summary    string `json:"summary"`
	StartMsgID int64  `json:"start_msg_id"`
	EndMsgID   int64  `json:"end_msg_id"`
}

// Result contains the extracted topics.
type Result struct {
	Topics []ExtractedTopic
}

// Splitter segments conversation logs into distinct topics.
type Splitter struct {
	executor   *agent.Executor
	translator *i18n.Translator
	cfg        *config.Config

	// Data loaders for background jobs (when SharedContext is not available)
	factRepo  storage.FactRepository
	topicRepo storage.TopicRepository
}

// New creates a new Splitter agent.
func New(
	executor *agent.Executor,
	translator *i18n.Translator,
	cfg *config.Config,
	factRepo storage.FactRepository,
	topicRepo storage.TopicRepository,
) *Splitter {
	return &Splitter{
		executor:   executor,
		translator: translator,
		cfg:        cfg,
		factRepo:   factRepo,
		topicRepo:  topicRepo,
	}
}

// Type returns the agent type.
func (s *Splitter) Type() agent.AgentType {
	return agent.TypeSplitter
}

// Execute runs the splitter with the given request.
func (s *Splitter) Execute(ctx context.Context, req *agent.Request) (*agent.Response, error) {
	messages := s.getMessages(req)
	if len(messages) == 0 {
		return &agent.Response{
			Structured: &Result{Topics: nil},
		}, nil
	}

	model := s.cfg.Agents.Archivist.GetModel(s.cfg.Agents.Default.Model)
	if model == "" {
		return nil, fmt.Errorf("no model configured for splitter")
	}

	// Get profile and recent topics
	userID := s.getUserID(req)
	profile, recentTopics := s.getContext(ctx, req, userID)

	// Get optional goal parameter
	goal := ""
	if req.Params != nil {
		if g, ok := req.Params[ParamGoal].(string); ok {
			goal = g
		}
	}

	// Build system prompt
	systemPrompt, err := s.translator.GetTemplate(s.cfg.Bot.Language, "rag.topic_extraction_prompt", prompts.SplitterParams{
		Profile:      profile,
		RecentTopics: recentTopics,
		Goal:         goal,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build system prompt: %w", err)
	}

	// Prepare messages as JSON
	userMessage := s.buildUserMessage(messages)

	// Define JSON Schema for structured output
	schema := s.buildJSONSchema()

	// Make LLM call using executor's client directly (for ResponseFormat support)
	start := time.Now()
	resp, err := s.executor.Client().CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
		Model: model,
		Messages: []openrouter.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
		ResponseFormat: schema,
		UserID:         userID,
	})
	duration := time.Since(start)

	if err != nil {
		return nil, fmt.Errorf("llm call failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("empty response from LLM")
	}

	content := resp.Choices[0].Message.Content

	// Parse JSON response
	var result struct {
		Topics []ExtractedTopic `json:"topics"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("json parse error: %w, content: %s", err, content)
	}

	return &agent.Response{
		Content:    content,
		Structured: &Result{Topics: result.Topics},
		Duration:   duration,
		Tokens: agent.TokenUsage{
			Prompt:     resp.Usage.PromptTokens,
			Completion: resp.Usage.CompletionTokens,
			Total:      resp.Usage.TotalTokens,
			Cost:       resp.Usage.Cost,
		},
	}, nil
}

// Capabilities returns the agent's capabilities.
func (s *Splitter) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		IsAgentic:    false,
		OutputFormat: "json",
	}
}

// Description returns a human-readable description.
func (s *Splitter) Description() string {
	return "Segments conversation logs into distinct topics"
}

// getMessages extracts messages from request params.
func (s *Splitter) getMessages(req *agent.Request) []storage.Message {
	if req.Params == nil {
		return nil
	}
	if msgs, ok := req.Params[ParamMessages].([]storage.Message); ok {
		return msgs
	}
	return nil
}

// getUserID extracts user ID from request.
func (s *Splitter) getUserID(req *agent.Request) int64 {
	if req.Shared != nil {
		return req.Shared.UserID
	}
	// Try to get from params for background jobs
	if req.Params != nil {
		if userID, ok := req.Params["user_id"].(int64); ok {
			return userID
		}
	}
	return 0
}

// getContext returns profile and recent topics.
func (s *Splitter) getContext(ctx context.Context, req *agent.Request, userID int64) (profile, recentTopics string) {
	// Try SharedContext first
	if req.Shared != nil {
		return req.Shared.Profile, req.Shared.RecentTopics
	}
	if shared := agent.FromContext(ctx); shared != nil {
		return shared.Profile, shared.RecentTopics
	}

	// Fallback: load directly for background jobs
	if s.factRepo != nil && userID > 0 {
		facts, err := s.factRepo.GetFacts(userID)
		if err == nil {
			profile = storage.FormatUserProfile(storage.FilterProfileFacts(facts))
		}
	}

	if s.topicRepo != nil && userID > 0 {
		recentTopicsCount := s.cfg.RAG.GetRecentTopicsInContext()
		if recentTopicsCount > 0 {
			filter := storage.TopicFilter{UserID: userID}
			result, err := s.topicRepo.GetTopicsExtended(filter, recentTopicsCount, 0, "created_at", "DESC")
			if err == nil {
				recentTopics = storage.FormatRecentTopics(result.Data)
			}
		}
	}

	return profile, recentTopics
}

// buildUserMessage creates the user message with messages as JSON.
func (s *Splitter) buildUserMessage(messages []storage.Message) string {
	type MsgItem struct {
		ID      int64  `json:"id"`
		Role    string `json:"role"`
		Content string `json:"content"`
		Date    string `json:"date"`
	}

	items := make([]MsgItem, len(messages))
	for i, m := range messages {
		items[i] = MsgItem{
			ID:      m.ID,
			Role:    m.Role,
			Content: m.Content,
			Date:    m.CreatedAt.Format("2006-01-02 15:04:05"),
		}
	}

	itemsBytes, _ := json.Marshal(items)
	return fmt.Sprintf("Chat Log JSON:\n%s", string(itemsBytes))
}

// buildJSONSchema creates the JSON schema for structured output.
func (s *Splitter) buildJSONSchema() map[string]interface{} {
	return map[string]interface{}{
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
}

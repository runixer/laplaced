// Package merger provides the Merger agent that evaluates whether two topics
// should be merged and generates a combined summary.
package merger

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

// Request parameters for Merger agent.
const (
	// ParamTopic1Summary is the key for first topic summary (string).
	ParamTopic1Summary = "topic1_summary"
	// ParamTopic2Summary is the key for second topic summary (string).
	ParamTopic2Summary = "topic2_summary"
)

// Result contains the merge decision.
type Result struct {
	ShouldMerge bool   `json:"should_merge"`
	NewSummary  string `json:"new_summary"`
}

// Merger evaluates whether two topics should be merged.
type Merger struct {
	executor   *agent.Executor
	translator *i18n.Translator
	cfg        *config.Config

	// Data loaders for background jobs (when SharedContext is not available)
	factRepo  storage.FactRepository
	topicRepo storage.TopicRepository
}

// New creates a new Merger agent.
func New(
	executor *agent.Executor,
	translator *i18n.Translator,
	cfg *config.Config,
	factRepo storage.FactRepository,
	topicRepo storage.TopicRepository,
) *Merger {
	return &Merger{
		executor:   executor,
		translator: translator,
		cfg:        cfg,
		factRepo:   factRepo,
		topicRepo:  topicRepo,
	}
}

// Type returns the agent type.
func (m *Merger) Type() agent.AgentType {
	return agent.TypeMerger
}

// Execute runs the merger with the given request.
func (m *Merger) Execute(ctx context.Context, req *agent.Request) (*agent.Response, error) {
	// Extract topic summaries
	topic1Summary, topic2Summary := m.getTopicSummaries(req)
	if topic1Summary == "" || topic2Summary == "" {
		return nil, fmt.Errorf("both topic summaries are required")
	}

	model := m.cfg.Agents.Merger.GetModel(m.cfg.Agents.Default.Model)
	if model == "" {
		return nil, fmt.Errorf("no model configured for merger")
	}

	// Get profile and recent topics
	userID := m.getUserID(req)
	profile, recentTopics := m.getContext(ctx, req, userID)

	// Build system prompt
	systemPrompt, err := m.translator.GetTemplate(m.cfg.Bot.Language, "rag.topic_consolidation_system_prompt", prompts.MergerParams{
		Profile:      profile,
		RecentTopics: recentTopics,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build system prompt: %w", err)
	}

	// Build user prompt
	userPrompt, err := m.translator.GetTemplate(m.cfg.Bot.Language, "rag.topic_consolidation_user_prompt", prompts.MergerUserParams{
		Topic1Summary: topic1Summary,
		Topic2Summary: topic2Summary,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build user prompt: %w", err)
	}

	// Make LLM call
	start := time.Now()
	resp, err := m.executor.Client().CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
		Model: model,
		Messages: []openrouter.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		ResponseFormat: map[string]interface{}{"type": "json_object"},
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
	var result Result
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("json parse error: %w, content: %s", err, content)
	}

	return &agent.Response{
		Content:    content,
		Structured: &result,
		Duration:   duration,
		Tokens: agent.TokenUsage{
			Prompt:     resp.Usage.PromptTokens,
			Completion: resp.Usage.CompletionTokens,
			Total:      resp.Usage.TotalTokens,
			Cost:       resp.Usage.Cost,
		},
		Metadata: map[string]any{
			"should_merge": result.ShouldMerge,
		},
	}, nil
}

// Capabilities returns the agent's capabilities.
func (m *Merger) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		IsAgentic:    false,
		OutputFormat: "json",
	}
}

// Description returns a human-readable description.
func (m *Merger) Description() string {
	return "Evaluates whether two topics should be merged"
}

// getTopicSummaries extracts topic summaries from request params.
func (m *Merger) getTopicSummaries(req *agent.Request) (string, string) {
	if req.Params == nil {
		return "", ""
	}
	topic1, _ := req.Params[ParamTopic1Summary].(string)
	topic2, _ := req.Params[ParamTopic2Summary].(string)
	return topic1, topic2
}

// getUserID extracts user ID from request.
func (m *Merger) getUserID(req *agent.Request) int64 {
	if req.Shared != nil {
		return req.Shared.UserID
	}
	if req.Params != nil {
		if userID, ok := req.Params["user_id"].(int64); ok {
			return userID
		}
	}
	return 0
}

// getContext returns profile and recent topics.
func (m *Merger) getContext(ctx context.Context, req *agent.Request, userID int64) (profile, recentTopics string) {
	// Try SharedContext first
	if req.Shared != nil {
		return req.Shared.Profile, req.Shared.RecentTopics
	}
	if shared := agent.FromContext(ctx); shared != nil {
		return shared.Profile, shared.RecentTopics
	}

	// Fallback: load directly for background jobs
	if m.factRepo != nil && userID > 0 {
		facts, err := m.factRepo.GetFacts(userID)
		if err == nil {
			profile = storage.FormatUserProfile(storage.FilterProfileFacts(facts))
		}
	}

	if m.topicRepo != nil && userID > 0 {
		recentTopicsCount := m.cfg.RAG.GetRecentTopicsInContext()
		if recentTopicsCount > 0 {
			filter := storage.TopicFilter{UserID: userID}
			result, err := m.topicRepo.GetTopicsExtended(filter, recentTopicsCount, 0, "created_at", "DESC")
			if err == nil {
				recentTopics = storage.FormatRecentTopics(result.Data)
			}
		}
	}

	return profile, recentTopics
}

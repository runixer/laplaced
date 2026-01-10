// Package enricher provides the Enricher agent that expands user queries
// for better vector retrieval in the RAG pipeline.
package enricher

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/prompts"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// Request parameters for Enricher agent.
const (
	// ParamHistory is the key for conversation history ([]storage.Message).
	ParamHistory = "history"
	// ParamMediaParts is the key for multimodal content ([]interface{}).
	ParamMediaParts = "media_parts"
)

// Enricher analyzes user queries and formulates search queries
// for vector retrieval of relevant conversation history.
type Enricher struct {
	executor   *agent.Executor
	translator *i18n.Translator
	cfg        *config.Config
}

// New creates a new Enricher agent.
func New(
	executor *agent.Executor,
	translator *i18n.Translator,
	cfg *config.Config,
) *Enricher {
	return &Enricher{
		executor:   executor,
		translator: translator,
		cfg:        cfg,
	}
}

// Type returns the agent type.
func (e *Enricher) Type() agent.AgentType {
	return agent.TypeEnricher
}

// Execute runs the enricher with the given request.
func (e *Enricher) Execute(ctx context.Context, req *agent.Request) (*agent.Response, error) {
	model := e.cfg.Agents.Enricher.GetModel(e.cfg.Agents.Default.Model)
	if model == "" {
		// No model configured, return original query
		return &agent.Response{
			Content: req.Query,
		}, nil
	}

	// Get profile and recent topics from SharedContext or load directly
	profile, recentTopics := e.getContext(ctx, req)

	// Build system prompt
	systemPrompt, err := e.translator.GetTemplate(e.cfg.Bot.Language, "rag.enrichment_system_prompt", prompts.EnricherParams{
		Date:         time.Now().Format("2006-01-02"),
		Profile:      profile,
		RecentTopics: recentTopics,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build system prompt: %w", err)
	}

	// Format conversation history
	history := e.getHistory(req)
	var historyStr strings.Builder
	for _, msg := range history {
		content := msg.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		content = strings.ReplaceAll(content, "\n", " ")
		historyStr.WriteString(fmt.Sprintf("- [%s]: %s\n", msg.Role, content))
	}

	// Build user prompt
	userPrompt, err := e.translator.GetTemplate(e.cfg.Bot.Language, "rag.enrichment_user_prompt", prompts.EnricherUserParams{
		History: historyStr.String(),
		Query:   req.Query,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build user prompt: %w", err)
	}

	// Build messages with optional multimodal content
	messages := e.buildMessages(systemPrompt, userPrompt, req)

	// Execute LLM call
	userID := int64(0)
	if req.Shared != nil {
		userID = req.Shared.UserID
	}

	resp, err := e.executor.ExecuteSingleShot(ctx, agent.SingleShotRequest{
		AgentType: agent.TypeEnricher,
		UserID:    userID,
		Model:     model,
		Messages:  messages,
	})
	if err != nil {
		return nil, err
	}

	// Clean up response
	resp.Content = strings.TrimSpace(resp.Content)

	// Add metadata
	if resp.Metadata == nil {
		resp.Metadata = make(map[string]any)
	}
	resp.Metadata["original_query"] = req.Query
	resp.Metadata["query_expansion"] = len(resp.Content) - len(req.Query)

	return resp, nil
}

// Capabilities returns the agent's capabilities.
func (e *Enricher) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		IsAgentic:      false,
		SupportedMedia: []string{"image", "audio"},
		OutputFormat:   "text",
	}
}

// Description returns a human-readable description.
func (e *Enricher) Description() string {
	return "Expands user queries with context for better memory retrieval"
}

// getContext returns profile and recent topics from SharedContext.
// Returns empty strings if SharedContext is not available.
func (e *Enricher) getContext(ctx context.Context, req *agent.Request) (profile, recentTopics string) {
	// Try SharedContext from request first
	if req.Shared != nil {
		return req.Shared.Profile, req.Shared.RecentTopics
	}

	// Also check context.Context for SharedContext
	if shared := agent.FromContext(ctx); shared != nil {
		return shared.Profile, shared.RecentTopics
	}

	// No SharedContext available - return empty (for tests)
	return "", ""
}

// getHistory extracts conversation history from request params.
func (e *Enricher) getHistory(req *agent.Request) []storage.Message {
	if req.Params == nil {
		return nil
	}
	if history, ok := req.Params[ParamHistory].([]storage.Message); ok {
		return history
	}
	return nil
}

// buildMessages constructs OpenRouter messages, handling multimodal content.
func (e *Enricher) buildMessages(systemPrompt, userPrompt string, req *agent.Request) []openrouter.Message {
	messages := []openrouter.Message{
		{Role: "system", Content: systemPrompt},
	}

	// Check for multimodal content
	var mediaParts []interface{}
	if req.Params != nil {
		if parts, ok := req.Params[ParamMediaParts].([]interface{}); ok {
			mediaParts = parts
		}
	}

	if len(mediaParts) > 0 {
		// Add media description instruction
		mediaInstruction := e.translator.Get(e.cfg.Bot.Language, "rag.enrichment_media_instruction")
		if mediaInstruction == "" {
			mediaInstruction = "If the user's message contains an image or audio, include relevant visual/audio details in your search query (objects, people, places, spoken content)."
		}

		// Build multimodal content
		parts := []interface{}{
			openrouter.TextPart{Type: "text", Text: userPrompt + "\n\n" + mediaInstruction},
		}
		parts = append(parts, mediaParts...)
		messages = append(messages, openrouter.Message{Role: "user", Content: parts})
	} else {
		messages = append(messages, openrouter.Message{Role: "user", Content: userPrompt})
	}

	return messages
}

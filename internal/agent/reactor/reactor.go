// Package reactor provides the Reactor agent that decides whether to add an
// emoji reaction to a user message, and which one. It picks from a
// transport-specific allowed list passed by the caller; an empty decision
// means "do not react".
package reactor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/prompts"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/storage"
)

// Request parameters for the Reactor agent.
const (
	// ParamAllowedReactions is the key for the transport's reaction tokens ([]string).
	ParamAllowedReactions = "allowed_reactions"
	// ParamHistory is the key for conversation history ([]storage.Message).
	ParamHistory = "history"
	// ParamMediaParts is the key for multimodal content ([]interface{}).
	ParamMediaParts = "media_parts"
)

// Result is the parsed, validated decision. Emoji is a token from the allowed
// list, or "" meaning "do not react".
type Result struct {
	Emoji string `json:"emoji"`
}

// Reactor decides emoji reactions to user messages.
type Reactor struct {
	executor   *agent.Executor
	translator *i18n.Translator
	cfg        *config.Config
}

// New creates a new Reactor agent.
func New(
	executor *agent.Executor,
	translator *i18n.Translator,
	cfg *config.Config,
) *Reactor {
	return &Reactor{
		executor:   executor,
		translator: translator,
		cfg:        cfg,
	}
}

// Type returns the agent type.
func (r *Reactor) Type() agent.AgentType {
	return agent.TypeReactor
}

// Capabilities returns the agent's capabilities.
func (r *Reactor) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		IsAgentic:      false,
		SupportedMedia: []string{"image", "audio"},
		OutputFormat:   "json",
	}
}

// Description returns a human-readable description.
func (r *Reactor) Description() string {
	return "Decides whether to react to a user message with an emoji, and which one"
}

// Execute runs the reactor with the given request. It never fails the turn on
// a malformed model answer — that degrades to "no reaction" (Result.Emoji "").
func (r *Reactor) Execute(ctx context.Context, req *agent.Request) (*agent.Response, error) {
	noReact := &agent.Response{Structured: &Result{}}

	model := r.cfg.Agents.Reactor.GetModel(r.cfg.Agents.Default.Model)
	if model == "" {
		return noReact, nil
	}
	allowed := r.getAllowedReactions(req)
	if len(allowed) == 0 {
		return noReact, nil
	}

	var userID storage.ScopeID
	if req.Shared != nil {
		userID = req.Shared.UserID
	}
	mediaParts := r.getMediaParts(req)

	// Span boundary so the child llm span lands under reactor.Execute and
	// decisions become queryable via `{ span.reactor.reacted = true }`.
	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/agent/reactor").Start(
		ctx, "reactor.Execute",
		trace.WithAttributes(
			attribute.String("user.id", string(userID)),
			attribute.Int("reactor.allowed_count", len(allowed)),
			attribute.Int("reactor.media_count", len(mediaParts)),
		),
	)
	var (
		emoji        string
		parseError   bool
		jsonRepaired bool
	)
	defer func() {
		span.SetAttributes(
			attribute.Bool("reactor.reacted", emoji != ""),
			attribute.String("reactor.emoji", emoji),
			attribute.Bool("reactor.parse_error", parseError),
			attribute.Bool("reactor.json_repaired", jsonRepaired),
		)
		span.End()
	}()
	if obs.ContentEnabled() && len(mediaParts) > 0 {
		if body := agent.FormatMediaParts(mediaParts, ""); body != "" {
			obs.RecordContent(span, "reactor.media_parts", body)
		}
	}

	profile, _, _ := agent.GetSharedContext(ctx, req)

	systemPrompt, err := r.translator.GetTemplate(r.cfg.Bot.Language, "reactor.system_prompt", prompts.ReactorParams{
		Date:      time.Now().Format("2006-01-02"),
		Profile:   profile,
		Reactions: strings.Join(allowed, " "),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build system prompt: %w", err)
	}

	userPrompt, err := r.translator.GetTemplate(r.cfg.Bot.Language, "reactor.user_prompt", prompts.ReactorUserParams{
		History: r.formatHistory(req),
		Message: req.Query,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build user prompt: %w", err)
	}

	messages := r.buildMessages(systemPrompt, userPrompt, mediaParts)

	resp, err := r.executor.ExecuteSingleShot(ctx, agent.SingleShotRequest{
		AgentType: agent.TypeReactor,
		UserID:    userID,
		Model:     model,
		Messages:  messages,
		JSONMode:  true,
	})
	if err != nil {
		return nil, err
	}

	var result Result
	repaired, uerr := agent.UnmarshalLenient(resp.Content, &result)
	jsonRepaired = repaired
	if uerr != nil {
		// Malformed decision → skip the reaction, don't fail the turn.
		parseError = true
		resp.Structured = &Result{}
		return resp, nil
	}

	emoji = ValidateEmoji(result.Emoji, allowed)
	resp.Structured = &Result{Emoji: emoji}
	if resp.Metadata == nil {
		resp.Metadata = make(map[string]any)
	}
	resp.Metadata["emoji"] = emoji
	return resp, nil
}

// ValidateEmoji maps a model-proposed token onto the allowed list, returning
// the allowed-list form verbatim, or "" for "none"/unknown tokens. Comparison
// ignores U+FE0F variation selectors (models tend to emit "❤️" where the
// Telegram API wants "❤") and surrounding ":" (shortcode echo).
func ValidateEmoji(candidate string, allowed []string) string {
	c := strings.TrimSpace(candidate)
	c = strings.Trim(c, ":")
	if c == "" || strings.EqualFold(c, "none") {
		return ""
	}
	c = stripVariationSelectors(c)
	for _, a := range allowed {
		if stripVariationSelectors(a) == c {
			return a
		}
	}
	return ""
}

func stripVariationSelectors(s string) string {
	return strings.ReplaceAll(s, "\ufe0f", "")
}

// getAllowedReactions extracts the transport's reaction tokens from request params.
func (r *Reactor) getAllowedReactions(req *agent.Request) []string {
	if req.Params == nil {
		return nil
	}
	if allowed, ok := req.Params[ParamAllowedReactions].([]string); ok {
		return allowed
	}
	return nil
}

// getMediaParts extracts the multimodal content slice from request params.
func (r *Reactor) getMediaParts(req *agent.Request) []interface{} {
	if req.Params == nil {
		return nil
	}
	if parts, ok := req.Params[ParamMediaParts].([]interface{}); ok {
		return parts
	}
	return nil
}

// formatHistory renders recent conversation history as role-prefixed lines
// (same shape the enricher uses).
func (r *Reactor) formatHistory(req *agent.Request) string {
	if req.Params == nil {
		return ""
	}
	history, ok := req.Params[ParamHistory].([]storage.Message)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, msg := range history {
		content := msg.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		content = strings.ReplaceAll(content, "\n", " ")
		fmt.Fprintf(&sb, "- [%s]: %s\n", msg.Role, content)
	}
	return sb.String()
}

// buildMessages constructs LLM messages, handling multimodal content.
func (r *Reactor) buildMessages(systemPrompt, userPrompt string, mediaParts []interface{}) []llm.Message {
	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
	}
	if len(mediaParts) > 0 {
		parts := []interface{}{
			llm.TextPart{Type: "text", Text: userPrompt},
		}
		parts = append(parts, mediaParts...)
		messages = append(messages, llm.Message{Role: "user", Content: parts})
	} else {
		messages = append(messages, llm.Message{Role: "user", Content: userPrompt})
	}
	return messages
}

// Package archivist provides the Archivist agent that extracts and manages
// facts from conversations for long-term memory.
package archivist

import (
	"context"
	"encoding/json"
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

// Request parameters for Archivist agent.
const (
	// ParamMessages is the key for messages to process ([]storage.Message).
	ParamMessages = "messages"
	// ParamFacts is the key for current facts ([]storage.Fact).
	ParamFacts = "facts"
	// ParamReferenceDate is the key for the reference date (time.Time).
	ParamReferenceDate = "reference_date"
	// ParamUser is the key for user info (*storage.User, optional).
	ParamUser = "user"
)

// AddedFact represents a new fact to add.
type AddedFact struct {
	Entity     string `json:"entity"`
	Relation   string `json:"relation"`
	Content    string `json:"content"`
	Category   string `json:"category"`
	Type       string `json:"type"`
	Importance int    `json:"importance"`
	Reason     string `json:"reason"`
}

// UpdatedFact represents changes to an existing fact.
type UpdatedFact struct {
	ID         int64  `json:"id"`
	Content    string `json:"content"`
	Type       string `json:"type,omitempty"`
	Importance int    `json:"importance"`
	Reason     string `json:"reason"`
}

// RemovedFact represents a fact to be deleted.
type RemovedFact struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

// Result contains the memory update decisions.
type Result struct {
	Added   []AddedFact   `json:"added"`
	Updated []UpdatedFact `json:"updated"`
	Removed []RemovedFact `json:"removed"`
}

// Archivist extracts and manages facts from conversations.
type Archivist struct {
	executor   *agent.Executor
	translator *i18n.Translator
	cfg        *config.Config
}

// New creates a new Archivist agent.
func New(
	executor *agent.Executor,
	translator *i18n.Translator,
	cfg *config.Config,
) *Archivist {
	return &Archivist{
		executor:   executor,
		translator: translator,
		cfg:        cfg,
	}
}

// Type returns the agent type.
func (a *Archivist) Type() agent.AgentType {
	return agent.TypeArchivist
}

// Execute runs the archivist with the given request.
func (a *Archivist) Execute(ctx context.Context, req *agent.Request) (*agent.Response, error) {
	// Extract parameters
	messages := a.getMessages(req)
	if len(messages) == 0 {
		return &agent.Response{
			Structured: &Result{},
		}, nil
	}

	facts := a.getFacts(req)
	referenceDate := a.getReferenceDate(req)
	user := a.getUser(req)
	userID := a.getUserID(req)

	model := a.cfg.Agents.Archivist.GetModel(a.cfg.Agents.Default.Model)
	if model == "" {
		return nil, fmt.Errorf("no model configured for archivist")
	}

	// Build conversation text
	conversation := a.buildConversation(messages, user)

	// Prepare facts JSON
	userFacts, otherFacts := a.prepareFacts(facts)
	userFactsJSON, _ := json.MarshalIndent(userFacts, "", "  ")
	otherFactsJSON, _ := json.MarshalIndent(otherFacts, "", "  ")

	// Build system prompt
	maxFacts := a.cfg.RAG.MaxProfileFacts
	systemPrompt, err := a.translator.GetTemplate(a.cfg.Bot.Language, "memory.system_prompt", prompts.ArchivistParams{
		Date:            referenceDate.Format("2006-01-02"),
		UserFactsLimit:  maxFacts,
		UserFactsCount:  len(userFacts),
		OtherFactsCount: len(otherFacts),
		UserFacts:       string(userFactsJSON),
		OtherFacts:      string(otherFactsJSON),
		Conversation:    conversation,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build system prompt: %w", err)
	}

	// Add limit warning if needed
	if len(userFacts) > maxFacts {
		warning := fmt.Sprintf("\n\n<limit_exceeded>\nCRITICAL: You have %d facts about User. The limit is %d.\nYou MUST delete or consolidate at least %d facts in this response.\n</limit_exceeded>", len(userFacts), maxFacts, len(userFacts)-maxFacts+1)
		systemPrompt += warning
	}

	// Build JSON schema
	schema := a.buildJSONSchema()

	// Make LLM call
	start := time.Now()
	resp, err := a.executor.Client().CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
		Model: model,
		Messages: []openrouter.Message{
			{Role: "system", Content: systemPrompt},
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
			"added_count":   len(result.Added),
			"updated_count": len(result.Updated),
			"removed_count": len(result.Removed),
		},
	}, nil
}

// Capabilities returns the agent's capabilities.
func (a *Archivist) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		IsAgentic:    false,
		OutputFormat: "json",
	}
}

// Description returns a human-readable description.
func (a *Archivist) Description() string {
	return "Extracts and manages facts from conversations"
}

// getMessages extracts messages from request params.
func (a *Archivist) getMessages(req *agent.Request) []storage.Message {
	if req.Params == nil {
		return nil
	}
	if msgs, ok := req.Params[ParamMessages].([]storage.Message); ok {
		return msgs
	}
	return nil
}

// getFacts extracts facts from request params.
func (a *Archivist) getFacts(req *agent.Request) []storage.Fact {
	if req.Params == nil {
		return nil
	}
	if facts, ok := req.Params[ParamFacts].([]storage.Fact); ok {
		return facts
	}
	return nil
}

// getReferenceDate extracts reference date from request params.
func (a *Archivist) getReferenceDate(req *agent.Request) time.Time {
	if req.Params == nil {
		return time.Now()
	}
	if date, ok := req.Params[ParamReferenceDate].(time.Time); ok {
		return date
	}
	return time.Now()
}

// getUser extracts user from request params.
func (a *Archivist) getUser(req *agent.Request) *storage.User {
	if req.Params == nil {
		return nil
	}
	if user, ok := req.Params[ParamUser].(*storage.User); ok {
		return user
	}
	return nil
}

// getUserID extracts user ID from request.
func (a *Archivist) getUserID(req *agent.Request) int64 {
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

// buildConversation formats messages for the prompt.
func (a *Archivist) buildConversation(messages []storage.Message, user *storage.User) string {
	var sb strings.Builder
	for _, msg := range messages {
		if msg.Role == "user" {
			if strings.HasPrefix(msg.Content, "[") && strings.Contains(msg.Content, "]:") {
				sb.WriteString(fmt.Sprintf("%s\n", msg.Content))
			} else {
				name := a.formatUserName(user)
				dateStr := msg.CreatedAt.Format("2006-01-02 15:04:05")
				sb.WriteString(fmt.Sprintf("[%s (%s)]: %s\n", name, dateStr, msg.Content))
			}
		} else {
			dateStr := msg.CreatedAt.Format("2006-01-02 15:04:05")
			sb.WriteString(fmt.Sprintf("[Bot (%s)]: %s\n", dateStr, msg.Content))
		}
	}
	return sb.String()
}

// formatUserName builds a display name for the user.
func (a *Archivist) formatUserName(user *storage.User) string {
	if user == nil {
		return "User"
	}
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if user.Username != "" {
		if name != "" {
			name = fmt.Sprintf("%s (@%s)", name, user.Username)
		} else {
			name = "@" + user.Username
		}
	}
	if name == "" {
		name = fmt.Sprintf("ID:%d", user.ID)
	}
	return name
}

// FactView is a simplified view of a fact for the prompt.
type FactView struct {
	ID         int64  `json:"id"`
	Entity     string `json:"entity"`
	Relation   string `json:"relation"`
	Content    string `json:"content"`
	Category   string `json:"category"`
	Type       string `json:"type"`
	Importance int    `json:"importance"`
}

// prepareFacts separates facts into user and other facts.
func (a *Archivist) prepareFacts(facts []storage.Fact) (userFacts, otherFacts []FactView) {
	for _, f := range facts {
		view := FactView{
			ID:         f.ID,
			Entity:     f.Entity,
			Relation:   f.Relation,
			Content:    f.Content,
			Category:   f.Category,
			Type:       f.Type,
			Importance: f.Importance,
		}
		if strings.EqualFold(f.Entity, "User") {
			userFacts = append(userFacts, view)
		} else {
			otherFacts = append(otherFacts, view)
		}
	}
	return
}

// buildJSONSchema creates the JSON schema for structured output.
func (a *Archivist) buildJSONSchema() map[string]interface{} {
	factSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"entity":     map[string]interface{}{"type": "string"},
			"relation":   map[string]interface{}{"type": "string"},
			"content":    map[string]interface{}{"type": "string"},
			"category":   map[string]interface{}{"type": "string", "enum": []string{"bio", "work", "hobby", "preference", "other"}},
			"type":       map[string]interface{}{"type": "string", "enum": []string{"identity", "context", "status"}},
			"importance": map[string]interface{}{"type": "integer", "description": "0-100"},
			"reason":     map[string]interface{}{"type": "string"},
		},
		"required":             []string{"entity", "relation", "content", "category", "type", "importance", "reason"},
		"additionalProperties": false,
	}

	updateSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":         map[string]interface{}{"type": "integer"},
			"content":    map[string]interface{}{"type": "string"},
			"type":       map[string]interface{}{"type": "string", "enum": []string{"identity", "context", "status"}},
			"importance": map[string]interface{}{"type": "integer"},
			"reason":     map[string]interface{}{"type": "string"},
		},
		"required":             []string{"id", "content", "importance", "reason"},
		"additionalProperties": false,
	}

	removeSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id":     map[string]interface{}{"type": "integer"},
			"reason": map[string]interface{}{"type": "string"},
		},
		"required":             []string{"id", "reason"},
		"additionalProperties": false,
	}

	return map[string]interface{}{
		"type": "json_schema",
		"json_schema": map[string]interface{}{
			"name":   "memory_update",
			"strict": true,
			"schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"added": map[string]interface{}{
						"type":  "array",
						"items": factSchema,
					},
					"updated": map[string]interface{}{
						"type":  "array",
						"items": updateSchema,
					},
					"removed": map[string]interface{}{
						"type":  "array",
						"items": removeSchema,
					},
				},
				"required":             []string{"added", "updated", "removed"},
				"additionalProperties": false,
			},
		},
	}
}

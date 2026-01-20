// Package archivist provides the Archivist agent that extracts and manages
// facts and people from conversations for long-term memory.
package archivist

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/prompts"
	"github.com/runixer/laplaced/internal/agentlog"
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
	// ParamPeople is the key for known people names ([]storage.Person).
	ParamPeople = "people"
	// ParamReferenceDate is the key for the reference date (time.Time).
	ParamReferenceDate = "reference_date"
	// ParamUser is the key for user info (*storage.User, optional).
	ParamUser = "user"
)

// AddedFact represents a new fact to add.
type AddedFact struct {
	Relation   string `json:"relation"`
	Content    string `json:"content"`
	Category   string `json:"category"`
	Type       string `json:"type"`
	Importance int    `json:"importance"`
	Reason     string `json:"reason"`
}

// UpdatedFact represents changes to an existing fact.
type UpdatedFact struct {
	ID         int64  `json:"id,omitempty"`      // Legacy: numeric fallback with warning
	FactID     string `json:"fact_id,omitempty"` // Preferred: "Fact:1522" or "1522"
	Content    string `json:"content"`
	Type       string `json:"type,omitempty"`
	Importance int    `json:"importance"`
	Reason     string `json:"reason"`
}

// RemovedFact represents a fact to be deleted.
type RemovedFact struct {
	ID     int64  `json:"id,omitempty"`      // Legacy: numeric fallback with warning
	FactID string `json:"fact_id,omitempty"` // Preferred: "Fact:1234" or "1234"
	Reason string `json:"reason"`
}

// FactsResult contains fact operations.
type FactsResult struct {
	Added   []AddedFact   `json:"added"`
	Updated []UpdatedFact `json:"updated"`
	Removed []RemovedFact `json:"removed"`
}

// AddedPerson represents a new person to add.
type AddedPerson struct {
	DisplayName string   `json:"display_name"`
	Aliases     []string `json:"aliases,omitempty"`
	Circle      string   `json:"circle"`
	Bio         string   `json:"bio"`
	Reason      string   `json:"reason"`
}

// UpdatedPerson represents changes to an existing person.
type UpdatedPerson struct {
	DisplayName    string   `json:"display_name,omitempty"`     // Fallback with warning
	PersonID       string   `json:"person_id,omitempty"`        // Preferred: "Person:5"
	NewDisplayName string   `json:"new_display_name,omitempty"` // Rename person, old name goes to aliases
	Aliases        []string `json:"aliases,omitempty"`          // New aliases to add
	Circle         string   `json:"circle,omitempty"`
	Bio            string   `json:"bio"` // Complete rewritten bio
	Reason         string   `json:"reason"`
}

// MergedPerson represents a merge operation for duplicate people.
type MergedPerson struct {
	TargetName string `json:"target_name,omitempty"` // Fallback with warning
	SourceName string `json:"source_name,omitempty"` // Fallback with warning
	TargetID   string `json:"target_id,omitempty"`   // Preferred: "Person:10"
	SourceID   string `json:"source_id,omitempty"`   // Preferred: "Person:5"
	Reason     string `json:"reason"`
}

// PeopleResult contains people operations.
type PeopleResult struct {
	Added   []AddedPerson   `json:"added,omitempty"`
	Updated []UpdatedPerson `json:"updated,omitempty"`
	Merged  []MergedPerson  `json:"merged,omitempty"`
}

// Result contains all memory update decisions.
type Result struct {
	Facts  FactsResult  `json:"facts"`
	People PeopleResult `json:"people"`
}

// LegacyResult for backward compatibility with old format.
type LegacyResult struct {
	Added   []AddedFact   `json:"added"`
	Updated []UpdatedFact `json:"updated"`
	Removed []RemovedFact `json:"removed"`
}

// GetFactID extracts fact ID from UpdatedFact.
// Prefers "Fact:1522" or "1522" (string), falls back to numeric id.
// Returns 0 if no valid ID found.
func (f *UpdatedFact) GetFactID() (int64, error) {
	// New format: "Fact:1522" or "1522" (string)
	if f.FactID != "" {
		re := regexp.MustCompile(`^(?:Fact:)?(\d+)$`)
		matches := re.FindStringSubmatch(f.FactID)
		if len(matches) == 2 {
			return strconv.ParseInt(matches[1], 10, 64)
		}
		return 0, fmt.Errorf("invalid fact_id format: %s", f.FactID)
	}
	// Legacy format: numeric id
	if f.ID > 0 {
		return f.ID, nil
	}
	return 0, fmt.Errorf("no fact ID provided")
}

// GetFactID extracts fact ID from RemovedFact.
// Prefers "Fact:1234" or "1234" (string), falls back to numeric id.
// Returns 0 if no valid ID found.
func (f *RemovedFact) GetFactID() (int64, error) {
	// New format: "Fact:1234" or "1234" (string)
	if f.FactID != "" {
		re := regexp.MustCompile(`^(?:Fact:)?(\d+)$`)
		matches := re.FindStringSubmatch(f.FactID)
		if len(matches) == 2 {
			return strconv.ParseInt(matches[1], 10, 64)
		}
		return 0, fmt.Errorf("invalid fact_id format: %s", f.FactID)
	}
	// Legacy format: numeric id
	if f.ID > 0 {
		return f.ID, nil
	}
	return 0, fmt.Errorf("no fact ID provided")
}

// GetPersonID extracts person ID from UpdatedPerson.
// Prefers "Person:5" (string), returns empty string if not found.
func (p *UpdatedPerson) GetPersonID() (string, bool) {
	if p.PersonID != "" {
		re := regexp.MustCompile(`^(?:Person:)?(\d+)$`)
		matches := re.FindStringSubmatch(p.PersonID)
		if len(matches) == 2 {
			return matches[1], true
		}
	}
	return "", false
}

// GetTargetID extracts target person ID from MergedPerson.
// Prefers "Person:10" (string), returns empty string if not found.
func (p *MergedPerson) GetTargetID() (string, bool) {
	if p.TargetID != "" {
		re := regexp.MustCompile(`^(?:Person:)?(\d+)$`)
		matches := re.FindStringSubmatch(p.TargetID)
		if len(matches) == 2 {
			return matches[1], true
		}
	}
	return "", false
}

// GetSourceID extracts source person ID from MergedPerson.
// Prefers "Person:5" (string), returns empty string if not found.
func (p *MergedPerson) GetSourceID() (string, bool) {
	if p.SourceID != "" {
		re := regexp.MustCompile(`^(?:Person:)?(\d+)$`)
		matches := re.FindStringSubmatch(p.SourceID)
		if len(matches) == 2 {
			return matches[1], true
		}
	}
	return "", false
}

// ResultWithArrayFields handles LLM quirk where "facts" or "people" is wrapped in array.
type ResultWithArrayFields struct {
	Facts  []FactsResult  `json:"facts"`
	People []PeopleResult `json:"people"`
}

// RawFact represents a fact in the wrong format (LLM returned array of facts instead of operations).
type RawFact struct {
	ID         int64  `json:"id"`
	Content    string `json:"content"`
	Type       string `json:"type"`
	Importance int    `json:"importance"`
	Reason     string `json:"reason"`
}

// toUpdatedFact converts RawFact to UpdatedFact.
func (rf RawFact) toUpdatedFact() UpdatedFact {
	return UpdatedFact{
		ID:         rf.ID,
		Content:    rf.Content,
		Type:       rf.Type,
		Importance: rf.Importance,
		Reason:     rf.Reason,
	}
}

// RawPerson represents a person in the wrong format (LLM returned array of people instead of operations).
type RawPerson struct {
	DisplayName string   `json:"display_name"`
	Aliases     []string `json:"aliases"`
	Circle      string   `json:"circle"`
	Bio         string   `json:"bio"`
	Reason      string   `json:"reason"`
}

// toAddedPerson converts RawPerson to AddedPerson.
func (rp RawPerson) toAddedPerson() AddedPerson {
	return AddedPerson(rp)
}

// ResultWithRawArrays handles LLM error where it returns raw fact/person arrays instead of operations.
type ResultWithRawArrays struct {
	Facts  []RawFact   `json:"facts"`
	People []RawPerson `json:"people"`
}

// PeopleRepository is the interface for loading people.
type PeopleRepository interface {
	GetPeople(userID int64) ([]storage.Person, error)
}

// Archivist extracts and manages facts and people from conversations.
type Archivist struct {
	executor    *agent.Executor
	translator  *i18n.Translator
	cfg         *config.Config
	logger      *slog.Logger
	peopleRepo  PeopleRepository
	agentLogger *agentlog.Logger
}

// New creates a new Archivist agent.
func New(
	executor *agent.Executor,
	translator *i18n.Translator,
	cfg *config.Config,
	logger *slog.Logger,
	agentLogger *agentlog.Logger,
) *Archivist {
	return &Archivist{
		executor:    executor,
		translator:  translator,
		cfg:         cfg,
		logger:      logger.With("component", "archivist"),
		agentLogger: agentLogger,
	}
}

// SetPeopleRepository sets the people repository for the agent.
func (a *Archivist) SetPeopleRepository(repo PeopleRepository) {
	a.peopleRepo = repo
}

// Type returns the agent type.
func (a *Archivist) Type() agent.AgentType {
	return agent.TypeArchivist
}

// Capabilities returns the agent's capabilities.
func (a *Archivist) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		IsAgentic:    true,
		OutputFormat: "json",
	}
}

// Description returns a human-readable description.
func (a *Archivist) Description() string {
	return "Extracts and manages facts and people from conversations"
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
	people := a.getPeople(req)
	referenceDate := a.getReferenceDate(req)
	user := a.getUser(req)
	userID := a.getUserID(req)

	cfg := a.cfg.Agents.Archivist
	model := cfg.GetModel(a.cfg.Agents.Default.Model)
	if model == "" {
		return nil, fmt.Errorf("no model configured for archivist")
	}

	// Parse timeout
	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil || timeout <= 0 {
		timeout = 120 * time.Second
	}

	thinkingLevel := cfg.ThinkingLevel
	if thinkingLevel == "" {
		thinkingLevel = "medium"
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startTime := time.Now()
	tracker := agentlog.NewTurnTracker()

	// Build conversation text
	conversation := a.buildConversation(messages, user)

	// Prepare facts JSON (only User facts)
	userFacts := a.prepareUserFacts(facts)
	userFactsJSON, _ := json.MarshalIndent(userFacts, "", "  ")

	// Format all people with full bio (v0.5.1 unified format)
	knownPeople := storage.FormatPeople(people, storage.TagPeople)

	// Build system prompt (instructions only, no data)
	maxFacts := a.cfg.RAG.MaxProfileFacts
	systemPrompt, err := a.translator.GetTemplate(a.cfg.Bot.Language, "memory.system_prompt", prompts.ArchivistParams{
		Date:           referenceDate.Format("2006-01-02"),
		UserFactsLimit: maxFacts,
		UserFactsCount: len(userFacts),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build system prompt: %w", err)
	}

	// Add limit warning if needed
	if len(userFacts) > maxFacts {
		warning := fmt.Sprintf("\n\n<limit_exceeded>\nCRITICAL: You have %d facts about User. The limit is %d.\nYou MUST delete or consolidate at least %d facts in this response.\n</limit_exceeded>", len(userFacts), maxFacts, len(userFacts)-maxFacts+1)
		systemPrompt += warning
	}

	// Build user prompt (data: facts, people, conversation)
	userPrompt, err := a.translator.GetTemplate(a.cfg.Bot.Language, "memory.user_prompt", prompts.ArchivistParams{
		UserFacts:    string(userFactsJSON),
		KnownPeople:  knownPeople,
		Conversation: conversation,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build user prompt: %w", err)
	}

	messages_llm := []openrouter.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	// Single LLM call (v0.5.1: no agentic loop - all people info is in prompt)
	tracker.StartTurn()

	var reasoning *openrouter.ReasoningConfig
	if thinkingLevel != "off" && thinkingLevel != "" {
		reasoning = &openrouter.ReasoningConfig{
			Effort: thinkingLevel,
		}
	}

	llmReq := openrouter.ChatCompletionRequest{
		Model:    model,
		Messages: messages_llm,
		// NOTE: response-healing plugin breaks reasoning visibility when combined with json_object format.
		// Reasoning works correctly without plugins.
		ResponseFormat: openrouter.ResponseFormat{Type: "json_object"},
		Reasoning:      reasoning,
		UserID:         userID,
	}

	resp, err := a.executor.Client().CreateChatCompletion(ctx, llmReq)

	tracker.EndTurn(
		resp.DebugRequestBody,
		resp.DebugResponseBody,
		resp.Usage.PromptTokens,
		resp.Usage.CompletionTokens,
		resp.Usage.Cost,
	)

	if err != nil {
		a.logger.Warn("archivist LLM call failed", "error", err)
		a.saveTrace(userID, conversation, tracker, startTime, false, "llm_error: "+err.Error())
		return nil, fmt.Errorf("llm call failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		a.logger.Warn("archivist got empty response")
		a.saveTrace(userID, conversation, tracker, startTime, false, "empty_response")
		return nil, fmt.Errorf("empty response from LLM")
	}

	choice := resp.Choices[0]
	content := choice.Message.Content
	result, err := a.parseResponse(content)
	if err != nil {
		a.logger.Warn("failed to parse archivist response", "error", err, "content_preview", truncate(content, 500))
		a.saveTrace(userID, conversation, tracker, startTime, false, "parse_error: "+err.Error())
		return nil, fmt.Errorf("json parse error: %w, content: %s", err, content)
	}

	duration := time.Since(startTime)
	promptTokens, completionTokens := tracker.TotalTokens()

	a.logger.Info("archivist completed",
		"user_id", userID,
		"facts_added", len(result.Facts.Added),
		"facts_updated", len(result.Facts.Updated),
		"facts_removed", len(result.Facts.Removed),
		"people_added", len(result.People.Added),
		"people_updated", len(result.People.Updated),
		"people_merged", len(result.People.Merged),
		"duration_ms", duration.Milliseconds(),
	)

	a.saveTrace(userID, conversation, tracker, startTime, true, "")

	return &agent.Response{
		Content:    "",
		Structured: result,
		Duration:   duration,
		Tokens: agent.TokenUsage{
			Prompt:     promptTokens,
			Completion: completionTokens,
			Total:      promptTokens + completionTokens,
			Cost:       tracker.TotalCost(),
		},
		Metadata: map[string]any{
			"facts_added":    len(result.Facts.Added),
			"facts_updated":  len(result.Facts.Updated),
			"facts_removed":  len(result.Facts.Removed),
			"people_added":   len(result.People.Added),
			"people_updated": len(result.People.Updated),
			"people_merged":  len(result.People.Merged),
		},
	}, nil
}

// parseResponse parses the JSON response, supporting both new and legacy formats.
func (a *Archivist) parseResponse(content string) (*Result, error) {
	content = strings.TrimSpace(content)

	// Try new format first (object)
	var result Result
	if err := json.Unmarshal([]byte(content), &result); err == nil {
		// Check if it's the new format (has "facts" key) or legacy format
		if hasStructuredContent(&result) {
			return &result, nil
		}
	}

	// Try array format (LLM sometimes wraps response in array)
	var arrayResult []Result
	if err := json.Unmarshal([]byte(content), &arrayResult); err == nil && len(arrayResult) > 0 {
		if hasStructuredContent(&arrayResult[0]) {
			return &arrayResult[0], nil
		}
	}

	// Try format with array fields (LLM quirk: "facts": [...] instead of "facts": {...})
	var arrayFieldsResult ResultWithArrayFields
	if err := json.Unmarshal([]byte(content), &arrayFieldsResult); err == nil {
		if len(arrayFieldsResult.Facts) > 0 || len(arrayFieldsResult.People) > 0 {
			converted := &Result{}
			// Merge all facts arrays into one
			for _, f := range arrayFieldsResult.Facts {
				converted.Facts.Added = append(converted.Facts.Added, f.Added...)
				converted.Facts.Updated = append(converted.Facts.Updated, f.Updated...)
				converted.Facts.Removed = append(converted.Facts.Removed, f.Removed...)
			}
			// Merge all people arrays into one
			for _, p := range arrayFieldsResult.People {
				converted.People.Added = append(converted.People.Added, p.Added...)
				converted.People.Updated = append(converted.People.Updated, p.Updated...)
				converted.People.Merged = append(converted.People.Merged, p.Merged...)
			}
			if hasStructuredContent(converted) {
				return converted, nil
			}
		}
	}

	// Try raw arrays format (LLM error: returns fact/person arrays instead of operations)
	var rawArraysResult ResultWithRawArrays
	if err := json.Unmarshal([]byte(content), &rawArraysResult); err == nil {
		if len(rawArraysResult.Facts) > 0 || len(rawArraysResult.People) > 0 {
			// Log warning about wrong format
			a.logger.Warn("archivist received wrong format (raw arrays instead of operations), converting",
				"facts_count", len(rawArraysResult.Facts),
				"people_count", len(rawArraysResult.People))

			converted := &Result{}
			// Convert raw facts to updated facts (they have IDs, so they must be updates)
			for _, f := range rawArraysResult.Facts {
				converted.Facts.Updated = append(converted.Facts.Updated, f.toUpdatedFact())
			}
			// Convert raw people to added people (assume new, caller will deduplicate)
			for _, p := range rawArraysResult.People {
				converted.People.Added = append(converted.People.Added, p.toAddedPerson())
			}
			if hasStructuredContent(converted) {
				return converted, nil
			}
		}
	}

	// Try legacy format (backward compatibility)
	var legacy LegacyResult
	if err := json.Unmarshal([]byte(content), &legacy); err == nil {
		if len(legacy.Added) > 0 || len(legacy.Updated) > 0 || len(legacy.Removed) > 0 {
			return &Result{
				Facts:  FactsResult(legacy),
				People: PeopleResult{},
			}, nil
		}
	}

	// Default: try parsing as new format anyway
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// hasStructuredContent checks if result has any content (new format detection).
func hasStructuredContent(r *Result) bool {
	return len(r.Facts.Added) > 0 || len(r.Facts.Updated) > 0 || len(r.Facts.Removed) > 0 ||
		len(r.People.Added) > 0 || len(r.People.Updated) > 0 || len(r.People.Merged) > 0
}

// saveTrace saves the execution trace for debugging.
func (a *Archivist) saveTrace(
	userID int64,
	conversation string,
	tracker *agentlog.TurnTracker,
	startTime time.Time,
	success bool,
	errorMsg string,
) {
	if a.agentLogger == nil {
		return
	}

	duration := time.Since(startTime)
	promptTokens, completionTokens := tracker.TotalTokens()

	a.agentLogger.Log(context.Background(), agentlog.Entry{
		UserID:            userID,
		AgentType:         agentlog.AgentArchivist,
		InputPrompt:       truncate(conversation, 2000),
		InputContext:      tracker.FirstRequest(),
		OutputResponse:    tracker.LastResponse(),
		OutputParsed:      "",
		OutputContext:     "",
		Model:             a.cfg.Agents.Archivist.GetModel(a.cfg.Agents.Default.Model),
		PromptTokens:      promptTokens,
		CompletionTokens:  completionTokens,
		TotalCost:         tracker.TotalCost(),
		DurationMs:        int(duration.Milliseconds()),
		ConversationTurns: tracker.Build(),
		Metadata:          nil,
		Success:           success,
		ErrorMessage:      errorMsg,
	})
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

// getPeople extracts people from request params.
func (a *Archivist) getPeople(req *agent.Request) []storage.Person {
	if req.Params == nil {
		return nil
	}
	if people, ok := req.Params[ParamPeople].([]storage.Person); ok {
		return people
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
	Relation   string `json:"relation"`
	Content    string `json:"content"`
	Category   string `json:"category"`
	Type       string `json:"type"`
	Importance int    `json:"importance"`
}

// prepareUserFacts formats facts for the prompt.
// All facts are now about User (entity field removed).
func (a *Archivist) prepareUserFacts(facts []storage.Fact) []FactView {
	var userFacts []FactView
	for _, f := range facts {
		userFacts = append(userFacts, FactView{
			ID:         f.ID,
			Relation:   f.Relation,
			Content:    f.Content,
			Category:   f.Category,
			Type:       f.Type,
			Importance: f.Importance,
		})
	}
	return userFacts
}

// truncate limits string length for logging.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

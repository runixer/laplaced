package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

type VectorSearcher interface {
	FindSimilarFacts(ctx context.Context, userID int64, embedding []float32, threshold float32) ([]storage.Fact, error)
}

type Service struct {
	logger          *slog.Logger
	cfg             *config.Config
	factRepo        storage.FactRepository
	userRepo        storage.UserRepository
	factHistoryRepo storage.FactHistoryRepository
	orClient        openrouter.Client
	translator      *i18n.Translator
	vectorSearcher  VectorSearcher
}

func NewService(logger *slog.Logger, cfg *config.Config, factRepo storage.FactRepository, userRepo storage.UserRepository, factHistoryRepo storage.FactHistoryRepository, orClient openrouter.Client, translator *i18n.Translator) *Service {
	return &Service{
		logger:          logger.With("component", "memory"),
		cfg:             cfg,
		factRepo:        factRepo,
		userRepo:        userRepo,
		factHistoryRepo: factHistoryRepo,
		orClient:        orClient,
		translator:      translator,
	}
}

func (s *Service) SetVectorSearcher(vs VectorSearcher) {
	s.vectorSearcher = vs
}

// MemoryUpdate represents the changes returned by the LLM
type MemoryUpdate struct {
	Added []struct {
		Entity     string `json:"entity"`
		Relation   string `json:"relation"`
		Content    string `json:"content"`
		Category   string `json:"category"`
		Type       string `json:"type"`
		Importance int    `json:"importance"`
		Reason     string `json:"reason"`
	} `json:"added"`
	Updated []struct {
		ID         int64  `json:"id"`
		Content    string `json:"content"`
		Type       string `json:"type,omitempty"`
		Importance int    `json:"importance"`
		Reason     string `json:"reason"`
	} `json:"updated"`
	Removed []struct {
		ID     int64  `json:"id"`
		Reason string `json:"reason"`
	} `json:"removed"`
}

// FactStats contains statistics about fact processing.
type FactStats struct {
	Created int
	Updated int
	Deleted int
	// Usage tracking from API calls
	PromptTokens     int
	CompletionTokens int
	EmbeddingTokens  int
	Cost             *float64
}

// AddChatUsage adds usage from a chat completion response.
func (s *FactStats) AddChatUsage(promptTokens, completionTokens int, cost *float64) {
	s.PromptTokens += promptTokens
	s.CompletionTokens += completionTokens
	if cost != nil {
		if s.Cost == nil {
			s.Cost = new(float64)
		}
		*s.Cost += *cost
	}
}

// AddEmbeddingUsage adds usage from an embedding response.
func (s *FactStats) AddEmbeddingUsage(tokens int, cost *float64) {
	s.EmbeddingTokens += tokens
	if cost != nil {
		if s.Cost == nil {
			s.Cost = new(float64)
		}
		*s.Cost += *cost
	}
}

func (s *Service) ProcessSession(ctx context.Context, userID int64, messages []storage.Message, referenceDate time.Time, topicID int64) error {
	_, err := s.ProcessSessionWithStats(ctx, userID, messages, referenceDate, topicID)
	return err
}

// ProcessSessionWithStats processes a session and returns statistics about fact changes.
func (s *Service) ProcessSessionWithStats(ctx context.Context, userID int64, messages []storage.Message, referenceDate time.Time, topicID int64) (FactStats, error) {
	// Set a timeout to prevent hanging indefinitely if the model is slow or unresponsive
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	s.logger.Info("Processing session for memory update", "user_id", userID, "msg_count", len(messages), "topic_id", topicID, "topic_date", referenceDate.Format(time.RFC3339))

	// 1. Load current facts
	facts, err := s.factRepo.GetFacts(userID)
	if err != nil {
		return FactStats{}, fmt.Errorf("failed to get facts: %w", err)
	}

	// Fetch user info for formatting
	var user *storage.User
	users, err := s.userRepo.GetAllUsers()
	if err == nil {
		for _, u := range users {
			if u.ID == userID {
				user = &u
				break
			}
		}
	} else {
		s.logger.Warn("failed to fetch users for formatting", "error", err)
	}

	// 2. Prepare prompt
	update, requestInput, extractUsage, err := s.extractMemoryUpdate(ctx, userID, messages, facts, referenceDate, user)
	if err != nil {
		return FactStats{}, fmt.Errorf("failed to extract memory update: %w", err)
	}

	// 3. Apply updates and collect stats
	stats, err := s.applyUpdateWithStats(ctx, userID, update, facts, referenceDate, topicID, requestInput)
	if err != nil {
		return FactStats{}, fmt.Errorf("failed to apply memory update: %w", err)
	}

	// Add LLM usage from extraction call
	stats.AddChatUsage(extractUsage.PromptTokens, extractUsage.CompletionTokens, extractUsage.Cost)

	return stats, nil
}

func (s *Service) extractMemoryUpdate(ctx context.Context, userID int64, session []storage.Message, facts []storage.Fact, referenceDate time.Time, user *storage.User) (*MemoryUpdate, string, chatUsage, error) {
	startTime := time.Now()
	defer func() {
		RecordMemoryExtraction(time.Since(startTime).Seconds())
	}()

	var sb strings.Builder
	for _, msg := range session {
		if msg.Role == "user" {
			// Check if content is already formatted (starts with [ and contains ]:)
			// This is a heuristic to avoid double formatting if the storage already contains formatted messages
			if strings.HasPrefix(msg.Content, "[") && strings.Contains(msg.Content, "]:") {
				sb.WriteString(fmt.Sprintf("%s\n", msg.Content))
			} else {
				// Format message
				name := "User"
				if user != nil {
					name = strings.TrimSpace(user.FirstName + " " + user.LastName)
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
				}
				dateStr := msg.CreatedAt.Format("2006-01-02 15:04:05")
				sb.WriteString(fmt.Sprintf("[%s (%s)]: %s\n", name, dateStr, msg.Content))
			}
		} else {
			// Assistant/Bot message
			dateStr := msg.CreatedAt.Format("2006-01-02 15:04:05")
			sb.WriteString(fmt.Sprintf("[Bot (%s)]: %s\n", dateStr, msg.Content))
		}
	}

	// Serialize facts for prompt (simplified view)
	type FactView struct {
		ID         int64  `json:"id"`
		Entity     string `json:"entity"`
		Relation   string `json:"relation"`
		Content    string `json:"content"`
		Category   string `json:"category"`
		Type       string `json:"type"`
		Importance int    `json:"importance"`
	}
	var userFacts []FactView
	var otherFacts []FactView

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

	userFactsJSON, _ := json.MarshalIndent(userFacts, "", "  ")
	otherFactsJSON, _ := json.MarshalIndent(otherFacts, "", "  ")
	currentDate := referenceDate.Format("2006-01-02")

	// Schemas
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

	schema := map[string]interface{}{
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
	}

	maxFacts := s.cfg.RAG.MaxProfileFacts
	prompt := s.translator.Get(s.cfg.Bot.Language, "memory.system_prompt", currentDate, maxFacts, len(userFacts), len(otherFacts), string(userFactsJSON), string(otherFactsJSON), sb.String())
	if len(userFacts) > maxFacts {
		warning := fmt.Sprintf("\n\nCRITICAL WARNING: You have %d facts about User. The limit is %d. You MUST delete or consolidate at least %d facts in this turn to reduce the count.", len(userFacts), maxFacts, len(userFacts)-maxFacts+1)
		prompt += warning
	}

	fullRequest := fmt.Sprintf("%s\n\n=== MESSAGES ===\n%s", prompt, sb.String())

	req := openrouter.ChatCompletionRequest{
		Model: s.cfg.OpenRouter.Model,
		Messages: []openrouter.Message{
			{Role: "system", Content: prompt},
		},
		ResponseFormat: &openrouter.ResponseFormat{
			Type: "json_schema",
			JSONSchema: &openrouter.JSONSchema{
				Name:   "memory_update",
				Strict: true,
				Schema: schema,
			},
		},
		UserID: userID,
	}

	resp, err := s.orClient.CreateChatCompletion(ctx, req)
	if err != nil {
		return nil, fullRequest, chatUsage{}, err
	}

	usage := chatUsage{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		Cost:             resp.Usage.Cost,
	}

	if len(resp.Choices) == 0 {
		return nil, fullRequest, usage, fmt.Errorf("empty response")
	}

	var update MemoryUpdate
	if err := json.Unmarshal([]byte(resp.Choices[0].Message.Content), &update); err != nil {
		return nil, fullRequest, usage, fmt.Errorf("failed to parse response: %w", err)
	}

	return &update, fullRequest, usage, nil
}

func (s *Service) applyUpdateWithStats(ctx context.Context, userID int64, update *MemoryUpdate, currentFacts []storage.Fact, referenceDate time.Time, topicID int64, requestInput string) (FactStats, error) {
	var stats FactStats

	// Handle Added
	for _, added := range update.Added {
		emb, embUsage, err := s.getEmbedding(ctx, added.Content)
		stats.AddEmbeddingUsage(embUsage.Tokens, embUsage.Cost)
		if err != nil {
			s.logger.Error("failed to get embedding", "error", err)
			continue
		}

		var tID *int64
		if topicID != 0 {
			tID = &topicID
		}

		fact := storage.Fact{
			UserID:      userID,
			Entity:      added.Entity,
			Relation:    added.Relation,
			Content:     added.Content,
			Category:    added.Category,
			Type:        added.Type,
			Importance:  added.Importance,
			Embedding:   emb,
			TopicID:     tID,
			CreatedAt:   referenceDate,
			LastUpdated: referenceDate,
		}

		if err := s.deduplicateAndAddFact(ctx, fact, added.Reason, topicID, requestInput); err != nil {
			s.logger.Error("failed to process fact addition", "error", err)
		} else {
			stats.Created++
			RecordFactOperation(userID, OperationAdd)
		}
	}

	// Handle Updated
	for _, updated := range update.Updated {
		// Find existing fact to get other fields if needed
		var existingFact *storage.Fact
		for _, f := range currentFacts {
			if f.ID == updated.ID {
				existingFact = &f
				break
			}
		}

		if existingFact == nil {
			s.logger.Warn("Fact to update not found", "id", updated.ID)
			continue
		}

		// Re-embed if content changed
		var emb []float32
		if updated.Content != existingFact.Content {
			var err error
			var embUsage embeddingUsage
			emb, embUsage, err = s.getEmbedding(ctx, updated.Content)
			stats.AddEmbeddingUsage(embUsage.Tokens, embUsage.Cost)
			if err != nil {
				s.logger.Error("failed to get embedding for update", "error", err)
				continue
			}
		} else {
			emb = existingFact.Embedding
		}

		fact := storage.Fact{
			ID:          updated.ID,
			UserID:      userID,
			Content:     updated.Content,
			Type:        updated.Type,
			Importance:  updated.Importance,
			Embedding:   emb,
			LastUpdated: referenceDate,
		}

		// If type is missing in update, keep old type
		if fact.Type == "" {
			fact.Type = existingFact.Type
		}

		if err := s.factRepo.UpdateFact(fact); err != nil {
			s.logger.Error("failed to update fact", "error", err)
		} else {
			stats.Updated++
			RecordFactOperation(userID, OperationUpdate)
			s.logger.Info("Fact updated", "id", updated.ID)
			if s.cfg.Server.DebugMode {
				var tID *int64
				if topicID != 0 {
					tID = &topicID
				}
				if err := s.factHistoryRepo.AddFactHistory(storage.FactHistory{
					FactID:       updated.ID,
					UserID:       userID,
					Action:       "update",
					OldContent:   existingFact.Content,
					NewContent:   updated.Content,
					Reason:       updated.Reason,
					Category:     existingFact.Category, // Keep existing category or should we allow update? Schema doesn't support category update in MemoryUpdate struct yet.
					Entity:       existingFact.Entity,
					Relation:     existingFact.Relation,
					Importance:   updated.Importance,
					TopicID:      tID,
					RequestInput: requestInput,
				}); err != nil {
					s.logger.Warn("failed to add fact history", "action", "update", "fact_id", updated.ID, "error", err)
				}
			}
		}
	}

	// Handle Removed
	for _, removed := range update.Removed {
		// Find existing fact for history
		var oldContent string
		var category, entity, relation string
		var importance int
		for _, f := range currentFacts {
			if f.ID == removed.ID {
				oldContent = f.Content
				category = f.Category
				entity = f.Entity
				relation = f.Relation
				importance = f.Importance
				break
			}
		}

		if err := s.factRepo.DeleteFact(userID, removed.ID); err != nil {
			s.logger.Error("failed to delete fact", "error", err)
		} else {
			stats.Deleted++
			RecordFactOperation(userID, OperationDelete)
			s.logger.Info("Fact removed", "id", removed.ID)
			if s.cfg.Server.DebugMode {
				var tID *int64
				if topicID != 0 {
					tID = &topicID
				}
				if err := s.factHistoryRepo.AddFactHistory(storage.FactHistory{
					FactID:       removed.ID,
					UserID:       userID,
					Action:       "delete",
					OldContent:   oldContent,
					Reason:       removed.Reason,
					Category:     category,
					Entity:       entity,
					Relation:     relation,
					Importance:   importance,
					TopicID:      tID,
					RequestInput: requestInput,
				}); err != nil {
					s.logger.Warn("failed to add fact history", "action", "delete", "fact_id", removed.ID, "error", err)
				}
			}
		}
	}

	return stats, nil
}

// chatUsage holds usage info from chat completion call.
type chatUsage struct {
	PromptTokens     int
	CompletionTokens int
	Cost             *float64
}

// embeddingUsage holds usage info from embedding call.
type embeddingUsage struct {
	Tokens int
	Cost   *float64
}

func (s *Service) getEmbedding(ctx context.Context, text string) ([]float32, embeddingUsage, error) {
	resp, err := s.orClient.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
		Model: s.cfg.RAG.EmbeddingModel,
		Input: []string{text},
	})
	if err != nil {
		return nil, embeddingUsage{}, err
	}
	usage := embeddingUsage{
		Tokens: resp.Usage.TotalTokens,
		Cost:   resp.Usage.Cost,
	}
	if len(resp.Data) == 0 {
		return nil, usage, fmt.Errorf("no embedding returned")
	}
	return resp.Data[0].Embedding, usage, nil
}

// addFactWithHistory adds a fact and records history if debug mode is enabled.
func (s *Service) addFactWithHistory(fact storage.Fact, reason string, topicID *int64, requestInput string) (int64, error) {
	id, err := s.factRepo.AddFact(fact)
	if err != nil {
		return 0, err
	}

	if s.cfg.Server.DebugMode {
		if err := s.factHistoryRepo.AddFactHistory(storage.FactHistory{
			FactID:       id,
			UserID:       fact.UserID,
			Action:       "add",
			NewContent:   fact.Content,
			Reason:       reason,
			Category:     fact.Category,
			Entity:       fact.Entity,
			Relation:     fact.Relation,
			Importance:   fact.Importance,
			TopicID:      topicID,
			RequestInput: requestInput,
		}); err != nil {
			s.logger.Warn("failed to add fact history", "action", "add", "fact_id", id, "error", err)
		}
	}

	return id, nil
}

func (s *Service) deduplicateAndAddFact(ctx context.Context, fact storage.Fact, reason string, topicID int64, requestInput string) error {
	var tID *int64
	if topicID != 0 {
		tID = &topicID
	}

	if s.vectorSearcher == nil {
		_, err := s.addFactWithHistory(fact, reason, tID, requestInput)
		return err
	}

	// Search for similar facts
	similar, err := s.vectorSearcher.FindSimilarFacts(ctx, fact.UserID, fact.Embedding, 0.85)
	if err != nil {
		s.logger.Error("failed to search similar facts", "error", err)
		_, err := s.addFactWithHistory(fact, reason, tID, requestInput)
		return err
	}

	if len(similar) == 0 {
		_, err := s.addFactWithHistory(fact, reason, tID, requestInput)
		return err
	}

	// Arbitration
	action, targetID, newContent, err := s.arbitrateFact(ctx, fact, similar)
	if err != nil {
		s.logger.Error("arbitration failed", "error", err)
		_, err := s.addFactWithHistory(fact, reason, tID, requestInput)
		return err
	}

	switch action {
	case "IGNORE":
		RecordDedupDecision(fact.UserID, DecisionIgnore)
		s.logger.Info("Fact ignored as duplicate", "content", fact.Content)
		// Update LastUpdated of the most similar fact to keep it fresh
		if len(similar) > 0 {
			f := similar[0]
			f.LastUpdated = time.Now()
			if err := s.factRepo.UpdateFact(f); err != nil {
				s.logger.Warn("failed to update timestamp of existing fact", "id", f.ID, "error", err)
			}
		}
		return nil
	case "REPLACE", "MERGE":
		if action == "REPLACE" {
			RecordDedupDecision(fact.UserID, DecisionReplace)
		} else {
			RecordDedupDecision(fact.UserID, DecisionMerge)
		}
		s.logger.Info("Fact merged/replaced", "action", action, "target_id", targetID)

		// Find the target fact in similar list or fetch it
		var targetFact *storage.Fact
		for _, f := range similar {
			if f.ID == targetID {
				targetFact = &f
				break
			}
		}
		if targetFact == nil {
			// Fallback if ID returned by LLM is weird, though unlikely if we passed it
			_, err := s.addFactWithHistory(fact, reason, tID, requestInput)
			return err
		}

		oldContent := targetFact.Content
		targetFact.Content = newContent
		targetFact.LastUpdated = time.Now()

		// Re-embed if content changed
		if action == "MERGE" || newContent != targetFact.Content {
			emb, _, err := s.getEmbedding(ctx, newContent)
			if err == nil {
				targetFact.Embedding = emb
			}
		}

		if err := s.factRepo.UpdateFact(*targetFact); err != nil {
			return err
		}

		if s.cfg.Server.DebugMode {
			if err := s.factHistoryRepo.AddFactHistory(storage.FactHistory{
				FactID:       targetFact.ID,
				UserID:       fact.UserID,
				Action:       "update",
				OldContent:   oldContent,
				NewContent:   newContent,
				Reason:       fmt.Sprintf("%s (Merged/Replaced)", reason),
				Category:     targetFact.Category,
				Entity:       targetFact.Entity,
				Relation:     targetFact.Relation,
				Importance:   targetFact.Importance,
				TopicID:      tID,
				RequestInput: requestInput,
			}); err != nil {
				s.logger.Warn("failed to add fact history", "action", "merge", "fact_id", targetFact.ID, "error", err)
			}
		}
		return nil

	case "ADD":
		RecordDedupDecision(fact.UserID, DecisionAdd)
		_, err := s.addFactWithHistory(fact, reason, tID, requestInput)
		return err
	default:
		RecordDedupDecision(fact.UserID, DecisionAdd)
		_, err := s.addFactWithHistory(fact, reason, tID, requestInput)
		return err
	}
}

func (s *Service) arbitrateFact(ctx context.Context, newFact storage.Fact, existingFacts []storage.Fact) (string, int64, string, error) {
	var existingSB strings.Builder
	for _, f := range existingFacts {
		existingSB.WriteString(fmt.Sprintf("- [ID:%d] %s (Date: %s)\n", f.ID, f.Content, f.CreatedAt.Format("2006-01-02")))
	}

	prompt := s.translator.Get(s.cfg.Bot.Language, "memory.consolidation_prompt", newFact.Content, existingSB.String())

	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{"IGNORE", "REPLACE", "MERGE", "ADD"},
			},
			"target_id": map[string]interface{}{
				"type": "integer",
			},
			"new_content": map[string]interface{}{
				"type": "string",
			},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
	}

	req := openrouter.ChatCompletionRequest{
		Model: s.cfg.OpenRouter.Model, // Use main model or a cheaper one? Main is fine for now.
		Messages: []openrouter.Message{
			{Role: "system", Content: prompt},
		},
		ResponseFormat: &openrouter.ResponseFormat{
			Type: "json_schema",
			JSONSchema: &openrouter.JSONSchema{
				Name:   "consolidation_decision",
				Strict: true,
				Schema: schema,
			},
		},
		UserID: newFact.UserID,
	}

	resp, err := s.orClient.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", 0, "", err
	}

	if len(resp.Choices) == 0 {
		return "", 0, "", fmt.Errorf("empty response")
	}

	var result struct {
		Action     string `json:"action"`
		TargetID   int64  `json:"target_id"`
		NewContent string `json:"new_content"`
	}
	if err := json.Unmarshal([]byte(resp.Choices[0].Message.Content), &result); err != nil {
		return "", 0, "", err
	}

	return result.Action, result.TargetID, result.NewContent, nil
}

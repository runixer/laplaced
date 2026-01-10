package memory

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/archivist"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/jobtype"
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
	topicRepo       storage.TopicRepository
	orClient        openrouter.Client
	translator      *i18n.Translator
	vectorSearcher  VectorSearcher
	agentLogger     *agentlog.Logger
	archivistAgent  agent.Agent // Fact extraction agent
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

func (s *Service) SetTopicRepository(tr storage.TopicRepository) {
	s.topicRepo = tr
}

func (s *Service) SetAgentLogger(logger *agentlog.Logger) {
	s.agentLogger = logger
}

// SetArchivistAgent sets the fact extraction agent.
func (s *Service) SetArchivistAgent(a agent.Agent) {
	s.archivistAgent = a
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
	// Mark as background job for metrics (archiver is a maintenance task)
	ctx = jobtype.WithJobType(ctx, jobtype.Background)

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
	if s.archivistAgent == nil {
		return nil, "", chatUsage{}, fmt.Errorf("archivist agent not configured")
	}
	return s.extractMemoryUpdateViaAgent(ctx, userID, session, facts, referenceDate, user)
}

// extractMemoryUpdateViaAgent delegates fact extraction to the Archivist agent.
func (s *Service) extractMemoryUpdateViaAgent(ctx context.Context, userID int64, session []storage.Message, facts []storage.Fact, referenceDate time.Time, user *storage.User) (*MemoryUpdate, string, chatUsage, error) {
	startTime := time.Now()
	defer func() {
		RecordMemoryExtraction(time.Since(startTime).Seconds())
	}()

	req := &agent.Request{
		Params: map[string]any{
			archivist.ParamMessages:      session,
			archivist.ParamFacts:         facts,
			archivist.ParamReferenceDate: referenceDate,
			archivist.ParamUser:          user,
			"user_id":                    userID,
		},
	}

	// Try to get SharedContext from ctx
	if shared := agent.FromContext(ctx); shared != nil {
		req.Shared = shared
	}

	resp, err := s.archivistAgent.Execute(ctx, req)
	if err != nil {
		return nil, "", chatUsage{}, err
	}

	result, ok := resp.Structured.(*archivist.Result)
	if !ok {
		return nil, "", chatUsage{}, fmt.Errorf("unexpected result type from archivist agent")
	}

	// Convert archivist.Result to MemoryUpdate
	update := convertArchivistResult(result)

	usage := chatUsage{
		PromptTokens:     resp.Tokens.Prompt,
		CompletionTokens: resp.Tokens.Completion,
		Cost:             resp.Tokens.Cost,
	}

	return update, resp.Content, usage, nil
}

// convertArchivistResult converts archivist.Result to MemoryUpdate.
func convertArchivistResult(result *archivist.Result) *MemoryUpdate {
	update := &MemoryUpdate{}

	for _, a := range result.Added {
		update.Added = append(update.Added, struct {
			Entity     string `json:"entity"`
			Relation   string `json:"relation"`
			Content    string `json:"content"`
			Category   string `json:"category"`
			Type       string `json:"type"`
			Importance int    `json:"importance"`
			Reason     string `json:"reason"`
		}{
			Entity:     a.Entity,
			Relation:   a.Relation,
			Content:    a.Content,
			Category:   a.Category,
			Type:       a.Type,
			Importance: a.Importance,
			Reason:     a.Reason,
		})
	}

	for _, u := range result.Updated {
		update.Updated = append(update.Updated, struct {
			ID         int64  `json:"id"`
			Content    string `json:"content"`
			Type       string `json:"type,omitempty"`
			Importance int    `json:"importance"`
			Reason     string `json:"reason"`
		}{
			ID:         u.ID,
			Content:    u.Content,
			Type:       u.Type,
			Importance: u.Importance,
			Reason:     u.Reason,
		})
	}

	for _, r := range result.Removed {
		update.Removed = append(update.Removed, struct {
			ID     int64  `json:"id"`
			Reason string `json:"reason"`
		}{
			ID:     r.ID,
			Reason: r.Reason,
		})
	}

	return update
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

		if _, err := s.addFactWithHistory(fact, added.Reason, tID, requestInput); err != nil {
			s.logger.Error("failed to add fact", "error", err)
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
		Model: s.cfg.Embedding.Model,
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

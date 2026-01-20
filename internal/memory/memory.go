package memory

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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
	peopleRepo      storage.PeopleRepository
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

// SetPeopleRepository sets the people repository for person extraction.
func (s *Service) SetPeopleRepository(pr storage.PeopleRepository) {
	s.peopleRepo = pr
}

// MemoryUpdate represents the changes returned by the LLM
type MemoryUpdate struct {
	Added []struct {
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
	// People stats (v0.5.1)
	PeopleAdded   int
	PeopleUpdated int
	PeopleMerged  int
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

	// 1b. Load current people (v0.5.1)
	var people []storage.Person
	if s.peopleRepo != nil {
		people, err = s.peopleRepo.GetPeople(userID)
		if err != nil {
			s.logger.Warn("failed to load people for archivist", "error", err)
			people = nil // Continue without people context
		}
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

	// 2. Prepare prompt and extract memory update
	archivistResult, requestInput, extractUsage, err := s.extractMemoryUpdate(ctx, userID, messages, facts, people, referenceDate, user)
	if err != nil {
		return FactStats{}, fmt.Errorf("failed to extract memory update: %w", err)
	}

	// 3. Apply fact updates and collect stats
	update := convertArchivistResult(archivistResult)
	stats, err := s.applyUpdateWithStats(ctx, userID, update, facts, referenceDate, topicID, requestInput)
	if err != nil {
		return FactStats{}, fmt.Errorf("failed to apply memory update: %w", err)
	}

	// 4. Apply people updates (v0.5.1)
	if s.peopleRepo != nil && archivistResult != nil {
		peopleStats, err := s.applyPeopleUpdates(ctx, userID, archivistResult.People, people, referenceDate)
		if err != nil {
			s.logger.Error("failed to apply people updates", "error", err)
		} else {
			stats.PeopleAdded = peopleStats.Added
			stats.PeopleUpdated = peopleStats.Updated
			stats.PeopleMerged = peopleStats.Merged
			stats.AddEmbeddingUsage(peopleStats.EmbeddingTokens, peopleStats.EmbeddingCost)
		}
	}

	// Add LLM usage from extraction call
	stats.AddChatUsage(extractUsage.PromptTokens, extractUsage.CompletionTokens, extractUsage.Cost)

	return stats, nil
}

func (s *Service) extractMemoryUpdate(ctx context.Context, userID int64, session []storage.Message, facts []storage.Fact, people []storage.Person, referenceDate time.Time, user *storage.User) (*archivist.Result, string, chatUsage, error) {
	if s.archivistAgent == nil {
		return nil, "", chatUsage{}, fmt.Errorf("archivist agent not configured")
	}
	return s.extractMemoryUpdateViaAgent(ctx, userID, session, facts, people, referenceDate, user)
}

// extractMemoryUpdateViaAgent delegates fact and people extraction to the Archivist agent.
func (s *Service) extractMemoryUpdateViaAgent(ctx context.Context, userID int64, session []storage.Message, facts []storage.Fact, people []storage.Person, referenceDate time.Time, user *storage.User) (*archivist.Result, string, chatUsage, error) {
	startTime := time.Now()
	defer func() {
		RecordMemoryExtraction(time.Since(startTime).Seconds())
	}()

	req := &agent.Request{
		Params: map[string]any{
			archivist.ParamMessages:      session,
			archivist.ParamFacts:         facts,
			archivist.ParamPeople:        people,
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

	usage := chatUsage{
		PromptTokens:     resp.Tokens.Prompt,
		CompletionTokens: resp.Tokens.Completion,
		Cost:             resp.Tokens.Cost,
	}

	return result, resp.Content, usage, nil
}

// convertArchivistResult converts archivist.Result to MemoryUpdate.
func convertArchivistResult(result *archivist.Result) *MemoryUpdate {
	update := &MemoryUpdate{}

	for _, a := range result.Facts.Added {
		update.Added = append(update.Added, struct {
			Relation   string `json:"relation"`
			Content    string `json:"content"`
			Category   string `json:"category"`
			Type       string `json:"type"`
			Importance int    `json:"importance"`
			Reason     string `json:"reason"`
		}{
			Relation:   a.Relation,
			Content:    a.Content,
			Category:   a.Category,
			Type:       a.Type,
			Importance: a.Importance,
			Reason:     a.Reason,
		})
	}

	for _, u := range result.Facts.Updated {
		factID, err := u.GetFactID()
		if err != nil {
			// Skip invalid fact IDs silently
			continue
		}
		update.Updated = append(update.Updated, struct {
			ID         int64  `json:"id"`
			Content    string `json:"content"`
			Type       string `json:"type,omitempty"`
			Importance int    `json:"importance"`
			Reason     string `json:"reason"`
		}{
			ID:         factID,
			Content:    u.Content,
			Type:       u.Type,
			Importance: u.Importance,
			Reason:     u.Reason,
		})
	}

	for _, r := range result.Facts.Removed {
		factID, err := r.GetFactID()
		if err != nil {
			// Skip invalid fact IDs silently
			continue
		}
		update.Removed = append(update.Removed, struct {
			ID     int64  `json:"id"`
			Reason string `json:"reason"`
		}{
			ID:     factID,
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
					Category:     existingFact.Category,
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
		var category, relation string
		var importance int
		for _, f := range currentFacts {
			if f.ID == removed.ID {
				oldContent = f.Content
				category = f.Category
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

// PeopleStats contains statistics about people processing.
type PeopleStats struct {
	Added           int
	Updated         int
	Merged          int
	EmbeddingTokens int
	EmbeddingCost   *float64
}

// buildPersonEmbeddingText constructs text for person embedding from all searchable fields.
// Includes display_name, username, aliases, and bio for comprehensive vector search.
func buildPersonEmbeddingText(displayName string, username *string, aliases []string, bio string) string {
	text := displayName
	if username != nil && *username != "" {
		text += " " + *username
	}
	for _, alias := range aliases {
		text += " " + alias
	}
	if bio != "" {
		text += " " + bio
	}
	return text
}

// applyPeopleUpdates applies people changes from the archivist result.
func (s *Service) applyPeopleUpdates(ctx context.Context, userID int64, peopleResult archivist.PeopleResult, currentPeople []storage.Person, referenceDate time.Time) (PeopleStats, error) {
	var stats PeopleStats

	// Handle Added people
	for _, added := range peopleResult.Added {
		// Check if person already exists by name or alias
		var existingPerson *storage.Person
		var matchType string
		for _, p := range currentPeople {
			if p.DisplayName == added.DisplayName {
				existingPerson = &p
				matchType = "exact"
				break
			}
			// Check aliases
			for _, alias := range p.Aliases {
				if alias == added.DisplayName {
					existingPerson = &p
					matchType = "alias"
					break
				}
			}
			if existingPerson != nil {
				break
			}
		}

		// Check for name prefix pattern (e.g., "Мария" vs "Мария Ивановна Петрова")
		// This catches Russian naming patterns: first name → +patronymic → +surname
		if existingPerson == nil {
			for i := range currentPeople {
				p := &currentPeople[i]
				// New name starts with existing name (e.g., "Мария Ивановна" starts with "Мария")
				if strings.HasPrefix(added.DisplayName, p.DisplayName+" ") && p.Circle == added.Circle {
					existingPerson = p
					matchType = "prefix"
					break
				}
				// Existing name starts with new name (e.g., existing "Мария Ивановна", adding "Мария")
				if strings.HasPrefix(p.DisplayName, added.DisplayName+" ") && p.Circle == added.Circle {
					existingPerson = p
					matchType = "prefix_reverse"
					break
				}
			}
		}

		if existingPerson != nil {
			if matchType == "prefix" || matchType == "prefix_reverse" {
				// Convert add to update: rename existing person to fuller name and update bio
				s.logger.Info("Detected name variation, converting add to update",
					"new_name", added.DisplayName,
					"existing_name", existingPerson.DisplayName,
					"existing_id", existingPerson.ID,
					"match_type", matchType)

				// Use the fuller name as display_name
				newDisplayName := added.DisplayName
				if matchType == "prefix_reverse" {
					newDisplayName = existingPerson.DisplayName // Keep existing fuller name
				}

				// Add old name to aliases if not already there
				oldName := existingPerson.DisplayName
				if matchType == "prefix_reverse" {
					oldName = added.DisplayName
				}
				aliasExists := false
				for _, a := range existingPerson.Aliases {
					if a == oldName {
						aliasExists = true
						break
					}
				}
				newAliases := existingPerson.Aliases
				if !aliasExists && oldName != newDisplayName {
					newAliases = append(newAliases, oldName)
				}
				// Also add any new aliases from added
				for _, a := range added.Aliases {
					found := false
					for _, ea := range newAliases {
						if ea == a {
							found = true
							break
						}
					}
					if !found && a != newDisplayName {
						newAliases = append(newAliases, a)
					}
				}

				// Update the person with fuller name and merged bio
				existingPerson.DisplayName = newDisplayName
				existingPerson.Aliases = newAliases
				if added.Bio != "" {
					existingPerson.Bio = added.Bio // Use new bio (it's likely more complete)
				}
				existingPerson.LastSeen = referenceDate
				existingPerson.MentionCount++

				// Regenerate embedding with new data
				embText := buildPersonEmbeddingText(existingPerson.DisplayName, existingPerson.Username, existingPerson.Aliases, existingPerson.Bio)
				emb, embUsage, err := s.getEmbedding(ctx, embText)
				stats.EmbeddingTokens += embUsage.Tokens
				if embUsage.Cost != nil {
					if stats.EmbeddingCost == nil {
						stats.EmbeddingCost = new(float64)
					}
					*stats.EmbeddingCost += *embUsage.Cost
				}
				if err != nil {
					s.logger.Error("failed to get embedding for person update", "name", existingPerson.DisplayName, "error", err)
				} else {
					existingPerson.Embedding = emb
				}

				if err := s.peopleRepo.UpdatePerson(*existingPerson); err != nil {
					s.logger.Error("failed to update person (name variation)", "id", existingPerson.ID, "error", err)
				} else {
					s.logger.Info("Person updated (name variation)", "id", existingPerson.ID, "name", existingPerson.DisplayName)
					stats.Updated++
				}
				continue
			}

			s.logger.Info("Person already exists, skipping add", "name", added.DisplayName, "existing_id", existingPerson.ID, "match_type", matchType)
			continue
		}

		// Get embedding for person (includes display_name, username, aliases, bio)
		embText := buildPersonEmbeddingText(added.DisplayName, nil, added.Aliases, added.Bio)
		var emb []float32
		var embUsage embeddingUsage
		var err error
		emb, embUsage, err = s.getEmbedding(ctx, embText)
		stats.EmbeddingTokens += embUsage.Tokens
		if embUsage.Cost != nil {
			if stats.EmbeddingCost == nil {
				stats.EmbeddingCost = new(float64)
			}
			*stats.EmbeddingCost += *embUsage.Cost
		}
		if err != nil {
			s.logger.Error("failed to get embedding for person", "name", added.DisplayName, "error", err)
			// Continue without embedding
		}

		person := storage.Person{
			UserID:       userID,
			DisplayName:  added.DisplayName,
			Aliases:      added.Aliases,
			Circle:       added.Circle,
			Bio:          added.Bio,
			Embedding:    emb,
			FirstSeen:    referenceDate,
			LastSeen:     referenceDate,
			MentionCount: 1,
		}

		if person.Circle == "" {
			person.Circle = "Other"
		}
		if person.Aliases == nil {
			person.Aliases = []string{}
		}

		personID, err := s.peopleRepo.AddPerson(person)
		if err != nil {
			// Check if this is a UNIQUE constraint error (person was added by concurrent process)
			if strings.Contains(err.Error(), "UNIQUE constraint") {
				// Find the existing person and update them instead
				existing, findErr := s.peopleRepo.FindPersonByName(userID, added.DisplayName)
				if findErr != nil || existing == nil {
					s.logger.Error("failed to find existing person after UNIQUE error", "name", added.DisplayName, "error", findErr)
					continue
				}

				// Merge bio if needed
				mergedBio := existing.Bio
				if added.Bio != "" && added.Bio != existing.Bio {
					if existing.Bio != "" {
						mergedBio = existing.Bio + " " + added.Bio
					} else {
						mergedBio = added.Bio
					}
				}

				// Update the existing person
				existing.Bio = mergedBio
				if added.Circle != "" && added.Circle != "Other" {
					existing.Circle = added.Circle
				}
				existing.LastSeen = referenceDate
				existing.MentionCount++
				existing.Embedding = emb // Use the new embedding

				if updateErr := s.peopleRepo.UpdatePerson(*existing); updateErr != nil {
					s.logger.Error("failed to update existing person", "name", added.DisplayName, "error", updateErr)
					continue
				}

				stats.Updated++
				s.logger.Info("Person updated (was UNIQUE conflict)", "id", existing.ID, "name", added.DisplayName, "reason", added.Reason)
				continue
			}

			s.logger.Error("failed to add person", "name", added.DisplayName, "error", err)
			continue
		}

		stats.Added++
		s.logger.Info("Person added", "id", personID, "name", added.DisplayName, "circle", added.Circle, "reason", added.Reason)
	}

	// Handle Updated people
	for _, updated := range peopleResult.Updated {
		// Try to extract person_id first (new format)
		personIDStr, hasPersonID := updated.GetPersonID()

		// Find existing person
		var existingPerson *storage.Person

		// Priority 1: Use person_id if provided
		if hasPersonID {
			personID, _ := strconv.ParseInt(personIDStr, 10, 64)
			for i := range currentPeople {
				if currentPeople[i].ID == personID {
					existingPerson = &currentPeople[i]
					s.logger.Debug("Found person by person_id", "person_id", personIDStr, "name", currentPeople[i].DisplayName)
					break
				}
			}
		}

		// Priority 2: Try exact match on DisplayName (fallback)
		if existingPerson == nil && updated.DisplayName != "" {
			for i := range currentPeople {
				if currentPeople[i].DisplayName == updated.DisplayName {
					existingPerson = &currentPeople[i]
					break
				}
			}

			// Fallback: strip aliases in parentheses (e.g., "John Smith (@john, Johnny)" -> "John Smith")
			if existingPerson == nil {
				cleanName := updated.DisplayName
				if idx := strings.Index(cleanName, " ("); idx > 0 {
					cleanName = strings.TrimSpace(cleanName[:idx])
				}
				if cleanName != updated.DisplayName {
					for i := range currentPeople {
						if currentPeople[i].DisplayName == cleanName {
							existingPerson = &currentPeople[i]
							s.logger.Debug("Found person by stripped name", "original", updated.DisplayName, "clean", cleanName)
							break
						}
					}
				}
			}

			// Fallback: try matching by alias
			if existingPerson == nil {
				for i := range currentPeople {
					for _, alias := range currentPeople[i].Aliases {
						if strings.EqualFold(alias, updated.DisplayName) || strings.Contains(updated.DisplayName, alias) {
							existingPerson = &currentPeople[i]
							s.logger.Debug("Found person by alias", "requested", updated.DisplayName, "found", currentPeople[i].DisplayName)
							break
						}
					}
					if existingPerson != nil {
						break
					}
				}
			}
		}

		if existingPerson == nil {
			if hasPersonID {
				s.logger.Warn("Person to update not found", "person_id", personIDStr)
			} else if updated.DisplayName != "" {
				s.logger.Warn("Person to update not found", "name", updated.DisplayName)
			} else {
				s.logger.Warn("Person to update not found", "person_id", "", "name", "")
			}
			continue
		}

		// Start with existing values
		person := storage.Person{
			ID:           existingPerson.ID,
			UserID:       userID,
			DisplayName:  existingPerson.DisplayName,
			Aliases:      existingPerson.Aliases,
			TelegramID:   existingPerson.TelegramID,
			Username:     existingPerson.Username,
			Circle:       existingPerson.Circle,
			Bio:          existingPerson.Bio,
			FirstSeen:    existingPerson.FirstSeen,
			LastSeen:     referenceDate,
			MentionCount: existingPerson.MentionCount + 1,
		}

		// Track if embedding-relevant fields changed
		needsReembed := false

		// Apply updates
		if updated.Bio != "" && updated.Bio != existingPerson.Bio {
			person.Bio = updated.Bio
			needsReembed = true
		}
		if updated.Circle != "" {
			person.Circle = updated.Circle
		}
		// Add new aliases from Archivist
		if len(updated.Aliases) > 0 {
			existingAliasSet := make(map[string]bool)
			for _, a := range person.Aliases {
				existingAliasSet[a] = true
			}
			for _, newAlias := range updated.Aliases {
				if !existingAliasSet[newAlias] && newAlias != person.DisplayName {
					person.Aliases = append(person.Aliases, newAlias)
					needsReembed = true
				}
			}
		}
		// Handle rename: new_display_name moves old name to aliases
		if updated.NewDisplayName != "" && updated.NewDisplayName != person.DisplayName {
			oldName := person.DisplayName
			person.DisplayName = updated.NewDisplayName
			// Add old name to aliases if not already there
			hasOldName := false
			for _, alias := range person.Aliases {
				if alias == oldName {
					hasOldName = true
					break
				}
			}
			if !hasOldName {
				person.Aliases = append(person.Aliases, oldName)
			}
			s.logger.Info("Person renamed", "id", person.ID, "old_name", oldName, "new_name", updated.NewDisplayName)
			needsReembed = true
		}

		// Regenerate embedding if any searchable field changed, or if person had no embedding
		if needsReembed || len(existingPerson.Embedding) == 0 {
			embText := buildPersonEmbeddingText(person.DisplayName, person.Username, person.Aliases, person.Bio)
			emb, embUsage, err := s.getEmbedding(ctx, embText)
			stats.EmbeddingTokens += embUsage.Tokens
			if embUsage.Cost != nil {
				if stats.EmbeddingCost == nil {
					stats.EmbeddingCost = new(float64)
				}
				*stats.EmbeddingCost += *embUsage.Cost
			}
			if err != nil {
				s.logger.Error("failed to get embedding for person update", "name", updated.DisplayName, "error", err)
				person.Embedding = existingPerson.Embedding // Keep old embedding on error
			} else {
				person.Embedding = emb
			}
		} else {
			person.Embedding = existingPerson.Embedding
		}

		if err := s.peopleRepo.UpdatePerson(person); err != nil {
			s.logger.Error("failed to update person", "name", updated.DisplayName, "error", err)
			continue
		}

		stats.Updated++
		s.logger.Info("Person updated", "id", person.ID, "name", person.DisplayName, "reason", updated.Reason)
	}

	// Handle Merged people
	for _, merged := range peopleResult.Merged {
		// Try to extract IDs first (new format)
		targetIDStr, hasTargetID := merged.GetTargetID()
		sourceIDStr, hasSourceID := merged.GetSourceID()

		// Find target and source persons
		var targetPerson, sourcePerson *storage.Person

		// Priority 1: Use person_id if provided
		if hasTargetID {
			targetID, _ := strconv.ParseInt(targetIDStr, 10, 64)
			for _, p := range currentPeople {
				if p.ID == targetID {
					targetPerson = &p
					break
				}
			}
		}
		if hasSourceID {
			sourceID, _ := strconv.ParseInt(sourceIDStr, 10, 64)
			for _, p := range currentPeople {
				if p.ID == sourceID {
					sourcePerson = &p
					break
				}
			}
		}

		// Priority 2: Fallback to name matching (for backward compatibility)
		if targetPerson == nil && merged.TargetName != "" {
			for _, p := range currentPeople {
				if p.DisplayName == merged.TargetName {
					targetPerson = &p
					break
				}
			}
		}
		if sourcePerson == nil && merged.SourceName != "" {
			for _, p := range currentPeople {
				if p.DisplayName == merged.SourceName {
					sourcePerson = &p
					break
				}
			}
		}

		if targetPerson == nil {
			if hasTargetID {
				s.logger.Warn("Target person for merge not found", "target_id", targetIDStr)
			} else {
				s.logger.Warn("Target person for merge not found", "target_name", merged.TargetName)
			}
			continue
		}
		if sourcePerson == nil {
			if hasSourceID {
				s.logger.Warn("Source person for merge not found", "source_id", sourceIDStr)
			} else {
				s.logger.Warn("Source person for merge not found", "source_name", merged.SourceName)
			}
			continue
		}

		// Combine bios
		newBio := targetPerson.Bio
		if sourcePerson.Bio != "" {
			if newBio != "" {
				newBio += " " + sourcePerson.Bio
			} else {
				newBio = sourcePerson.Bio
			}
		}

		// Combine aliases (include source name as alias)
		newAliases := append([]string{}, targetPerson.Aliases...)
		newAliases = append(newAliases, sourcePerson.DisplayName)
		newAliases = append(newAliases, sourcePerson.Aliases...)

		// Deduplicate aliases
		seen := make(map[string]bool)
		uniqueAliases := []string{}
		for _, alias := range newAliases {
			if !seen[alias] && alias != targetPerson.DisplayName {
				seen[alias] = true
				uniqueAliases = append(uniqueAliases, alias)
			}
		}

		// Merge username and telegram_id: prefer target's values if they exist, otherwise use source's
		var newUsername *string
		if targetPerson.Username != nil && *targetPerson.Username != "" {
			newUsername = targetPerson.Username // Keep target's username
		} else if sourcePerson.Username != nil && *sourcePerson.Username != "" {
			newUsername = sourcePerson.Username // Inherit from source
		}
		// If both are nil/empty, newUsername remains nil (no change)

		var newTelegramID *int64
		if targetPerson.TelegramID != nil && *targetPerson.TelegramID != 0 {
			newTelegramID = targetPerson.TelegramID // Keep target's telegram_id
		} else if sourcePerson.TelegramID != nil && *sourcePerson.TelegramID != 0 {
			newTelegramID = sourcePerson.TelegramID // Inherit from source
		}

		if err := s.peopleRepo.MergePeople(userID, targetPerson.ID, sourcePerson.ID, newBio, uniqueAliases, newUsername, newTelegramID); err != nil {
			s.logger.Error("failed to merge people", "target", merged.TargetName, "source", merged.SourceName, "error", err)
			continue
		}

		// Regenerate embedding for merged person (bio, aliases, and possibly username changed)
		// Use newUsername which may be from source if target didn't have one
		embUsername := newUsername
		if embUsername == nil {
			embUsername = targetPerson.Username // Fallback (will be nil if both are nil)
		}
		embText := buildPersonEmbeddingText(targetPerson.DisplayName, embUsername, uniqueAliases, newBio)
		emb, embUsage, err := s.getEmbedding(ctx, embText)
		stats.EmbeddingTokens += embUsage.Tokens
		if embUsage.Cost != nil {
			if stats.EmbeddingCost == nil {
				stats.EmbeddingCost = new(float64)
			}
			*stats.EmbeddingCost += *embUsage.Cost
		}
		if err != nil {
			s.logger.Error("failed to get embedding for merged person", "name", merged.TargetName, "error", err)
		} else {
			// Update just the embedding (and username/telegram_id for consistency)
			mergedPerson := *targetPerson
			mergedPerson.Bio = newBio
			mergedPerson.Aliases = uniqueAliases
			mergedPerson.Username = newUsername
			mergedPerson.TelegramID = newTelegramID
			mergedPerson.Embedding = emb
			if err := s.peopleRepo.UpdatePerson(mergedPerson); err != nil {
				s.logger.Error("failed to update merged person embedding", "name", merged.TargetName, "error", err)
			}
		}

		stats.Merged++
		s.logger.Info("People merged", "target", merged.TargetName, "source", merged.SourceName, "reason", merged.Reason)
	}

	return stats, nil
}

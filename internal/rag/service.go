package rag

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/storage"
)

// ProcessingStats contains detailed statistics about session processing.
type ProcessingStats struct {
	MessagesProcessed int `json:"messages_processed"`
	TopicsExtracted   int `json:"topics_extracted"`
	TopicsMerged      int `json:"topics_merged"`
	FactsCreated      int `json:"facts_created"`
	FactsUpdated      int `json:"facts_updated"`
	FactsDeleted      int `json:"facts_deleted"`
	// People stats (v0.5.1)
	PeopleAdded   int `json:"people_added"`
	PeopleUpdated int `json:"people_updated"`
	PeopleMerged  int `json:"people_merged"`
	// Usage stats from API calls
	PromptTokens     int      `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	EmbeddingTokens  int      `json:"embedding_tokens"`
	TotalCost        *float64 `json:"total_cost,omitempty"` // Cost in USD
}

// AddChatUsage adds usage from a chat completion response.
func (s *ProcessingStats) AddChatUsage(promptTokens, completionTokens int, cost *float64) {
	s.PromptTokens += promptTokens
	s.CompletionTokens += completionTokens
	if cost != nil {
		if s.TotalCost == nil {
			s.TotalCost = new(float64)
		}
		*s.TotalCost += *cost
	}
}

// AddEmbeddingUsage adds usage from an embedding response.
func (s *ProcessingStats) AddEmbeddingUsage(tokens int, cost *float64) {
	s.EmbeddingTokens += tokens
	if cost != nil {
		if s.TotalCost == nil {
			s.TotalCost = new(float64)
		}
		*s.TotalCost += *cost
	}
}

// UsageInfo holds usage data from a single API call.
type UsageInfo struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Cost             *float64
}

// ProgressEvent represents a progress update during processing.
type ProgressEvent struct {
	Stage    string           `json:"stage"`           // "topics", "consolidation", "facts"
	Current  int              `json:"current"`         // Current item being processed
	Total    int              `json:"total"`           // Total items to process
	Message  string           `json:"message"`         // Human-readable status message
	Complete bool             `json:"complete"`        // True when processing is complete
	Stats    *ProcessingStats `json:"stats,omitempty"` // Final stats when complete
}

// ProgressCallback is called during processing to report progress.
type ProgressCallback func(event ProgressEvent)

// TestMessageResult contains the result of a test message sent through the bot pipeline.
// Used by the debug chat interface to display detailed metrics.
type TestMessageResult struct {
	Response         string
	TimingTotal      time.Duration
	TimingEmbedding  time.Duration
	TimingSearch     time.Duration
	TimingLLM        time.Duration
	PromptTokens     int
	CompletionTokens int
	TotalCost        float64
	TopicsMatched    int
	FactsInjected    int
	ContextPreview   string
	RAGDebugInfo     *RetrievalDebugInfo
}

// Service provides RAG (Retrieval-Augmented Generation) functionality.
type Service struct {
	logger               *slog.Logger
	cfg                  *config.Config
	topicRepo            storage.TopicRepository
	factRepo             storage.FactRepository
	factHistoryRepo      storage.FactHistoryRepository
	msgRepo              storage.MessageRepository
	maintenanceRepo      storage.MaintenanceRepository
	peopleRepo           storage.PeopleRepository // v0.5.1: People repository
	artifactRepo         storage.ArtifactRepository
	userRepo             storage.UserRepository  // background loops enumerate real tenants (transport-agnostic)
	scopeRepo            storage.ScopeRepository // channel-scope detection (Phase 6); nil without a resolver (Telegram-only path)
	client               llm.Client
	memoryService        MemoryService
	translator           *i18n.Translator
	agentLogger          *agentlog.Logger
	enricherAgent        agent.Agent           // Query enrichment agent
	splitterAgent        agent.Agent           // Topic splitting agent
	mergerAgent          agent.Agent           // Topic merging agent
	rerankerAgent        agent.Agent           // Topic reranking agent
	extractorAgent       agent.Agent           // Artifact extraction agent (Phase 3)
	vectors              VectorStore           // Vector index for topics/facts/people/artifacts
	contextService       *agent.ContextService // Context service for loading user context
	stopChan             chan struct{}
	consolidationTrigger chan struct{}
	wg                   sync.WaitGroup
	processingTopics     sync.Map             // Track topics currently being processed for facts (prevents race condition)
	processingUsers      sync.Map             // Track users currently being processed (prevents concurrent people merges)
	processingArtifacts  sync.Map             // Track artifacts currently being processed (prevents concurrent extraction)
	chunkBreaker         *chunkCircuitBreaker // Exponential-backoff cooldown for persistently-failing chunks

	// Shutdown coordination (v0.6.0 - CRIT-2)
	shuttingDown      atomic.Bool // Signal to stop accepting new work
	inFlightArtifacts sync.Map    // artifactID -> time.Time for visibility during shutdown
}

// SetAgentLogger sets the agent logger for debugging LLM calls.
func (s *Service) SetAgentLogger(logger *agentlog.Logger) {
	s.agentLogger = logger
}

// SetEnricherAgent sets the query enrichment agent.
func (s *Service) SetEnricherAgent(a agent.Agent) {
	s.enricherAgent = a
}

// SetSplitterAgent sets the topic splitting agent.
func (s *Service) SetSplitterAgent(a agent.Agent) {
	s.splitterAgent = a
}

// SetMergerAgent sets the topic merging agent.
func (s *Service) SetMergerAgent(a agent.Agent) {
	s.mergerAgent = a
}

// SetRerankerAgent sets the topic reranking agent.
func (s *Service) SetRerankerAgent(a agent.Agent) {
	s.rerankerAgent = a
}

// SetExtractorAgent sets the artifact extraction agent.
func (s *Service) SetExtractorAgent(a agent.Agent) {
	s.extractorAgent = a
}

// SetArtifactRepository sets the artifact repository.
func (s *Service) SetArtifactRepository(artifactRepo storage.ArtifactRepository) {
	s.artifactRepo = artifactRepo
}

// tryStartProcessingTopic attempts to mark a topic as being processed.
// Returns true if this goroutine should process it, false if another is already processing.
func (s *Service) tryStartProcessingTopic(topicID int64) bool {
	_, alreadyProcessing := s.processingTopics.LoadOrStore(topicID, true)
	return !alreadyProcessing
}

// finishProcessingTopic marks a topic as no longer being processed.
func (s *Service) finishProcessingTopic(topicID int64) {
	s.processingTopics.Delete(topicID)
}

// tryStartProcessingUser attempts to mark a user as being processed for fact extraction.
// Returns true if this goroutine should process, false if another is already processing.
// This prevents concurrent people merges for the same user (BUG-013).
func (s *Service) tryStartProcessingUser(userID storage.ScopeID) bool {
	_, alreadyProcessing := s.processingUsers.LoadOrStore(userID, true)
	return !alreadyProcessing
}

// finishProcessingUser marks a user as no longer being processed for fact extraction.
func (s *Service) finishProcessingUser(userID storage.ScopeID) {
	s.processingUsers.Delete(userID)
}

// tryStartProcessingArtifact attempts to mark an artifact as being processed.
// Returns true if this goroutine should process it, false if another is already processing.
func (s *Service) tryStartProcessingArtifact(artifactID int64) bool {
	_, alreadyProcessing := s.processingArtifacts.LoadOrStore(artifactID, true)
	return !alreadyProcessing
}

// finishProcessingArtifact marks an artifact as no longer being processed.
func (s *Service) finishProcessingArtifact(artifactID int64) {
	s.processingArtifacts.Delete(artifactID)
}

// SetPeopleRepository sets the people repository for person search (v0.5.1).
func (s *Service) SetPeopleRepository(pr storage.PeopleRepository) {
	s.peopleRepo = pr
}

// SetContextService sets the context service for loading user context.
func (s *Service) SetContextService(cs *agent.ContextService) {
	s.contextService = cs
}

// Start initializes and starts the RAG service background loops.
func (s *Service) Start(ctx context.Context) error {
	if !s.cfg.RAG.Enabled {
		s.logger.Info("RAG is disabled")
		return nil
	}

	s.logger.Info("Starting RAG service...")

	// 0. Re-embed any rows produced under a different model/dim. Blocks
	//    startup (webhook/TG updates come later in main.go) — the full
	//    prod DB re-embeds in ~45 seconds at parallelism=8, see
	//    docs/architecture/embedding-storage.md.
	if err := s.ReembedIfNeeded(ctx); err != nil {
		s.logger.Error("embedding migration failed", "error", err)
		// Continue: bot still works with mixed-version vectors, but
		// cross-version cosine comparisons will return meaningless scores.
		// Operator should investigate before the next restart.
	}

	// 1. Load existing vectors (topics + facts + people)
	if err := s.ReloadVectors(); err != nil {
		s.logger.Error("failed to load vectors", "error", err)
	}

	// 2. Start background chunking/topic extraction
	s.wg.Add(1)
	go s.backgroundLoop(ctx)

	// 3. Start background fact extraction
	s.wg.Add(1)
	go s.factExtractionLoop(ctx)

	// 4. Start background consolidation loop
	s.wg.Add(1)
	go s.runConsolidationLoop(ctx)

	// 5. Start background artifact extraction (Phase 3, v0.6.0)
	if s.cfg.Artifacts.Enabled {
		s.wg.Add(1)
		go s.artifactExtractionLoop(ctx)
		s.logger.Info("Artifact extraction started")
	}

	return nil
}

// Stop gracefully shuts down the RAG service.
// Signals to stop accepting new work and waits for in-flight operations to complete.
func (s *Service) Stop() {
	// Signal to stop accepting new work (v0.6.0 - CRIT-2)
	s.shuttingDown.Store(true)

	// Log in-flight artifacts for visibility
	inFlightCount := 0
	s.inFlightArtifacts.Range(func(key, value interface{}) bool {
		artifactID := key.(int64)
		startTime := value.(time.Time)
		s.logger.Info("waiting for in-flight artifact",
			"artifact_id", artifactID,
			"running_for", time.Since(startTime).Round(time.Second).String(),
		)
		inFlightCount++
		return true
	})

	if inFlightCount > 0 {
		s.logger.Info("shutdown: waiting for in-flight work to complete",
			"in_flight_artifacts", inFlightCount,
		)
	}

	close(s.stopChan)
	s.wg.Wait()

	s.logger.Info("RAG service stopped")
}

// TriggerConsolidation triggers a consolidation run.
func (s *Service) TriggerConsolidation() {
	select {
	case s.consolidationTrigger <- struct{}{}:
	default:
		// Already triggered
	}
}

// GetRecentTopics returns the N most recent topics for a user with message counts.
func (s *Service) GetRecentTopics(userID storage.ScopeID, limit int) ([]storage.TopicExtended, error) {
	if limit <= 0 {
		return nil, nil
	}
	filter := storage.TopicFilter{UserID: userID}
	result, err := s.topicRepo.GetTopicsExtended(filter, limit, 0, "created_at", "DESC")
	if err != nil {
		return nil, err
	}
	return result.Data, nil
}

// ReloadVectors fully reloads all topic, fact, and people vectors from the database.
func (s *Service) ReloadVectors() error {
	// Full reload on startup - load all topics, facts, and people
	topics, err := s.topicRepo.GetAllTopics()
	if err != nil {
		return fmt.Errorf("load topics: %w", err)
	}

	facts, err := s.factRepo.GetAllFacts()
	if err != nil {
		return fmt.Errorf("load facts: %w", err)
	}

	// v0.5.1: Load people vectors
	var people []storage.Person
	if s.peopleRepo != nil {
		people, err = s.peopleRepo.GetAllPeople()
		if err != nil {
			s.logger.Warn("failed to load people for vector index", "error", err)
			people = nil // Continue without people
		}
	}

	topicEntries, tCount := collectVectorEntries(topics, func(t storage.Topic) (storage.ScopeID, int64, []float32) {
		return t.UserID, t.ID, t.Embedding
	})
	factEntries, fCount := collectVectorEntries(facts, func(f storage.Fact) (storage.ScopeID, int64, []float32) {
		return f.UserID, f.ID, f.Embedding
	})
	peopleEntries, pCount := collectVectorEntries(people, func(p storage.Person) (storage.ScopeID, int64, []float32) {
		return p.UserID, p.ID, p.Embedding
	})

	s.vectors.ReplaceAll(VectorKindTopics, topicEntries)
	s.vectors.ReplaceAll(VectorKindFacts, factEntries)
	s.vectors.ReplaceAll(VectorKindPeople, peopleEntries)
	s.vectors.ReplaceAll(VectorKindArtifacts, nil) // reset watermark; reloaded below

	// v0.6.0: Load artifact summaries BEFORE logging (so we can include count)
	artifactCount := 0
	if err := s.LoadNewArtifactSummaries(); err != nil {
		s.logger.Warn("failed to load new artifact summaries", "error", err)
		// Continue without artifact summaries - not critical
	} else {
		artifactCount = s.vectors.Count(VectorKindArtifacts)
	}

	s.logger.Info("Loaded vectors (full)", "topics", tCount, "facts", fCount, "people", pCount, "artifacts", artifactCount)
	UpdateVectorIndexMetrics(tCount, fCount, pCount, s.cfg.Embedding.Dimensions)

	return nil
}

// LoadNewVectors incrementally loads only new topics, facts, and people since last load.
func (s *Service) LoadNewVectors() error {
	minTopicID := s.vectors.MaxID(VectorKindTopics)
	minFactID := s.vectors.MaxID(VectorKindFacts)
	minPersonID := s.vectors.MaxID(VectorKindPeople)

	// Load only new topics
	topics, err := s.topicRepo.GetTopicsAfterID(minTopicID)
	if err != nil {
		return fmt.Errorf("load new topics: %w", err)
	}

	// Load only new facts
	facts, err := s.factRepo.GetFactsAfterID(minFactID)
	if err != nil {
		return fmt.Errorf("load new facts: %w", err)
	}

	// v0.5.1: Load only new people
	var people []storage.Person
	if s.peopleRepo != nil {
		people, err = s.peopleRepo.GetPeopleAfterID(minPersonID)
		if err != nil {
			s.logger.Warn("failed to load new people", "error", err)
			people = nil
		}
	}

	// Early return if nothing new
	if len(topics) == 0 && len(facts) == 0 && len(people) == 0 {
		return nil
	}

	topicEntries, _ := collectVectorEntries(topics, func(t storage.Topic) (storage.ScopeID, int64, []float32) {
		return t.UserID, t.ID, t.Embedding
	})
	factEntries, _ := collectVectorEntries(facts, func(f storage.Fact) (storage.ScopeID, int64, []float32) {
		return f.UserID, f.ID, f.Embedding
	})
	peopleEntries, _ := collectVectorEntries(people, func(p storage.Person) (storage.ScopeID, int64, []float32) {
		return p.UserID, p.ID, p.Embedding
	})

	// AppendSince skips a kind (returns 0) if another goroutine already
	// advanced its watermark while we were fetching.
	tCount := s.vectors.AppendSince(VectorKindTopics, minTopicID, topicEntries)
	fCount := s.vectors.AppendSince(VectorKindFacts, minFactID, factEntries)
	pCount := s.vectors.AppendSince(VectorKindPeople, minPersonID, peopleEntries)

	if tCount > 0 || fCount > 0 || pCount > 0 {
		s.logger.Info("Loaded vectors (incremental)", "new_topics", tCount, "new_facts", fCount, "new_people", pCount)
		UpdateVectorIndexMetrics(
			s.vectors.Count(VectorKindTopics),
			s.vectors.Count(VectorKindFacts),
			s.vectors.Count(VectorKindPeople),
			s.cfg.Embedding.Dimensions,
		)
	}

	// v0.6.0: Load new artifact summaries (incremental)
	if err := s.LoadNewArtifactSummaries(); err != nil {
		s.logger.Warn("failed to load new artifact summaries", "error", err)
		// Continue without artifact summaries - not critical
	}

	return nil
}

// LoadNewArtifactSummaries incrementally loads only new artifact summary embeddings.
// Called after artifact processing completes.
func (s *Service) LoadNewArtifactSummaries() error {
	if s.artifactRepo == nil {
		s.logger.Debug("artifactRepo is nil, skipping artifact loading")
		return nil
	}

	minArtifactID := s.vectors.MaxID(VectorKindArtifacts)

	// Load artifacts per-user (same pattern as topics/facts/people for data isolation)
	users := s.backgroundUserIDs()
	if len(users) == 0 {
		return nil
	}

	entries := make(map[storage.ScopeID][]VectorEntry)
	for _, userID := range users {
		filter := storage.ArtifactFilter{UserID: userID, State: "ready"}
		artifacts, _, err := s.artifactRepo.GetArtifacts(filter, 1000, 0)
		if err != nil {
			s.logger.Error("LoadNewArtifactSummaries: query failed", "user_id", userID, "error", err)
			continue // Skip this user, try next
		}

		// Incremental load: append new artifacts only
		for _, artifact := range artifacts {
			if len(artifact.Embedding) > 0 && artifact.ID > minArtifactID {
				entries[artifact.UserID] = append(entries[artifact.UserID], VectorEntry{
					ID:        artifact.ID,
					Embedding: artifact.Embedding,
				})
			}
		}
	}

	count := s.vectors.AppendSince(VectorKindArtifacts, minArtifactID, entries)

	UpdateArtifactSummaryMetrics(s.vectors.CountByUser(VectorKindArtifacts))

	if count > 0 {
		s.logger.Info("Loaded artifact summaries (incremental)", "new_artifacts", count, "max_id", s.vectors.MaxID(VectorKindArtifacts))
	} else {
		s.logger.Debug("No new artifact summaries to load", "min_id", minArtifactID)
	}

	return nil
}

// backgroundUserIDs returns the memory tenants the background loops should
// process: the union of the configured allowlist (Telegram) and every user
// that exists in storage. The union covers minted scope ids on non-Telegram
// transports (where Bot.AllowedUserIDs is empty) while never narrowing the Telegram
// path — allowlisted users are always included even before they appear in the
// users table. WARNING: GetAllUsers is cross-user; this is background-only.
func (s *Service) backgroundUserIDs() []storage.ScopeID {
	seen := make(map[storage.ScopeID]struct{})
	var ids []storage.ScopeID
	add := func(id storage.ScopeID) {
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, id := range s.cfg.Bot.AllowedUserIDs {
		add(storage.PassthroughScopeID("telegram", strconv.FormatInt(id, 10)))
	}
	if s.userRepo != nil {
		users, err := s.userRepo.GetAllUsers()
		if err != nil {
			s.logger.Warn("backgroundUserIDs: GetAllUsers failed, using allowlist only", "error", err)
		} else {
			for _, u := range users {
				add(u.ID)
			}
		}
	}
	return ids
}

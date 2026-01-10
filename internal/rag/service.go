package rag

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// TopicVectorItem stores a topic's embedding for vector search.
type TopicVectorItem struct {
	TopicID   int64
	Embedding []float32
}

// FactVectorItem stores a fact's embedding for vector search.
type FactVectorItem struct {
	FactID    int64
	Embedding []float32
}

// ProcessingStats contains detailed statistics about session processing.
type ProcessingStats struct {
	MessagesProcessed int `json:"messages_processed"`
	TopicsExtracted   int `json:"topics_extracted"`
	TopicsMerged      int `json:"topics_merged"`
	FactsCreated      int `json:"facts_created"`
	FactsUpdated      int `json:"facts_updated"`
	FactsDeleted      int `json:"facts_deleted"`
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
	client               openrouter.Client
	memoryService        *memory.Service
	translator           *i18n.Translator
	agentLogger          *agentlog.Logger
	enricherAgent        agent.Agent                 // Query enrichment agent
	splitterAgent        agent.Agent                 // Topic splitting agent
	mergerAgent          agent.Agent                 // Topic merging agent
	topicVectors         map[int64][]TopicVectorItem // UserID -> []TopicVectorItem
	factVectors          map[int64][]FactVectorItem  // UserID -> []FactVectorItem
	maxLoadedTopicID     int64                       // Track max loaded topic ID for incremental loading
	maxLoadedFactID      int64                       // Track max loaded fact ID for incremental loading
	mu                   sync.RWMutex
	stopChan             chan struct{}
	consolidationTrigger chan struct{}
	wg                   sync.WaitGroup
}

// NewService creates a new RAG service.
func NewService(logger *slog.Logger, cfg *config.Config, topicRepo storage.TopicRepository, factRepo storage.FactRepository, factHistoryRepo storage.FactHistoryRepository, msgRepo storage.MessageRepository, maintenanceRepo storage.MaintenanceRepository, client openrouter.Client, memoryService *memory.Service, translator *i18n.Translator) *Service {
	return &Service{
		logger:               logger.With("component", "rag"),
		cfg:                  cfg,
		topicRepo:            topicRepo,
		factRepo:             factRepo,
		factHistoryRepo:      factHistoryRepo,
		msgRepo:              msgRepo,
		maintenanceRepo:      maintenanceRepo,
		client:               client,
		memoryService:        memoryService,
		translator:           translator,
		topicVectors:         make(map[int64][]TopicVectorItem),
		factVectors:          make(map[int64][]FactVectorItem),
		stopChan:             make(chan struct{}),
		consolidationTrigger: make(chan struct{}, 1),
	}
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

// Start initializes and starts the RAG service background loops.
func (s *Service) Start(ctx context.Context) error {
	if !s.cfg.RAG.Enabled {
		s.logger.Info("RAG is disabled")
		return nil
	}

	s.logger.Info("Starting RAG service...")

	// 1. Load existing vectors (topics + facts)
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

	return nil
}

// Stop gracefully shuts down the RAG service.
func (s *Service) Stop() {
	close(s.stopChan)
	s.wg.Wait()
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
func (s *Service) GetRecentTopics(userID int64, limit int) ([]storage.TopicExtended, error) {
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

// ReloadVectors fully reloads all topic and fact vectors from the database.
func (s *Service) ReloadVectors() error {
	// Full reload on startup - load all topics and facts
	topics, err := s.topicRepo.GetAllTopics()
	if err != nil {
		return fmt.Errorf("load topics: %w", err)
	}

	facts, err := s.factRepo.GetAllFacts()
	if err != nil {
		return fmt.Errorf("load facts: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Reset maps for full reload
	s.topicVectors = make(map[int64][]TopicVectorItem)
	s.factVectors = make(map[int64][]FactVectorItem)
	s.maxLoadedTopicID = 0
	s.maxLoadedFactID = 0

	tCount := 0
	for _, t := range topics {
		if len(t.Embedding) > 0 {
			s.topicVectors[t.UserID] = append(s.topicVectors[t.UserID], TopicVectorItem{
				TopicID:   t.ID,
				Embedding: t.Embedding,
			})
			tCount++
			if t.ID > s.maxLoadedTopicID {
				s.maxLoadedTopicID = t.ID
			}
		}
	}

	fCount := 0
	for _, f := range facts {
		if len(f.Embedding) > 0 {
			s.factVectors[f.UserID] = append(s.factVectors[f.UserID], FactVectorItem{
				FactID:    f.ID,
				Embedding: f.Embedding,
			})
			fCount++
			if f.ID > s.maxLoadedFactID {
				s.maxLoadedFactID = f.ID
			}
		}
	}

	s.logger.Info("Loaded vectors (full)", "topics", tCount, "facts", fCount)
	UpdateVectorIndexMetrics(tCount, fCount)
	return nil
}

// LoadNewVectors incrementally loads only new topics and facts since last load.
func (s *Service) LoadNewVectors() error {
	s.mu.RLock()
	minTopicID := s.maxLoadedTopicID
	minFactID := s.maxLoadedFactID
	s.mu.RUnlock()

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

	// Early return if nothing new
	if len(topics) == 0 && len(facts) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if another goroutine already loaded these vectors while we were fetching
	if minTopicID < s.maxLoadedTopicID || minFactID < s.maxLoadedFactID {
		s.logger.Debug("Skipping incremental load - already loaded by another goroutine")
		return nil
	}

	tCount := 0
	for _, t := range topics {
		if len(t.Embedding) > 0 {
			s.topicVectors[t.UserID] = append(s.topicVectors[t.UserID], TopicVectorItem{
				TopicID:   t.ID,
				Embedding: t.Embedding,
			})
			tCount++
			if t.ID > s.maxLoadedTopicID {
				s.maxLoadedTopicID = t.ID
			}
		}
	}

	fCount := 0
	for _, f := range facts {
		if len(f.Embedding) > 0 {
			s.factVectors[f.UserID] = append(s.factVectors[f.UserID], FactVectorItem{
				FactID:    f.ID,
				Embedding: f.Embedding,
			})
			fCount++
			if f.ID > s.maxLoadedFactID {
				s.maxLoadedFactID = f.ID
			}
		}
	}

	if tCount > 0 || fCount > 0 {
		s.logger.Info("Loaded vectors (incremental)", "new_topics", tCount, "new_facts", fCount)
		// Calculate total counts for metrics
		totalTopics := 0
		for _, vectors := range s.topicVectors {
			totalTopics += len(vectors)
		}
		totalFacts := 0
		for _, vectors := range s.factVectors {
			totalFacts += len(vectors)
		}
		UpdateVectorIndexMetrics(totalTopics, totalFacts)
	}
	return nil
}

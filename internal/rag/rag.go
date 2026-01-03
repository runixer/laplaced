package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

type TopicVectorItem struct {
	TopicID   int64
	Embedding []float32
}

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

type Service struct {
	logger               *slog.Logger
	cfg                  *config.Config
	topicRepo            storage.TopicRepository
	factRepo             storage.FactRepository
	factHistoryRepo      storage.FactHistoryRepository
	msgRepo              storage.MessageRepository
	logRepo              storage.LogRepository
	client               openrouter.Client
	memoryService        *memory.Service
	translator           *i18n.Translator
	topicVectors         map[int64][]TopicVectorItem // UserID -> []TopicVectorItem
	factVectors          map[int64][]FactVectorItem  // UserID -> []FactVectorItem
	maxLoadedTopicID     int64                       // Track max loaded topic ID for incremental loading
	maxLoadedFactID      int64                       // Track max loaded fact ID for incremental loading
	mu                   sync.RWMutex
	stopChan             chan struct{}
	consolidationTrigger chan struct{}
	wg                   sync.WaitGroup
}

func NewService(logger *slog.Logger, cfg *config.Config, topicRepo storage.TopicRepository, factRepo storage.FactRepository, factHistoryRepo storage.FactHistoryRepository, msgRepo storage.MessageRepository, logRepo storage.LogRepository, client openrouter.Client, memoryService *memory.Service, translator *i18n.Translator) *Service {
	return &Service{
		logger:               logger.With("component", "rag"),
		cfg:                  cfg,
		topicRepo:            topicRepo,
		factRepo:             factRepo,
		factHistoryRepo:      factHistoryRepo,
		msgRepo:              msgRepo,
		logRepo:              logRepo,
		client:               client,
		memoryService:        memoryService,
		translator:           translator,
		topicVectors:         make(map[int64][]TopicVectorItem),
		factVectors:          make(map[int64][]FactVectorItem),
		stopChan:             make(chan struct{}),
		consolidationTrigger: make(chan struct{}, 1),
	}
}

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

func (s *Service) Stop() {
	close(s.stopChan)
	s.wg.Wait()
}

func (s *Service) TriggerConsolidation() {
	select {
	case s.consolidationTrigger <- struct{}{}:
	default:
		// Already triggered
	}
}

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

// RetrievalDebugInfo contains trace data for RAG debugging
type RetrievalDebugInfo struct {
	OriginalQuery    string
	EnrichedQuery    string
	EnrichmentPrompt string
	EnrichmentTokens int
	Results          []TopicSearchResult
}

// TopicSearchResult represents a matched topic with its messages
type TopicSearchResult struct {
	Topic    storage.Topic
	Score    float32
	Messages []storage.Message
}

// SearchResult kept for backward compatibility if needed, but we are moving to TopicSearchResult.
// We remove it to force type errors in other files to fix them.
type SearchResult struct {
	Message storage.Message
	Score   float32
}

type RetrievalOptions struct {
	History        []storage.Message
	SkipEnrichment bool
}

func (s *Service) Retrieve(ctx context.Context, userID int64, query string, opts *RetrievalOptions) ([]TopicSearchResult, *RetrievalDebugInfo, error) {
	startTime := time.Now()
	debugInfo := &RetrievalDebugInfo{
		OriginalQuery: query,
	}

	if !s.cfg.RAG.Enabled {
		return nil, debugInfo, nil
	}

	if opts == nil {
		opts = &RetrievalOptions{}
	}

	var enrichedQuery string
	if opts.SkipEnrichment {
		enrichedQuery = query
	} else {
		// Enrich query
		var err error
		var prompt string
		var tokens int
		enrichStart := time.Now()
		enrichedQuery, prompt, tokens, err = s.enrichQuery(ctx, userID, query, opts.History)
		RecordRAGEnrichment(userID, time.Since(enrichStart).Seconds())
		debugInfo.EnrichmentPrompt = prompt
		debugInfo.EnrichmentTokens = tokens

		if err != nil {
			s.logger.Error("failed to enrich query", "error", err)
			enrichedQuery = query
		} else {
			s.logger.Debug("Enriched query", "original", query, "new", enrichedQuery)
		}
	}
	debugInfo.EnrichedQuery = enrichedQuery

	// Embedding for query
	embeddingStart := time.Now()
	resp, err := s.client.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
		Model: s.cfg.RAG.EmbeddingModel,
		Input: []string{enrichedQuery},
	})
	embeddingDuration := time.Since(embeddingStart).Seconds()
	if err != nil {
		RecordEmbeddingRequest(userID, s.cfg.RAG.EmbeddingModel, searchTypeTopics, embeddingDuration, false, 0, nil)
		return nil, debugInfo, err
	}
	if len(resp.Data) == 0 {
		RecordEmbeddingRequest(userID, s.cfg.RAG.EmbeddingModel, searchTypeTopics, embeddingDuration, false, 0, nil)
		return nil, debugInfo, fmt.Errorf("no embedding returned")
	}
	RecordEmbeddingRequest(userID, s.cfg.RAG.EmbeddingModel, searchTypeTopics, embeddingDuration, true, resp.Usage.TotalTokens, resp.Usage.Cost)
	qVec := resp.Data[0].Embedding

	// Search topics
	searchStart := time.Now()
	s.mu.RLock()
	type match struct {
		topicID int64
		score   float32
	}
	var matches []match
	userVectors := s.topicVectors[userID]
	vectorsScanned := len(userVectors)
	// Use a minimal safety threshold to avoid complete garbage, but much lower than strict content matching
	minSafetyThreshold := s.cfg.RAG.MinSafetyThreshold
	if minSafetyThreshold == 0 {
		minSafetyThreshold = 0.1
	}

	for _, item := range userVectors {
		score := cosineSimilarity(qVec, item.Embedding)
		// Relaxed logic: Collect everything above minimal safety threshold
		if float64(score) > minSafetyThreshold {
			matches = append(matches, match{topicID: item.TopicID, score: score})
		}
	}
	s.mu.RUnlock()
	RecordVectorSearch(userID, searchTypeTopics, time.Since(searchStart).Seconds(), vectorsScanned)
	RecordRAGCandidates(userID, searchTypeTopics, len(matches))

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})

	// Limit topics to search.
	maxTopics := s.cfg.RAG.RetrievedTopicsCount
	if maxTopics <= 0 {
		maxTopics = 10 // Increased default
	}

	// If explicit threshold is configured and high, we might want to respect it optionally?
	// But user requested "return top 10", so we prioritize count over threshold strictness.
	// We will simply take top N.

	if len(matches) > maxTopics {
		matches = matches[:maxTopics]
	}

	// Collect topic IDs from matches
	topicIDs := make([]int64, len(matches))
	for i, m := range matches {
		topicIDs[i] = m.topicID
	}

	// Fetch only the topics we need
	topics, err := s.topicRepo.GetTopicsByIDs(topicIDs)
	if err != nil {
		s.logger.Error("failed to load topics for retrieval", "error", err)
		return nil, debugInfo, err
	}
	topicMap := make(map[int64]storage.Topic)
	for _, t := range topics {
		topicMap[t.ID] = t
	}

	var results []TopicSearchResult
	seenMsgIDs := make(map[int64]bool)

	for _, m := range matches {
		topic, ok := topicMap[m.topicID]
		if !ok {
			continue
		}

		msgs, err := s.msgRepo.GetMessagesInRange(ctx, userID, topic.StartMsgID, topic.EndMsgID)
		if err != nil {
			s.logger.Error("failed to get messages for topic", "topic_id", topic.ID, "error", err)
			continue
		}

		var topicMsgs []storage.Message
		for _, msg := range msgs {
			if !seenMsgIDs[msg.ID] {
				seenMsgIDs[msg.ID] = true
				topicMsgs = append(topicMsgs, msg)
			}
		}

		if len(topicMsgs) > 0 {
			results = append(results, TopicSearchResult{
				Topic:    topic,
				Score:    m.score,
				Messages: topicMsgs,
			})
		}
	}

	debugInfo.Results = results

	// Record RAG metrics
	RecordRAGRetrieval(userID, len(results) > 0)
	RecordRAGLatency(userID, time.Since(startTime).Seconds())

	return results, debugInfo, nil
}

func (s *Service) backgroundLoop(ctx context.Context) {
	defer s.wg.Done()
	interval, err := time.ParseDuration(s.cfg.RAG.BackfillInterval) // We can reuse this or use ChunkInterval
	if err != nil {
		interval = 1 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial run
	s.processAllUsers(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.processAllUsers(ctx)
		}
	}
}

func (s *Service) processAllUsers(ctx context.Context) {
	users := s.cfg.Bot.AllowedUserIDs
	for _, userID := range users {
		if ctx.Err() != nil {
			return
		}
		s.processTopicChunking(ctx, userID)
	}
}

func (s *Service) processTopicChunking(ctx context.Context, userID int64) {
	chunkIntervalConfig := s.cfg.RAG.ChunkInterval
	if chunkIntervalConfig == "" {
		chunkIntervalConfig = "5h"
	}
	chunkDuration, err := time.ParseDuration(chunkIntervalConfig)
	if err != nil {
		chunkDuration = 5 * time.Hour
	}

	// 1. Fetch unprocessed messages (topic_id IS NULL)
	messages, err := s.msgRepo.GetUnprocessedMessages(userID)
	if err != nil {
		s.logger.Error("failed to fetch unprocessed messages", "error", err)
		return
	}

	if len(messages) == 0 {
		return
	}

	// 2. Identify chunks
	maxChunkSize := s.cfg.RAG.MaxChunkSize
	if maxChunkSize == 0 {
		maxChunkSize = 400
	}
	var currentChunk []storage.Message
	if len(messages) > 0 {
		currentChunk = append(currentChunk, messages[0])
	}

	for i := 1; i < len(messages); i++ {
		prev := messages[i-1]
		curr := messages[i]

		// Time diff
		diff := curr.CreatedAt.Sub(prev.CreatedAt)

		if diff >= chunkDuration || len(currentChunk) >= maxChunkSize {
			// End of chunk found
			if ctx.Err() != nil {
				return
			}

			runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			err := s.processChunk(runCtx, userID, currentChunk)
			cancel()

			if err != nil {
				s.logger.Error("failed to process chunk", "error", err)
				// If failed, we return to retry later.
				return
			}
			currentChunk = []storage.Message{curr}
		} else {
			currentChunk = append(currentChunk, curr)
		}
	}

	// Process final chunk if enough time passed
	if len(currentChunk) > 0 {
		lastMsg := currentChunk[len(currentChunk)-1]
		if time.Since(lastMsg.CreatedAt) > chunkDuration {
			if ctx.Err() != nil {
				return
			}

			runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			err := s.processChunk(runCtx, userID, currentChunk)
			cancel()

			if err != nil {
				s.logger.Error("failed to process final chunk", "error", err)
			}
		}
	}
}

func (s *Service) ForceProcessUser(ctx context.Context, userID int64) (int, error) {
	// 1. Fetch unprocessed messages
	messages, err := s.msgRepo.GetUnprocessedMessages(userID)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch unprocessed messages: %w", err)
	}

	if len(messages) == 0 {
		return 0, nil
	}

	// 2. Process all messages in chunks
	maxChunkSize := s.cfg.RAG.MaxChunkSize
	if maxChunkSize == 0 {
		maxChunkSize = 400
	}

	processedCount := 0
	var currentChunk []storage.Message

	for _, msg := range messages {
		currentChunk = append(currentChunk, msg)
		if len(currentChunk) >= maxChunkSize {
			if err := s.processChunk(ctx, userID, currentChunk); err != nil {
				return processedCount, fmt.Errorf("failed to process chunk: %w", err)
			}
			processedCount += len(currentChunk)
			currentChunk = []storage.Message{}
		}
	}

	// Process remaining messages
	if len(currentChunk) > 0 {
		if err := s.processChunk(ctx, userID, currentChunk); err != nil {
			return processedCount, fmt.Errorf("failed to process final chunk: %w", err)
		}
		processedCount += len(currentChunk)
	}

	return processedCount, nil
}

// ForceProcessUserWithProgress processes all unprocessed messages for a user with progress reporting.
// Unlike ForceProcessUser, this runs consolidation and fact extraction synchronously.
func (s *Service) ForceProcessUserWithProgress(ctx context.Context, userID int64, onProgress ProgressCallback) (*ProcessingStats, error) {
	stats := &ProcessingStats{}

	// 1. Fetch unprocessed messages
	messages, err := s.msgRepo.GetUnprocessedMessages(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch unprocessed messages: %w", err)
	}

	if len(messages) == 0 {
		onProgress(ProgressEvent{
			Stage:    "complete",
			Complete: true,
			Message:  "No messages to process",
			Stats:    stats,
		})
		return stats, nil
	}

	stats.MessagesProcessed = len(messages)

	// 2. Extract topics
	onProgress(ProgressEvent{
		Stage:   "topics",
		Current: 0,
		Total:   1,
		Message: "Extracting topics from messages...",
	})

	topicIDs, err := s.processChunkWithStats(ctx, userID, messages, stats)
	if err != nil {
		return stats, fmt.Errorf("failed to extract topics: %w", err)
	}
	stats.TopicsExtracted = len(topicIDs)

	onProgress(ProgressEvent{
		Stage:   "topics",
		Current: 1,
		Total:   1,
		Message: fmt.Sprintf("Extracted %d topics", len(topicIDs)),
	})

	// 3. Run consolidation synchronously
	onProgress(ProgressEvent{
		Stage:   "consolidation",
		Current: 0,
		Total:   1,
		Message: "Checking for similar topics to merge...",
	})

	mergeCount := s.runConsolidationSync(ctx, userID, topicIDs, stats)
	stats.TopicsMerged = mergeCount

	onProgress(ProgressEvent{
		Stage:   "consolidation",
		Current: 1,
		Total:   1,
		Message: fmt.Sprintf("Merged %d topics", mergeCount),
	})

	// 4. Process facts for new topics
	// Re-fetch topics that weren't merged and need fact extraction
	pendingTopics, err := s.topicRepo.GetTopicsPendingFacts(userID)
	if err != nil {
		s.logger.Error("failed to get pending topics", "error", err)
	}

	// Filter to only include topics we just created (that are in topicIDs)
	topicIDSet := make(map[int64]bool)
	for _, id := range topicIDs {
		topicIDSet[id] = true
	}

	var toProcess []storage.Topic
	for _, t := range pendingTopics {
		if topicIDSet[t.ID] && t.ConsolidationChecked {
			toProcess = append(toProcess, t)
		}
	}

	if len(toProcess) > 0 {
		onProgress(ProgressEvent{
			Stage:   "facts",
			Current: 0,
			Total:   len(toProcess),
			Message: fmt.Sprintf("Extracting facts from %d topics...", len(toProcess)),
		})

		for i, topic := range toProcess {
			onProgress(ProgressEvent{
				Stage:   "facts",
				Current: i,
				Total:   len(toProcess),
				Message: fmt.Sprintf("Processing topic %d/%d...", i+1, len(toProcess)),
			})

			msgs, err := s.msgRepo.GetMessagesInRange(ctx, userID, topic.StartMsgID, topic.EndMsgID)
			if err != nil {
				s.logger.Error("failed to get messages for topic", "topic_id", topic.ID, "error", err)
				continue
			}

			if len(msgs) == 0 {
				_ = s.topicRepo.SetTopicFactsExtracted(topic.ID, true)
				continue
			}

			factStats, err := s.memoryService.ProcessSessionWithStats(ctx, userID, msgs, topic.CreatedAt, topic.ID)
			if err != nil {
				s.logger.Error("failed to process facts", "topic_id", topic.ID, "error", err)
				continue
			}

			stats.FactsCreated += factStats.Created
			stats.FactsUpdated += factStats.Updated
			stats.FactsDeleted += factStats.Deleted
			// Aggregate usage from fact extraction (cost is aggregated, tokens counted separately)
			stats.PromptTokens += factStats.PromptTokens
			stats.CompletionTokens += factStats.CompletionTokens
			stats.EmbeddingTokens += factStats.EmbeddingTokens
			if factStats.Cost != nil {
				if stats.TotalCost == nil {
					stats.TotalCost = new(float64)
				}
				*stats.TotalCost += *factStats.Cost
			}

			_ = s.topicRepo.SetTopicFactsExtracted(topic.ID, true)
		}

		onProgress(ProgressEvent{
			Stage:   "facts",
			Current: len(toProcess),
			Total:   len(toProcess),
			Message: fmt.Sprintf("Processed facts: %d created, %d updated, %d deleted",
				stats.FactsCreated, stats.FactsUpdated, stats.FactsDeleted),
		})
	}

	// Final progress
	onProgress(ProgressEvent{
		Stage:    "complete",
		Complete: true,
		Message:  "Processing complete",
		Stats:    stats,
	})

	return stats, nil
}

// processChunkWithStats processes a chunk and returns the created topic IDs.
func (s *Service) processChunkWithStats(ctx context.Context, userID int64, chunk []storage.Message, stats *ProcessingStats) ([]int64, error) {
	if len(chunk) == 0 {
		return nil, nil
	}
	s.logger.Info("Processing chunk with stats", "user_id", userID, "count", len(chunk))

	// 1. Extract Topics
	topics, topicUsage, err := s.extractTopics(ctx, userID, chunk)
	if err != nil {
		return nil, fmt.Errorf("extract topics: %w", err)
	}
	stats.AddChatUsage(topicUsage.PromptTokens, topicUsage.CompletionTokens, topicUsage.Cost)

	// 2. Validate Coverage
	chunkStartID, chunkEndID := findChunkBounds(chunk)
	referenceDate := chunk[len(chunk)-1].CreatedAt

	var validTopics []ExtractedTopic
	for _, t := range topics {
		if t.StartMsgID < chunkStartID {
			t.StartMsgID = chunkStartID
		}
		if t.EndMsgID > chunkEndID {
			t.EndMsgID = chunkEndID
		}
		if t.StartMsgID > t.EndMsgID {
			continue
		}

		var foundMessages []storage.Message
		for _, msg := range chunk {
			if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
				foundMessages = append(foundMessages, msg)
			}
		}

		if len(foundMessages) == 0 {
			continue
		}

		t.StartMsgID, t.EndMsgID = findChunkBounds(foundMessages)
		validTopics = append(validTopics, t)
	}

	stragglerIDs := findStragglers(chunk, validTopics)
	if len(stragglerIDs) > 0 {
		return nil, fmt.Errorf("topic extraction incomplete: %d messages not covered", len(stragglerIDs))
	}

	// 3. Vectorize and Save Topics
	var topicIDs []int64
	var embeddingInputs []string

	for _, t := range validTopics {
		var contentBuilder strings.Builder
		for _, msg := range chunk {
			if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
				contentBuilder.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
			}
		}
		embeddingInput := fmt.Sprintf("Topic Summary: %s\n\nConversation Log:\n%s", t.Summary, contentBuilder.String())
		embeddingInputs = append(embeddingInputs, embeddingInput)
	}

	if len(embeddingInputs) > 0 {
		embeddingStart := time.Now()
		resp, err := s.client.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
			Model: s.cfg.RAG.EmbeddingModel,
			Input: embeddingInputs,
		})
		embeddingDuration := time.Since(embeddingStart).Seconds()
		if err != nil {
			RecordEmbeddingRequest(userID, s.cfg.RAG.EmbeddingModel, searchTypeTopics, embeddingDuration, false, 0, nil)
			return nil, fmt.Errorf("create embeddings: %w", err)
		}
		RecordEmbeddingRequest(userID, s.cfg.RAG.EmbeddingModel, searchTypeTopics, embeddingDuration, true, resp.Usage.TotalTokens, resp.Usage.Cost)
		stats.AddEmbeddingUsage(resp.Usage.TotalTokens, resp.Usage.Cost)

		if len(resp.Data) != len(validTopics) {
			return nil, fmt.Errorf("embedding count mismatch")
		}

		sort.Slice(resp.Data, func(i, j int) bool {
			return resp.Data[i].Index < resp.Data[j].Index
		})

		for i, t := range validTopics {
			topic := storage.Topic{
				UserID:         userID,
				Summary:        t.Summary,
				StartMsgID:     t.StartMsgID,
				EndMsgID:       t.EndMsgID,
				Embedding:      resp.Data[i].Embedding,
				CreatedAt:      referenceDate,
				FactsExtracted: false,
			}

			id, err := s.topicRepo.AddTopic(topic)
			if err != nil {
				s.logger.Error("failed to save topic", "error", err)
				continue
			}
			topicIDs = append(topicIDs, id)
		}
	}

	// Load new vectors synchronously
	if err := s.LoadNewVectors(); err != nil {
		s.logger.Error("failed to load new vectors", "error", err)
	}

	return topicIDs, nil
}

// runConsolidationSync runs consolidation for specific topics and returns merge count.
func (s *Service) runConsolidationSync(ctx context.Context, userID int64, topicIDs []int64, stats *ProcessingStats) int {
	mergeCount := 0

	for {
		candidates, err := s.findMergeCandidates(userID)
		if err != nil {
			s.logger.Error("failed to find merge candidates", "error", err)
			break
		}

		// Filter to candidates involving our topics
		var relevantCandidates []storage.MergeCandidate
		topicIDSet := make(map[int64]bool)
		for _, id := range topicIDs {
			topicIDSet[id] = true
		}

		for _, c := range candidates {
			if topicIDSet[c.Topic1.ID] || topicIDSet[c.Topic2.ID] {
				relevantCandidates = append(relevantCandidates, c)
			}
		}

		if len(relevantCandidates) == 0 {
			break
		}

		merged := false
		for _, candidate := range relevantCandidates {
			if ctx.Err() != nil {
				return mergeCount
			}

			shouldMerge, newSummary, verifyUsage, err := s.verifyMerge(ctx, candidate)
			stats.AddChatUsage(verifyUsage.PromptTokens, verifyUsage.CompletionTokens, verifyUsage.Cost)
			if err != nil {
				s.logger.Error("failed to verify merge", "error", err)
				continue
			}

			if shouldMerge {
				mergeUsage, err := s.mergeTopics(ctx, candidate, newSummary)
				stats.AddEmbeddingUsage(mergeUsage.TotalTokens, mergeUsage.Cost)
				if err != nil {
					s.logger.Error("failed to merge topics", "error", err)
				} else {
					mergeCount++
					merged = true
					s.logger.Info("Merged topics (sync)", "t1", candidate.Topic1.ID, "t2", candidate.Topic2.ID)
					// Reload vectors after merge
					if err := s.ReloadVectors(); err != nil {
						s.logger.Error("failed to reload vectors", "error", err)
					}
					break // Refresh candidates
				}
			} else {
				if err := s.topicRepo.SetTopicConsolidationChecked(candidate.Topic1.ID, true); err != nil {
					s.logger.Error("failed to mark topic checked", "id", candidate.Topic1.ID, "error", err)
				}
			}
		}

		if !merged {
			break
		}
	}

	// Mark remaining topics as consolidation checked
	for _, id := range topicIDs {
		_ = s.topicRepo.SetTopicConsolidationChecked(id, true)
	}

	return mergeCount
}

// ActiveSessionInfo contains information about unprocessed messages for a user.
type ActiveSessionInfo struct {
	UserID           int64
	MessageCount     int
	FirstMessageTime time.Time
	LastMessageTime  time.Time
	ContextSize      int // Total characters in session messages
}

// GetActiveSessions returns information about unprocessed messages (active sessions) for all users.
func (s *Service) GetActiveSessions() ([]ActiveSessionInfo, error) {
	users := s.cfg.Bot.AllowedUserIDs
	var sessions []ActiveSessionInfo

	for _, userID := range users {
		messages, err := s.msgRepo.GetUnprocessedMessages(userID)
		if err != nil {
			s.logger.Error("failed to fetch unprocessed messages for user", "user_id", userID, "error", err)
			continue
		}

		if len(messages) == 0 {
			continue
		}

		// Calculate total context size
		contextSize := 0
		for _, msg := range messages {
			contextSize += len(msg.Content)
		}

		sessions = append(sessions, ActiveSessionInfo{
			UserID:           userID,
			MessageCount:     len(messages),
			FirstMessageTime: messages[0].CreatedAt,
			LastMessageTime:  messages[len(messages)-1].CreatedAt,
			ContextSize:      contextSize,
		})
	}

	return sessions, nil
}

type ExtractedTopic struct {
	Summary    string `json:"summary"`
	StartMsgID int64  `json:"start_msg_id"`
	EndMsgID   int64  `json:"end_msg_id"`
}

// findChunkBounds returns the min and max message IDs in a chunk.
func findChunkBounds(chunk []storage.Message) (minID, maxID int64) {
	if len(chunk) == 0 {
		return 0, 0
	}
	minID, maxID = chunk[0].ID, chunk[0].ID
	for _, m := range chunk[1:] {
		if m.ID < minID {
			minID = m.ID
		}
		if m.ID > maxID {
			maxID = m.ID
		}
	}
	return minID, maxID
}

// findStragglers returns message IDs from chunk that are not covered by any topic.
func findStragglers(chunk []storage.Message, topics []ExtractedTopic) []int64 {
	coveredIDs := make(map[int64]bool)
	for _, t := range topics {
		for _, msg := range chunk {
			if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
				coveredIDs[msg.ID] = true
			}
		}
	}

	var stragglers []int64
	for _, msg := range chunk {
		if !coveredIDs[msg.ID] {
			stragglers = append(stragglers, msg.ID)
		}
	}
	return stragglers
}

func (s *Service) processChunk(ctx context.Context, userID int64, chunk []storage.Message) error {
	if len(chunk) == 0 {
		return nil
	}
	s.logger.Info("Processing chunk", "user_id", userID, "count", len(chunk), "start", chunk[0].ID, "end", chunk[len(chunk)-1].ID)

	// 1. Extract Topics (Wait for completion)
	topics, _, err := s.extractTopics(ctx, userID, chunk)
	if err != nil {
		return fmt.Errorf("extract topics: %w", err)
	}

	// 2. Validate Coverage
	chunkStartID, chunkEndID := findChunkBounds(chunk)
	referenceDate := chunk[len(chunk)-1].CreatedAt

	var validTopics []ExtractedTopic

	for _, t := range topics {
		// Clamp IDs to chunk range
		if t.StartMsgID < chunkStartID {
			t.StartMsgID = chunkStartID
		}
		if t.EndMsgID > chunkEndID {
			t.EndMsgID = chunkEndID
		}

		if t.StartMsgID > t.EndMsgID {
			continue
		}

		// Collect actual message content for this topic
		var foundMessages []storage.Message
		for _, msg := range chunk {
			if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
				foundMessages = append(foundMessages, msg)
			}
		}

		if len(foundMessages) == 0 {
			s.logger.Warn("skipping topic with no messages", "summary", t.Summary, "start", t.StartMsgID, "end", t.EndMsgID)
			continue
		}

		// Update IDs to match actual messages found to ensure AddTopic updates them
		t.StartMsgID, t.EndMsgID = findChunkBounds(foundMessages)

		validTopics = append(validTopics, t)
	}

	// Check for stragglers (messages not covered by any topic)
	stragglerIDs := findStragglers(chunk, validTopics)
	if len(stragglerIDs) > 0 {
		s.logger.Warn("Topic extraction incomplete: stragglers detected", "count", len(stragglerIDs), "ids", stragglerIDs)
		// Log received topics for debug
		for i, t := range validTopics {
			s.logger.Debug("Received topic", "index", i, "start", t.StartMsgID, "end", t.EndMsgID, "summary", t.Summary)
		}
		// Log straggler content for debug
		for _, msgID := range stragglerIDs {
			for _, m := range chunk {
				if m.ID == msgID {
					s.logger.Debug("Straggler content", "id", m.ID, "content", m.Content)
					break
				}
			}
		}
		return fmt.Errorf("topic extraction incomplete: %d messages not covered", len(stragglerIDs))
	}

	// 3. Vectorize and Save Topics
	var embeddingInputs []string
	for _, t := range validTopics {
		var contentBuilder strings.Builder
		for _, msg := range chunk {
			if msg.ID >= t.StartMsgID && msg.ID <= t.EndMsgID {
				contentBuilder.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
			}
		}
		embeddingInput := fmt.Sprintf("Topic Summary: %s\n\nConversation Log:\n%s", t.Summary, contentBuilder.String())
		embeddingInputs = append(embeddingInputs, embeddingInput)
	}

	if len(embeddingInputs) > 0 {
		embeddingStart := time.Now()
		resp, err := s.client.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
			Model: s.cfg.RAG.EmbeddingModel,
			Input: embeddingInputs,
		})
		embeddingDuration := time.Since(embeddingStart).Seconds()
		if err != nil {
			RecordEmbeddingRequest(userID, s.cfg.RAG.EmbeddingModel, searchTypeTopics, embeddingDuration, false, 0, nil)
			s.logger.Error("failed to create embeddings for topics", "error", err)
			return fmt.Errorf("create embeddings: %w", err)
		}
		RecordEmbeddingRequest(userID, s.cfg.RAG.EmbeddingModel, searchTypeTopics, embeddingDuration, true, resp.Usage.TotalTokens, resp.Usage.Cost)

		if len(resp.Data) != len(validTopics) {
			s.logger.Error("embedding count mismatch", "expected", len(validTopics), "got", len(resp.Data))
			return fmt.Errorf("embedding count mismatch")
		}

		// Ensure response data is sorted by index to match input order
		sort.Slice(resp.Data, func(i, j int) bool {
			return resp.Data[i].Index < resp.Data[j].Index
		})

		for i, t := range validTopics {
			topic := storage.Topic{
				UserID:         userID,
				Summary:        t.Summary,
				StartMsgID:     t.StartMsgID,
				EndMsgID:       t.EndMsgID,
				Embedding:      resp.Data[i].Embedding,
				CreatedAt:      referenceDate,
				FactsExtracted: false, // Explicitly false, will be processed by factExtractionLoop
			}

			// AddTopic also updates messages in range with topic_id
			if _, err := s.topicRepo.AddTopic(topic); err != nil {
				s.logger.Error("failed to save topic", "error", err)
				continue
			}
		}
	}

	// Load new vectors (incremental)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.LoadNewVectors(); err != nil {
			s.logger.Error("failed to load new vectors", "error", err)
		}
	}()

	// Trigger consolidation
	s.TriggerConsolidation()

	return nil
}

func (s *Service) factExtractionLoop(ctx context.Context) {
	defer s.wg.Done()
	interval := 1 * time.Minute // Check every minute

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial run
	s.processFactExtraction(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.processFactExtraction(ctx)
		}
	}
}

func (s *Service) processFactExtraction(ctx context.Context) {
	users := s.cfg.Bot.AllowedUserIDs
	for _, userID := range users {
		if ctx.Err() != nil {
			return
		}

		topics, err := s.topicRepo.GetTopicsPendingFacts(userID)
		if err != nil {
			s.logger.Error("failed to get pending topics", "error", err)
			continue
		}

		for _, topic := range topics {
			if ctx.Err() != nil {
				return
			}

			// Enforce chronological order: stop if we hit a topic that hasn't been checked for consolidation yet.
			if !topic.ConsolidationChecked {
				// We stop processing for this user to ensure we don't skip ahead.
				// The consolidation loop will eventually mark this topic as checked (or merge it).
				break
			}

			// Get messages for this topic
			msgs, err := s.msgRepo.GetMessagesInRange(ctx, userID, topic.StartMsgID, topic.EndMsgID)
			if err != nil {
				s.logger.Error("failed to get messages for topic", "topic_id", topic.ID, "error", err)
				continue
			}

			if len(msgs) == 0 {
				// Empty topic? Mark processed.
				_ = s.topicRepo.SetTopicFactsExtracted(topic.ID, true)
				continue
			}

			// Process facts
			// Use a detached context to ensure completion even if shutdown is triggered
			runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			err = s.memoryService.ProcessSession(runCtx, userID, msgs, topic.CreatedAt, topic.ID)
			cancel()

			if err != nil {
				s.logger.Error("failed to process facts for topic", "topic_id", topic.ID, "error", err)
				// Don't mark processed so we retry? Or mark processed to avoid stuck?
				// Let's retry later.
				continue
			}

			// Mark processed
			if err := s.topicRepo.SetTopicFactsExtracted(topic.ID, true); err != nil {
				s.logger.Error("failed to mark topic processed", "topic_id", topic.ID, "error", err)
			}
		}
	}
}

func (s *Service) RetrieveFacts(ctx context.Context, userID int64, query string) ([]storage.Fact, error) {
	if !s.cfg.RAG.Enabled {
		return nil, nil
	}

	// Embedding for query
	embeddingStart := time.Now()
	resp, err := s.client.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
		Model: s.cfg.RAG.EmbeddingModel,
		Input: []string{query},
	})
	embeddingDuration := time.Since(embeddingStart).Seconds()
	if err != nil {
		RecordEmbeddingRequest(userID, s.cfg.RAG.EmbeddingModel, searchTypeFacts, embeddingDuration, false, 0, nil)
		return nil, err
	}
	if len(resp.Data) == 0 {
		RecordEmbeddingRequest(userID, s.cfg.RAG.EmbeddingModel, searchTypeFacts, embeddingDuration, false, 0, nil)
		return nil, fmt.Errorf("no embedding returned")
	}
	RecordEmbeddingRequest(userID, s.cfg.RAG.EmbeddingModel, searchTypeFacts, embeddingDuration, true, resp.Usage.TotalTokens, resp.Usage.Cost)
	qVec := resp.Data[0].Embedding

	searchStart := time.Now()
	s.mu.RLock()
	type match struct {
		factID int64
		score  float32
	}
	var matches []match
	userVectors := s.factVectors[userID]
	vectorsScanned := len(userVectors)
	minSafetyThreshold := s.cfg.RAG.MinSafetyThreshold
	if minSafetyThreshold == 0 {
		minSafetyThreshold = 0.1
	}

	for _, item := range userVectors {
		score := cosineSimilarity(qVec, item.Embedding)
		if float64(score) > minSafetyThreshold {
			matches = append(matches, match{factID: item.FactID, score: score})
		}
	}
	s.mu.RUnlock()
	RecordVectorSearch(userID, searchTypeFacts, time.Since(searchStart).Seconds(), vectorsScanned)
	RecordRAGCandidates(userID, searchTypeFacts, len(matches))

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})

	if len(matches) > 10 { // Limit to 10 facts
		matches = matches[:10]
	}

	// Fetch facts
	allFacts, err := s.factRepo.GetFacts(userID)
	if err != nil {
		return nil, err
	}

	factMap := make(map[int64]storage.Fact)
	for _, f := range allFacts {
		factMap[f.ID] = f
	}

	var results []storage.Fact
	for _, m := range matches {
		if f, ok := factMap[m.factID]; ok {
			results = append(results, f)
		}
	}

	return results, nil
}

func (s *Service) FindSimilarFacts(ctx context.Context, userID int64, embedding []float32, threshold float32) ([]storage.Fact, error) {
	searchStart := time.Now()
	s.mu.RLock()

	userVectors := s.factVectors[userID]
	vectorsScanned := len(userVectors)
	type match struct {
		factID int64
		score  float32
	}
	var matches []match

	for _, item := range userVectors {
		score := cosineSimilarity(embedding, item.Embedding)
		if score > threshold {
			matches = append(matches, match{factID: item.FactID, score: score})
		}
	}
	s.mu.RUnlock()
	RecordVectorSearch(userID, searchTypeFacts, time.Since(searchStart).Seconds(), vectorsScanned)
	RecordRAGCandidates(userID, searchTypeFacts, len(matches))

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})

	if len(matches) > 5 {
		matches = matches[:5]
	}

	if len(matches) == 0 {
		return nil, nil
	}

	var ids []int64
	for _, m := range matches {
		ids = append(ids, m.factID)
	}

	return s.factRepo.GetFactsByIDs(ids)
}

func (s *Service) extractTopics(ctx context.Context, userID int64, chunk []storage.Message) ([]ExtractedTopic, UsageInfo, error) {
	// Prepare JSON
	type MsgItem struct {
		ID      int64  `json:"id"`
		Role    string `json:"role"`
		Content string `json:"content"`
		Date    string `json:"date"`
	}
	var items []MsgItem
	for _, m := range chunk {
		items = append(items, MsgItem{
			ID:      m.ID,
			Role:    m.Role,
			Content: m.Content,
			Date:    m.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	itemsBytes, _ := json.Marshal(items)

	prompt := s.cfg.RAG.TopicExtractionPrompt
	if prompt == "" {
		prompt = s.translator.Get(s.cfg.Bot.Language, "rag.topic_extraction_prompt")
	}

	fullPrompt := fmt.Sprintf("%s\n\nChat Log JSON:\n%s", prompt, string(itemsBytes))

	model := s.cfg.RAG.TopicModel
	if model == "" {
		model = "google/gemini-3-flash-preview"
	}

	// Define JSON Schema for Structured Outputs
	schema := map[string]interface{}{
		"type": "json_schema",
		"json_schema": map[string]interface{}{
			"name":   "topic_extraction",
			"strict": true,
			"schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"topics": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"summary": map[string]interface{}{
									"type":        "string",
									"description": "Подробное описание темы обсуждения на русском языке. Сохраняй названия брендов и ПО на английском.",
								},
								"start_msg_id": map[string]interface{}{
									"type":        "integer",
									"description": "ID первого сообщения в теме.",
								},
								"end_msg_id": map[string]interface{}{
									"type":        "integer",
									"description": "ID последнего сообщения в теме.",
								},
							},
							"required":             []string{"summary", "start_msg_id", "end_msg_id"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"topics"},
				"additionalProperties": false,
			},
		},
	}

	resp, err := s.client.CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
		Model: model,
		Messages: []openrouter.Message{
			{Role: "user", Content: fullPrompt},
		},
		ResponseFormat: schema,
		UserID:         userID,
	})
	if err != nil {
		return nil, UsageInfo{}, err
	}

	usage := UsageInfo{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		Cost:             resp.Usage.Cost,
	}

	if len(resp.Choices) == 0 {
		return nil, usage, fmt.Errorf("empty response")
	}

	content := resp.Choices[0].Message.Content

	// Log to DB for debugging
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		userID := int64(0)
		if len(chunk) > 0 {
			userID = chunk[0].UserID
		}
		log := storage.RAGLog{
			UserID:           userID,
			OriginalQuery:    "Topic Extraction",
			ContextUsed:      string(itemsBytes),
			SystemPrompt:     fullPrompt,
			LLMResponse:      content,
			GenerationTokens: resp.Usage.PromptTokens + resp.Usage.CompletionTokens,
		}
		if err := s.logRepo.AddRAGLog(log); err != nil {
			s.logger.Error("failed to log topic extraction", "error", err)
		}
	}()

	var result struct {
		Topics []ExtractedTopic `json:"topics"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, usage, fmt.Errorf("json parse error: %w. Content: %s", err, content)
	}

	return result.Topics, usage, nil
}

func (s *Service) enrichQuery(ctx context.Context, userID int64, query string, history []storage.Message) (string, string, int, error) {
	model := s.cfg.RAG.QueryModel
	if model == "" {
		return query, "", 0, nil
	}

	var historyStr strings.Builder
	for _, msg := range history {
		content := msg.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		content = strings.ReplaceAll(content, "\n", " ")
		historyStr.WriteString(fmt.Sprintf("- [%s]: %s\n", msg.Role, content))
	}

	promptTmpl := s.cfg.RAG.EnrichmentPrompt
	if promptTmpl == "" {
		promptTmpl = s.translator.Get(s.cfg.Bot.Language, "rag.enrichment_prompt")
	}

	prompt := fmt.Sprintf(promptTmpl, historyStr.String(), query)

	resp, err := s.client.CreateChatCompletion(ctx, openrouter.ChatCompletionRequest{
		Model: model,
		Messages: []openrouter.Message{
			{Role: "user", Content: prompt},
		},
		UserID: userID,
	})
	if err != nil {
		return "", prompt, 0, err
	}
	if len(resp.Choices) == 0 {
		return "", prompt, 0, fmt.Errorf("empty response")
	}

	tokens := resp.Usage.PromptTokens + resp.Usage.CompletionTokens
	return strings.TrimSpace(resp.Choices[0].Message.Content), prompt, tokens, nil
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, magA, magB float32
	for i := 0; i < len(a); i++ {
		dot += a[i] * b[i]
		magA += a[i] * a[i]
		magB += b[i] * b[i]
	}
	if magA == 0 || magB == 0 {
		return 0
	}
	return dot / (float32(math.Sqrt(float64(magA))) * float32(math.Sqrt(float64(magB))))
}

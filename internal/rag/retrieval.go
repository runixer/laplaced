package rag

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

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
	Reason   string  // Why reranker chose this topic (empty if no reason provided)
	Excerpt  *string // If set, use this instead of Messages (for large topics)
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
	Source         string        // "auto" for buildContext, "tool" for search_history
	MediaParts     []interface{} // Multimodal content (images, audio) for enricher and reranker
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
		enrichedQuery, prompt, tokens, err = s.enrichQuery(ctx, userID, query, opts.History, opts.MediaParts)
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
		Model: s.cfg.Embedding.Model,
		Input: []string{enrichedQuery},
	})
	embeddingDuration := time.Since(embeddingStart).Seconds()
	if err != nil {
		RecordEmbeddingRequest(userID, s.cfg.Embedding.Model, searchTypeTopics, embeddingDuration, false, 0, nil)
		return nil, debugInfo, err
	}
	if len(resp.Data) == 0 {
		RecordEmbeddingRequest(userID, s.cfg.Embedding.Model, searchTypeTopics, embeddingDuration, false, 0, nil)
		return nil, debugInfo, fmt.Errorf("no embedding returned")
	}
	RecordEmbeddingRequest(userID, s.cfg.Embedding.Model, searchTypeTopics, embeddingDuration, true, resp.Usage.TotalTokens, resp.Usage.Cost)
	qVec := resp.Data[0].Embedding

	// Search topics
	searchStart := time.Now()
	s.mu.RLock()
	type match struct {
		topicID int64
		score   float32
		reason  string  // Reranker reason (filled after reranking)
		excerpt *string // Reranker excerpt (filled after reranking)
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

	// Determine how many candidates to consider
	rerankerCfg := s.cfg.Agents.Reranker
	useReranker := rerankerCfg.Enabled && len(matches) > 0

	var maxCandidates int
	if useReranker {
		// For reranker, take more candidates for intelligent filtering
		maxCandidates = rerankerCfg.Candidates
		if maxCandidates <= 0 {
			maxCandidates = 50
		}
	} else {
		// Legacy behavior: limit by retrieved_topics_count
		maxCandidates = s.cfg.RAG.RetrievedTopicsCount
		if maxCandidates <= 0 {
			maxCandidates = 10
		}
	}

	if len(matches) > maxCandidates {
		matches = matches[:maxCandidates]
	}

	// Collect topic IDs from matches
	topicIDs := make([]int64, len(matches))
	for i, m := range matches {
		topicIDs[i] = m.topicID
	}

	// Fetch topics (needed for summaries in reranker and for final results)
	topics, err := s.topicRepo.GetTopicsByIDs(topicIDs)
	if err != nil {
		s.logger.Error("failed to load topics for retrieval", "error", err)
		return nil, debugInfo, err
	}
	topicMap := make(map[int64]storage.Topic)
	for _, t := range topics {
		topicMap[t.ID] = t
	}

	// Apply reranker if enabled
	if useReranker {
		RecordRerankerCandidatesInput(userID, len(matches))

		// Build reranker candidates with topic metadata
		candidates := make([]rerankerCandidate, 0, len(matches))
		for _, m := range matches {
			if topic, ok := topicMap[m.topicID]; ok {
				msgCount := int(topic.EndMsgID - topic.StartMsgID + 1)
				if msgCount < 0 {
					msgCount = 0
				}
				candidates = append(candidates, rerankerCandidate{
					TopicID:      m.topicID,
					Score:        m.score,
					Topic:        topic,
					MessageCount: msgCount,
					SizeChars:    topic.SizeChars, // Use real size from DB
				})
			}
		}

		// Call reranker
		// contextualizedQuery = enrichedQuery or original query
		contextualizedQuery := debugInfo.EnrichedQuery
		if contextualizedQuery == "" {
			contextualizedQuery = query
		}

		// Load user profile and recent topics for reranker context
		// Try to use SharedContext if available (loaded once in processMessageGroup)
		var userProfile string
		var recentTopics string

		if shared := agent.FromContext(ctx); shared != nil {
			// Use pre-loaded data from SharedContext
			userProfile = shared.Profile
			recentTopics = shared.RecentTopics
		} else {
			// Fallback: load directly (for tests or when contextService is not configured)
			allFacts, err := s.factRepo.GetFacts(userID)
			if err == nil {
				userProfile = FormatUserProfile(FilterProfileFacts(allFacts))
			} else {
				s.logger.Warn("failed to load facts for reranker", "error", err)
				userProfile = FormatUserProfile(nil)
			}

			// Load recent topics for reranker context
			recentTopicsCount := s.cfg.RAG.GetRecentTopicsInContext()
			if recentTopicsCount > 0 {
				topics, err := s.GetRecentTopics(userID, recentTopicsCount)
				if err != nil {
					s.logger.Warn("failed to get recent topics for reranker", "error", err)
				}
				recentTopics = FormatRecentTopics(topics)
			} else {
				recentTopics = FormatRecentTopics(nil)
			}
		}

		// Format current session messages for reranker
		currentMessages := s.formatSessionMessages(opts.History)
		result, err := s.rerankCandidates(ctx, userID, candidates, contextualizedQuery, query, currentMessages, userProfile, recentTopics, opts.MediaParts)
		if err != nil {
			s.logger.Warn("reranker failed, falling back to vector search", "error", err)
			RecordRerankerFallback(userID, "error")
		} else {
			// Build maps for selected topics, reasons, and excerpts
			selectedIDs := make(map[int64]bool)
			topicReasons := make(map[int64]string)
			topicExcerpts := make(map[int64]string)
			for _, t := range result.Topics {
				selectedIDs[t.ID] = true
				if t.Reason != "" {
					topicReasons[t.ID] = t.Reason
				}
				if t.Excerpt != nil {
					topicExcerpts[t.ID] = *t.Excerpt
				}
			}

			var filteredMatches []match
			for _, m := range matches {
				if selectedIDs[m.topicID] {
					m.reason = topicReasons[m.topicID]
					if excerpt, ok := topicExcerpts[m.topicID]; ok {
						m.excerpt = &excerpt
					}
					filteredMatches = append(filteredMatches, m)
				}
			}
			matches = filteredMatches
		}
	} else {
		// Legacy behavior: limit by maxTopics (already applied above)
		maxTopics := s.cfg.RAG.RetrievedTopicsCount
		if maxTopics <= 0 {
			maxTopics = 10
		}
		if len(matches) > maxTopics {
			matches = matches[:maxTopics]
		}
	}

	var results []TopicSearchResult
	seenMsgIDs := make(map[int64]bool)

	for _, m := range matches {
		topic, ok := topicMap[m.topicID]
		if !ok {
			continue
		}

		msgs, err := s.msgRepo.GetMessagesByTopicID(ctx, topic.ID)
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

		if len(topicMsgs) > 0 || m.excerpt != nil {
			results = append(results, TopicSearchResult{
				Topic:    topic,
				Score:    m.score,
				Messages: topicMsgs,
				Reason:   m.reason,
				Excerpt:  m.excerpt,
			})
		}
	}

	debugInfo.Results = results

	// Record RAG metrics
	RecordRAGRetrieval(userID, len(results) > 0)
	RecordRAGLatency(userID, opts.Source, time.Since(startTime).Seconds())

	return results, debugInfo, nil
}

func (s *Service) RetrieveFacts(ctx context.Context, userID int64, query string) ([]storage.Fact, error) {
	if !s.cfg.RAG.Enabled {
		return nil, nil
	}

	// Embedding for query
	embeddingStart := time.Now()
	resp, err := s.client.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
		Model: s.cfg.Embedding.Model,
		Input: []string{query},
	})
	embeddingDuration := time.Since(embeddingStart).Seconds()
	if err != nil {
		RecordEmbeddingRequest(userID, s.cfg.Embedding.Model, searchTypeFacts, embeddingDuration, false, 0, nil)
		return nil, err
	}
	if len(resp.Data) == 0 {
		RecordEmbeddingRequest(userID, s.cfg.Embedding.Model, searchTypeFacts, embeddingDuration, false, 0, nil)
		return nil, fmt.Errorf("no embedding returned")
	}
	RecordEmbeddingRequest(userID, s.cfg.Embedding.Model, searchTypeFacts, embeddingDuration, true, resp.Usage.TotalTokens, resp.Usage.Cost)
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

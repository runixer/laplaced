package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/obs"
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
	Reason   string // Why reranker chose this topic (empty if no reason provided)
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

func (s *Service) Retrieve(ctx context.Context, userID int64, query string, opts *RetrievalOptions) (*RetrievalResult, *RetrievalDebugInfo, error) {
	startTime := time.Now()
	debugInfo := &RetrievalDebugInfo{
		OriginalQuery: query,
	}

	if !s.cfg.RAG.Enabled {
		return &RetrievalResult{}, debugInfo, nil
	}

	if opts == nil {
		opts = &RetrievalOptions{}
	}

	// Root-for-RAG span. Structural attrs (counts, reranker, selected IDs)
	// are filled in via closure-captured locals so the deferred End() sees
	// final values regardless of which error path we take. Content events
	// (raw_query, enriched_query) are noops when trace_content is off.
	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/rag").Start(
		ctx, "rag.Retrieve",
		trace.WithAttributes(
			attribute.Int64("user.id", userID),
			attribute.String("rag.source", opts.Source),
		),
	)
	var (
		candidatesIn        int
		candidatesOut       int
		usedReranker        bool
		selectedTopicIDs    []int64
		selectedArtifactIDs []int64
		spanErr             error
	)
	defer func() {
		span.SetAttributes(
			attribute.Int("rag.candidates_in", candidatesIn),
			attribute.Int("rag.candidates_out", candidatesOut),
			attribute.Bool("rag.used_reranker", usedReranker),
		)
		if len(selectedTopicIDs) > 0 {
			span.SetAttributes(attribute.Int64Slice("rag.selected_topic_ids", selectedTopicIDs))
		}
		if len(selectedArtifactIDs) > 0 {
			// Symmetric to selected_topic_ids — same "why was this retrieved"
			// signal for artifacts. Lets a TraceQL query line up the live
			// FilePart hashes from bot.current_media against the historical
			// artifact IDs the reranker chose for the same prompt window.
			span.SetAttributes(attribute.Int64Slice("rag.selected_artifact_ids", selectedArtifactIDs))
		}
		obs.RecordContent(span, "rag.raw_query", query)
		if debugInfo.EnrichedQuery != "" && debugInfo.EnrichedQuery != query {
			obs.RecordContent(span, "rag.enriched_query", debugInfo.EnrichedQuery)
		}
		_ = obs.ObserveErr(span, spanErr)
		span.End()
	}()

	// 1. Enrich query if enabled
	enrichedQuery := s.enrichQueryIfEnabled(ctx, userID, query, opts, debugInfo)

	// 2. Create embedding
	embedding, err := s.createEmbedding(ctx, userID, enrichedQuery)
	if err != nil {
		spanErr = err
		return nil, debugInfo, err
	}

	// 3. Search topics using extracted searchTopicCandidates
	topicCandidates, _ := s.searchTopicCandidates(userID, embedding, 0)

	// Convert to topicCandidate type for pipeline functions
	var matches []topicCandidate
	for _, tc := range topicCandidates {
		matches = append(matches, topicCandidate{topicID: tc.topicID, score: tc.score})
	}

	// 4. Search people and artifacts
	threshold := float32(s.cfg.RAG.GetMinSafetyThreshold())
	personCandidates, artifactCandidates := s.searchPeopleAndArtifacts(ctx, userID, embedding, threshold)

	// 5. Determine if reranker should be used
	useReranker := s.shouldUseReranker(len(matches), len(personCandidates), len(artifactCandidates))
	usedReranker = useReranker
	maxCandidates := s.getMaxCandidates(useReranker)

	// 6. Limit candidates
	matches = limitMatches(matches, maxCandidates)
	candidatesIn = len(matches)

	// 7. Load topic map
	topicMap, err := s.loadTopicMap(ctx, userID, matches)
	if err != nil {
		s.logger.Error("failed to load topics for retrieval", "error", err)
		spanErr = err
		return nil, debugInfo, err
	}

	// 8. Apply reranker or use vector search results
	var rerankerOut *rerankerOutput
	if useReranker {
		// Prepare reranker context
		userProfile, recentTopics, currentMessages, err := s.prepareRerankerContext(ctx, userID, opts.History)
		if err != nil {
			s.logger.Warn("failed to prepare reranker context", "error", err)
		}

		// Build reranker input
		rerankerInput := rerankerInput{
			topicCandidates:     matches,
			topicMap:            topicMap,
			personCandidates:    personCandidates,
			artifactCandidates:  artifactCandidates,
			contextualizedQuery: enrichedQuery,
			originalQuery:       query,
			currentMessages:     currentMessages,
			userProfile:         userProfile,
			recentTopics:        recentTopics,
			mediaParts:          opts.MediaParts,
		}

		// Execute reranker
		rerankerOut, err = s.executeReranker(ctx, userID, rerankerInput)
		if err != nil {
			s.logger.Warn("reranker failed, using vector search results", "error", err)
			RecordRerankerFallback(userID, "error")
			// Reranker fallback is graceful — flip the span attr to reflect
			// that the final result did not honor reranker selection.
			usedReranker = false
		} else {
			// Capture selected IDs BEFORE filter for trace visibility; the
			// filter drops matches but the IDs chosen by the reranker are
			// the single most useful "why was this retrieved" signal.
			selectedTopicIDs = append(selectedTopicIDs, rerankerOut.selectedTopicIDs...)
			matches = filterMatchesByReranker(matches, rerankerOut.selectedTopicIDs, rerankerOut.topicReasons)
		}
	} else {
		// Legacy behavior: limit by maxTopics
		maxTopics := s.cfg.RAG.RetrievedTopicsCount
		if maxTopics <= 0 {
			maxTopics = 10
		}
		if len(matches) > maxTopics {
			matches = matches[:maxTopics]
		}
	}

	// 9. Build final result
	result, err := s.buildRetrievalResult(ctx, userID, matches, topicMap, rerankerOut, artifactCandidates, useReranker)
	if err != nil {
		spanErr = err
		return nil, debugInfo, err
	}
	candidatesOut = len(result.Topics)
	selectedArtifactIDs = append(selectedArtifactIDs, result.SelectedArtifactIDs...)

	// 10. Record metrics
	debugInfo.Results = result.Topics
	RecordRAGRetrieval(userID, len(result.Topics) > 0)
	RecordRAGLatency(userID, opts.Source, time.Since(startTime).Seconds())

	return result, debugInfo, nil
}

// enrichQueryIfEnabled enriches the query if enrichment is not skipped.
func (s *Service) enrichQueryIfEnabled(ctx context.Context, userID int64, query string, opts *RetrievalOptions, debugInfo *RetrievalDebugInfo) string {
	if opts.SkipEnrichment {
		debugInfo.EnrichedQuery = query
		return query
	}

	enrichStart := time.Now()
	enrichedQuery, prompt, tokens, err := s.enrichQuery(ctx, userID, query, opts.History, opts.MediaParts)
	RecordRAGEnrichment(userID, time.Since(enrichStart).Seconds())
	debugInfo.EnrichmentPrompt = prompt
	debugInfo.EnrichmentTokens = tokens

	if err != nil {
		s.logger.Error("failed to enrich query", "error", err)
		debugInfo.EnrichedQuery = query
		return query
	}
	s.logger.Debug("Enriched query", "original", query, "new", enrichedQuery)
	debugInfo.EnrichedQuery = enrichedQuery
	return enrichedQuery
}

// createEmbedding creates an embedding for the given query.
func (s *Service) createEmbedding(ctx context.Context, userID int64, query string) ([]float32, error) {
	embeddingStart := time.Now()
	resp, err := s.client.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
		Model:      s.cfg.Embedding.Model,
		Dimensions: s.cfg.Embedding.Dimensions,
		Input:      []string{query},
	})
	embeddingDuration := time.Since(embeddingStart).Seconds()
	if err != nil {
		RecordEmbeddingRequest(userID, s.cfg.Embedding.Model, searchTypeTopics, embeddingDuration, false, 0, nil)
		return nil, err
	}
	if len(resp.Data) == 0 {
		RecordEmbeddingRequest(userID, s.cfg.Embedding.Model, searchTypeTopics, embeddingDuration, false, 0, nil)
		return nil, fmt.Errorf("no embedding returned")
	}
	RecordEmbeddingRequest(userID, s.cfg.Embedding.Model, searchTypeTopics, embeddingDuration, true, resp.Usage.TotalTokens, resp.Usage.Cost)
	return resp.Data[0].Embedding, nil
}

func (s *Service) RetrieveFacts(ctx context.Context, userID int64, query string) ([]storage.Fact, error) {
	if !s.cfg.RAG.Enabled {
		return nil, nil
	}

	// Embedding for query
	embeddingStart := time.Now()
	resp, err := s.client.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
		Model:      s.cfg.Embedding.Model,
		Dimensions: s.cfg.Embedding.Dimensions,
		Input:      []string{query},
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

	// Use generic vector search
	s.mu.RLock()
	userVectors := s.factVectors[userID]
	items := make([]embeddingItem, len(userVectors))
	for i := range userVectors {
		items[i] = userVectors[i]
	}
	s.mu.RUnlock()

	results := s.vectorSearch(userID, qVec, items, VectorSearchConfig{
		Limit:      10,
		MetricType: searchTypeFacts,
	})

	if len(results) == 0 {
		return nil, nil
	}

	// Fetch facts
	var ids []int64
	for _, r := range results {
		ids = append(ids, r.ID)
	}
	return s.factRepo.GetFactsByIDs(userID, ids)
}

func (s *Service) FindSimilarFacts(ctx context.Context, userID int64, embedding []float32, threshold float32) ([]storage.Fact, error) {
	s.mu.RLock()
	userVectors := s.factVectors[userID]
	items := make([]embeddingItem, len(userVectors))
	for i := range userVectors {
		items[i] = userVectors[i]
	}
	s.mu.RUnlock()

	results := s.vectorSearch(userID, embedding, items, VectorSearchConfig{
		Limit:      5,
		Threshold:  threshold,
		MetricType: searchTypeFacts,
	})

	if len(results) == 0 {
		return nil, nil
	}

	var ids []int64
	for _, r := range results {
		ids = append(ids, r.ID)
	}
	return s.factRepo.GetFactsByIDs(userID, ids)
}

// PersonSearchResult represents a matched person with their score.
type PersonSearchResult struct {
	Person storage.Person
	Score  float32
}

// ArtifactResult represents a matched artifact with its metadata (v0.6.0).
type ArtifactResult struct {
	ArtifactID   int64
	FileType     string
	OriginalName string
	Summary      string
	Keywords     []string
	Entities     []string // Named entities (people, companies, code mentioned)
	RAGHints     []string // Questions this artifact might answer
	Score        float32
}

// RetrievalResult contains both topics and selected people from RAG retrieval.
type RetrievalResult struct {
	Topics              []TopicSearchResult
	People              []storage.Person // Selected by reranker (excludes inner circle)
	Artifacts           []ArtifactResult // v0.6.0: Artifact summary matches
	SelectedArtifactIDs []int64          // v0.6.0: Artifact IDs selected by reranker for full content loading
}

// SearchPeople searches for people by vector similarity (v0.5.1).
// excludeCircles filters out people from specified circles (e.g., "Work_Inner", "Family").
func (s *Service) SearchPeople(ctx context.Context, userID int64, embedding []float32, threshold float32, maxResults int, excludeCircles []string) ([]PersonSearchResult, error) {
	if s.peopleRepo == nil {
		return nil, nil
	}

	searchStart := time.Now()
	s.mu.RLock()

	userVectors := s.peopleVectors[userID]
	vectorsScanned := len(userVectors)
	type match struct {
		personID int64
		score    float32
	}
	var matches []match

	for _, item := range userVectors {
		score := cosineSimilarity(embedding, item.Embedding)
		if score > threshold {
			matches = append(matches, match{personID: item.PersonID, score: score})
		}
	}
	s.mu.RUnlock()

	duration := time.Since(searchStart).Seconds()
	s.logger.Debug("People vector search", "user_id", userID, "scanned", vectorsScanned, "matches", len(matches), "duration_ms", time.Since(searchStart).Milliseconds())

	// Record metrics for people search (v0.5.1)
	RecordVectorSearch(userID, searchTypePeople, duration, vectorsScanned)

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})

	if maxResults > 0 && len(matches) > maxResults {
		matches = matches[:maxResults]
	}

	if len(matches) == 0 {
		return nil, nil
	}

	var ids []int64
	for _, m := range matches {
		ids = append(ids, m.personID)
	}

	people, err := s.peopleRepo.GetPeopleByIDs(userID, ids)
	if err != nil {
		return nil, err
	}

	// Build exclusion set for circles
	excludeSet := make(map[string]bool)
	for _, c := range excludeCircles {
		excludeSet[c] = true
	}

	// Build result with scores, filtering out excluded circles
	scoreMap := make(map[int64]float32)
	for _, m := range matches {
		scoreMap[m.personID] = m.score
	}

	var results []PersonSearchResult
	for _, p := range people {
		// Skip people from excluded circles (they're already in system prompt)
		if excludeSet[p.Circle] {
			continue
		}
		results = append(results, PersonSearchResult{
			Person: p,
			Score:  scoreMap[p.ID],
		})
	}

	// Sort by score again (GetPeopleByIDs might return in different order)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results, nil
}

// SearchArtifactsBySummary searches for artifacts by summary embedding similarity.
// Returns top N artifacts.
func (s *Service) SearchArtifactsBySummary(
	ctx context.Context,
	userID int64,
	embedding []float32,
	threshold float32,
) ([]ArtifactResult, error) {
	userVectors := s.artifactVectors[userID]
	if len(userVectors) == 0 {
		return nil, nil
	}

	searchStart := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	type match struct {
		artifactID int64
		score      float32
	}
	var matches []match
	vectorsScanned := len(userVectors)

	for _, item := range userVectors {
		score := cosineSimilarity(embedding, item.Embedding)
		if score > threshold {
			matches = append(matches, match{
				artifactID: item.ArtifactID,
				score:      score,
			})
		}
	}

	RecordVectorSearch(userID, searchTypeArtifacts, time.Since(searchStart).Seconds(), vectorsScanned)
	RecordRAGCandidates(userID, searchTypeArtifacts, len(matches))

	// Sort by score
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})

	// Apply configured candidates_limit (was silently unbounded — with v2
	// embeddings producing higher similarities, hundreds of artifacts were
	// sliding past the threshold and bloating the reranker context).
	if limit := s.cfg.Agents.Reranker.Artifacts.CandidatesLimit; limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}

	if len(matches) == 0 {
		return nil, nil
	}

	// Load artifact details
	artifactIDs := make([]int64, len(matches))
	for i, m := range matches {
		artifactIDs[i] = m.artifactID
	}

	artifacts, err := s.artifactRepo.GetArtifactsByIDs(userID, artifactIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to load artifacts: %w", err)
	}

	// Build score map
	scoreMap := make(map[int64]float32)
	for _, m := range matches {
		scoreMap[m.artifactID] = m.score
	}

	// Build results
	var results []ArtifactResult
	for _, artifact := range artifacts {
		var keywords, entities, ragHints []string
		if artifact.Keywords != nil {
			_ = json.Unmarshal([]byte(*artifact.Keywords), &keywords)
		}
		if artifact.Entities != nil {
			_ = json.Unmarshal([]byte(*artifact.Entities), &entities)
		}
		if artifact.RAGHints != nil {
			_ = json.Unmarshal([]byte(*artifact.RAGHints), &ragHints)
		}

		summary := ""
		if artifact.Summary != nil {
			summary = *artifact.Summary
		}

		results = append(results, ArtifactResult{
			ArtifactID:   artifact.ID,
			FileType:     artifact.FileType,
			OriginalName: artifact.OriginalName,
			Summary:      summary,
			Keywords:     keywords,
			Entities:     entities,
			RAGHints:     ragHints,
			Score:        scoreMap[artifact.ID],
		})
	}

	// Sort results by score
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results, nil
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

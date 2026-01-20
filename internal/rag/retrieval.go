package rag

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/reranker"
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
		reason  string // Reranker reason (filled after reranking)
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

	// v0.5.1: Search people vectors (excluding inner circle - they're already in system prompt)
	var personCandidates []reranker.PersonCandidate
	innerCircles := []string{"Work_Inner", "Family"}
	if s.peopleRepo != nil && s.cfg.Agents.Reranker.Enabled {
		peopleResults, err := s.SearchPeople(ctx, userID, qVec, float32(minSafetyThreshold), 20, innerCircles)
		if err != nil {
			s.logger.Warn("failed to search people", "error", err)
		} else {
			for _, pr := range peopleResults {
				personCandidates = append(personCandidates, reranker.PersonCandidate{
					PersonID: pr.Person.ID,
					Score:    pr.Score,
					Person:   pr.Person,
				})
			}
		}
	}

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
		candidates := make([]reranker.Candidate, 0, len(matches))
		for _, m := range matches {
			if topic, ok := topicMap[m.topicID]; ok {
				msgCount := int(topic.EndMsgID - topic.StartMsgID + 1)
				if msgCount < 0 {
					msgCount = 0
				}
				candidates = append(candidates, reranker.Candidate{
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

		// Run reranker agent
		result, err := s.rerankViaAgent(ctx, userID, candidates, personCandidates, contextualizedQuery, query, currentMessages, userProfile, recentTopics, opts.MediaParts)
		if err != nil {
			s.logger.Warn("reranker failed, falling back to vector search", "error", err)
			RecordRerankerFallback(userID, "error")
		} else {
			// Build maps for selected topics and reasons
			selectedIDs := make(map[int64]bool)
			topicReasons := make(map[int64]string)
			for _, t := range result.Topics {
				// Parse "Topic:N" to numeric ID
				topicID, err := t.GetNumericID()
				if err != nil {
					s.logger.Warn("invalid topic ID from reranker", "id", t.ID, "error", err)
					continue
				}
				selectedIDs[topicID] = true
				if t.Reason != "" {
					topicReasons[topicID] = t.Reason
				}
			}

			var filteredMatches []match
			for _, m := range matches {
				if selectedIDs[m.topicID] {
					m.reason = topicReasons[m.topicID]
					filteredMatches = append(filteredMatches, m)
				}
			}
			matches = filteredMatches

			// v0.5.1: Load selected people by IDs from reranker result
			if len(result.People) > 0 && s.peopleRepo != nil {
				peopleIDs := result.PeopleIDs()
				selectedPeople, err := s.peopleRepo.GetPeopleByIDs(peopleIDs)
				if err != nil {
					s.logger.Warn("failed to load selected people", "error", err)
				} else {
					// Store in personCandidates for later use
					// (personCandidates will be converted to result.People)
					personCandidates = nil // Clear candidates, we'll rebuild
					for _, p := range selectedPeople {
						personCandidates = append(personCandidates, reranker.PersonCandidate{
							PersonID: p.ID,
							Person:   p,
						})
					}
				}
			} else {
				// No people selected by reranker
				personCandidates = nil
			}
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

		if len(topicMsgs) > 0 {
			results = append(results, TopicSearchResult{
				Topic:    topic,
				Score:    m.score,
				Messages: topicMsgs,
				Reason:   m.reason,
			})
		}
	}

	debugInfo.Results = results

	// Record RAG metrics
	RecordRAGRetrieval(userID, len(results) > 0)
	RecordRAGLatency(userID, opts.Source, time.Since(startTime).Seconds())

	// v0.5.1: Build final result with selected people
	var selectedPeople []storage.Person
	for _, pc := range personCandidates {
		selectedPeople = append(selectedPeople, pc.Person)
	}

	return &RetrievalResult{
		Topics: results,
		People: selectedPeople,
	}, debugInfo, nil
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

// PersonSearchResult represents a matched person with their score.
type PersonSearchResult struct {
	Person storage.Person
	Score  float32
}

// RetrievalResult contains both topics and selected people from RAG retrieval.
type RetrievalResult struct {
	Topics []TopicSearchResult
	People []storage.Person // Selected by reranker (excludes inner circle)
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

	people, err := s.peopleRepo.GetPeopleByIDs(ids)
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

// rerankViaAgent delegates reranking to the Reranker agent.
func (s *Service) rerankViaAgent(
	ctx context.Context,
	userID int64,
	candidates []reranker.Candidate,
	personCandidates []reranker.PersonCandidate,
	contextualizedQuery string,
	originalQuery string,
	currentMessages string,
	userProfile string,
	recentTopics string,
	mediaParts []interface{},
) (*reranker.Result, error) {
	if s.rerankerAgent == nil {
		return nil, fmt.Errorf("reranker agent not configured")
	}
	startTime := time.Now()

	// Build request
	req := &agent.Request{
		Params: map[string]any{
			reranker.ParamCandidates:          candidates,
			reranker.ParamPersonCandidates:    personCandidates,
			reranker.ParamContextualizedQuery: contextualizedQuery,
			reranker.ParamOriginalQuery:       originalQuery,
			reranker.ParamCurrentMessages:     currentMessages,
			reranker.ParamMediaParts:          mediaParts,
			"user_id":                         userID,
		},
	}

	// Try to get SharedContext from ctx
	if shared := agent.FromContext(ctx); shared != nil {
		req.Shared = shared
	} else {
		// Fallback: create minimal shared context for the agent
		req.Shared = &agent.SharedContext{
			UserID:       userID,
			Profile:      userProfile,
			RecentTopics: recentTopics,
			Language:     s.cfg.Bot.Language,
		}
	}

	resp, err := s.rerankerAgent.Execute(ctx, req)
	if err != nil {
		return nil, err
	}

	result, ok := resp.Structured.(*reranker.Result)
	if !ok {
		return nil, fmt.Errorf("unexpected result type from reranker agent")
	}

	// Record metrics
	duration := time.Since(startTime).Seconds()
	RecordRerankerDuration(userID, duration)
	RecordRerankerCandidatesOutput(userID, len(result.Topics))

	return result, nil
}

// formatSessionMessages formats the current session history for the reranker prompt.
func (s *Service) formatSessionMessages(history []storage.Message) string {
	if len(history) == 0 {
		return "(no current session messages)"
	}

	var sb strings.Builder
	// Take last N messages to avoid overwhelming the reranker
	maxMessages := 10
	start := 0
	if len(history) > maxMessages {
		start = len(history) - maxMessages
		fmt.Fprintf(&sb, "... (%d earlier messages omitted)\n\n", start)
	}

	for _, m := range history[start:] {
		role := m.Role
		if role == "assistant" {
			role = "Assistant"
		} else if role == "user" {
			role = "User"
		}
		// Truncate very long messages
		content := m.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		fmt.Fprintf(&sb, "[%s]: %s\n", role, content)
	}
	return sb.String()
}

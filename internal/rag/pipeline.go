package rag

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/reranker"
	"github.com/runixer/laplaced/internal/storage"
)

// topicCandidate represents a topic match with score and optional reranker reason.
type topicCandidate struct {
	topicID int64
	score   float32
	reason  string // Why reranker chose this topic
}

// rerankerInput holds all candidates for the reranker agent.
type rerankerInput struct {
	topicCandidates     []topicCandidate
	topicMap            map[int64]storage.Topic
	personCandidates    []reranker.PersonCandidate
	artifactCandidates  []reranker.ArtifactCandidate
	contextualizedQuery string
	originalQuery       string
	currentMessages     string
	userProfile         string
	recentTopics        string
	mediaParts          []interface{}
}

// rerankerOutput holds the reranker agent's selection.
type rerankerOutput struct {
	selectedTopicIDs    []int64
	topicReasons        map[int64]string // ID -> reason
	selectedPersonIDs   []int64
	selectedArtifactIDs []int64
	personCandidates    []reranker.PersonCandidate // For final result
}

// searchTopicCandidates performs vector search for topics.
// Returns scored candidates and vectors scanned count.
func (s *Service) searchTopicCandidates(userID int64, embedding []float32, limit int) ([]topicCandidate, int) {
	s.mu.RLock()
	userVectors := s.topicVectors[userID]
	vectorsScanned := len(userVectors)

	// Convert []TopicVectorItem to []embeddingItem for generic search
	items := make([]embeddingItem, len(userVectors))
	for i := range userVectors {
		items[i] = userVectors[i]
	}
	s.mu.RUnlock()

	results := s.vectorSearch(userID, embedding, items, VectorSearchConfig{
		Limit:      limit,
		MetricType: searchTypeTopics,
	})

	candidates := make([]topicCandidate, len(results))
	for i, r := range results {
		candidates[i] = topicCandidate{topicID: r.ID, score: r.Score}
	}
	return candidates, vectorsScanned
}

// prepareRerankerContext loads user profile, recent topics, formats session messages.
// Returns formatted strings for reranker prompt.
func (s *Service) prepareRerankerContext(ctx context.Context, userID int64, history []storage.Message) (userProfile, recentTopics, currentMessages string, err error) {
	// Try to use SharedContext if available (loaded once in processMessageGroup)
	if shared := agent.FromContext(ctx); shared != nil {
		// Use pre-loaded data from SharedContext
		// Use compact format (no Fact IDs) to prevent ID confusion with Person/Topic/Artifact
		userProfile = storage.FormatUserProfileCompact(shared.ProfileFacts)
		recentTopics = shared.RecentTopics
	} else {
		// Fallback: load directly (for tests or when contextService is not configured)
		// Use compact format (no Fact IDs) to prevent ID confusion
		allFacts, err := s.factRepo.GetFacts(userID)
		if err == nil {
			userProfile = storage.FormatUserProfileCompact(storage.FilterProfileFacts(allFacts))
		} else {
			s.logger.Warn("failed to load facts for reranker", "error", err)
			userProfile = storage.FormatUserProfileCompact(nil)
		}

		// Load recent topics for reranker context
		recentTopicsCount := s.cfg.RAG.GetRecentTopicsInContext()
		if recentTopicsCount > 0 {
			topics, err := s.GetRecentTopics(userID, recentTopicsCount)
			if err != nil {
				s.logger.Warn("failed to get recent topics for reranker", "error", err)
			}
			recentTopics = storage.FormatRecentTopics(topics)
		} else {
			recentTopics = storage.FormatRecentTopics(nil)
		}
	}

	// Format current session messages for reranker
	currentMessages = s.formatSessionMessages(history)

	return userProfile, recentTopics, currentMessages, nil
}

// executeReranker runs the reranker agent with fallback.
// Returns selected topic IDs, people IDs, artifact IDs, and reasons.
func (s *Service) executeReranker(ctx context.Context, userID int64, input rerankerInput) (*rerankerOutput, error) {
	RecordRerankerCandidatesInput(userID, len(input.topicCandidates))

	// Build reranker candidates with topic metadata
	candidates := make([]reranker.Candidate, 0, len(input.topicCandidates))
	for _, tc := range input.topicCandidates {
		if topic, ok := input.topicMap[tc.topicID]; ok {
			msgCount := int(topic.EndMsgID - topic.StartMsgID + 1)
			if msgCount < 0 {
				msgCount = 0
			}
			candidates = append(candidates, reranker.Candidate{
				TopicID:      tc.topicID,
				Score:        tc.score,
				Topic:        topic,
				MessageCount: msgCount,
				SizeChars:    topic.SizeChars,
			})
		}
	}

	// Call reranker agent
	result, err := s.rerankViaAgent(ctx, userID, candidates, input.personCandidates, input.artifactCandidates,
		input.contextualizedQuery, input.originalQuery, input.currentMessages, input.userProfile, input.recentTopics, input.mediaParts)
	if err != nil {
		s.logger.Warn("reranker failed, falling back to vector search", "error", err)
		RecordRerankerFallback(userID, "error")
		return nil, err
	}

	output := &rerankerOutput{
		topicReasons:        make(map[int64]string),
		selectedArtifactIDs: result.ArtifactIDs(),
	}

	// Parse selected topics with reasons
	for _, t := range result.Topics {
		topicID, err := t.GetNumericID()
		if err != nil {
			s.logger.Warn("invalid topic ID from reranker", "id", t.ID, "error", err)
			continue
		}
		output.selectedTopicIDs = append(output.selectedTopicIDs, topicID)
		if t.Reason != "" {
			output.topicReasons[topicID] = t.Reason
		}
	}

	// Load selected people by IDs from reranker result
	if len(result.People) > 0 && s.peopleRepo != nil {
		peopleIDs := result.PeopleIDs()
		selectedPeople, err := s.peopleRepo.GetPeopleByIDs(userID, peopleIDs)
		if err != nil {
			s.logger.Warn("failed to load selected people", "error", err)
		} else {
			output.selectedPersonIDs = peopleIDs
			output.personCandidates = make([]reranker.PersonCandidate, len(selectedPeople))
			for i, p := range selectedPeople {
				output.personCandidates[i] = reranker.PersonCandidate{
					PersonID: p.ID,
					Person:   p,
				}
			}
		}
	}

	return output, nil
}

// buildRetrievalResult assembles final RetrievalResult from selected candidates.
// Loads topic messages, builds artifact results.
func (s *Service) buildRetrievalResult(ctx context.Context, userID int64, topicMatches []topicCandidate, topicMap map[int64]storage.Topic, rerankerOutput *rerankerOutput, artifactCandidates []reranker.ArtifactCandidate, useReranker bool) (*RetrievalResult, error) {
	var results []TopicSearchResult
	seenMsgIDs := make(map[int64]bool)

	for _, m := range topicMatches {
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

	// Build selected people
	var selectedPeople []storage.Person
	if rerankerOutput != nil {
		for _, pc := range rerankerOutput.personCandidates {
			selectedPeople = append(selectedPeople, pc.Person)
		}
	}

	// Build artifact results
	var artifactResults []ArtifactResult
	var selectedArtifactIDs []int64

	if s.cfg.Artifacts.Enabled && s.cfg.RAG.Enabled && len(artifactCandidates) > 0 {
		if rerankerOutput != nil && len(rerankerOutput.selectedArtifactIDs) > 0 {
			// Reranker selected specific artifacts: filter by selected IDs
			selectedSet := make(map[int64]bool, len(rerankerOutput.selectedArtifactIDs))
			for _, id := range rerankerOutput.selectedArtifactIDs {
				selectedSet[id] = true
			}
			for _, ac := range artifactCandidates {
				if selectedSet[ac.ArtifactID] {
					artifactResults = append(artifactResults, ArtifactResult{
						ArtifactID:   ac.ArtifactID,
						FileType:     ac.FileType,
						OriginalName: ac.OriginalName,
						Summary:      ac.Summary,
						Keywords:     ac.Keywords,
						Entities:     ac.Entities,
						RAGHints:     ac.RAGHints,
						Score:        ac.Score,
					})
				}
			}
			selectedArtifactIDs = rerankerOutput.selectedArtifactIDs
		} else if !useReranker {
			// Reranker disabled: select top-N by vector score (fallback)
			maxArtifacts := s.cfg.Agents.Reranker.Artifacts.Max
			if maxArtifacts <= 0 {
				maxArtifacts = 3
			}
			for i, ac := range artifactCandidates {
				if i >= maxArtifacts {
					break
				}
				artifactResults = append(artifactResults, ArtifactResult{
					ArtifactID:   ac.ArtifactID,
					FileType:     ac.FileType,
					OriginalName: ac.OriginalName,
					Summary:      ac.Summary,
					Keywords:     ac.Keywords,
					Entities:     ac.Entities,
					RAGHints:     ac.RAGHints,
					Score:        ac.Score,
				})
				selectedArtifactIDs = append(selectedArtifactIDs, ac.ArtifactID)
			}
		}
	}

	return &RetrievalResult{
		Topics:              results,
		People:              selectedPeople,
		Artifacts:           artifactResults,
		SelectedArtifactIDs: selectedArtifactIDs,
	}, nil
}

// filterMatchesByReranker filters topic matches by reranker selection and adds reasons.
func filterMatchesByReranker(matches []topicCandidate, selectedIDs []int64, reasons map[int64]string) []topicCandidate {
	selectedSet := make(map[int64]bool, len(selectedIDs))
	for _, id := range selectedIDs {
		selectedSet[id] = true
	}

	var filtered []topicCandidate
	for _, m := range matches {
		if selectedSet[m.topicID] {
			if reason, ok := reasons[m.topicID]; ok {
				m.reason = reason
			}
			filtered = append(filtered, m)
		}
	}
	return filtered
}

// searchPeopleAndArtifacts searches for people and artifact candidates.
// Returns person candidates and artifact candidates.
func (s *Service) searchPeopleAndArtifacts(ctx context.Context, userID int64, embedding []float32, threshold float32) ([]reranker.PersonCandidate, []reranker.ArtifactCandidate) {
	var personCandidates []reranker.PersonCandidate
	innerCircles := []string{"Work_Inner", "Family"}
	if s.peopleRepo != nil && s.cfg.Agents.Reranker.Enabled {
		peopleResults, err := s.SearchPeople(ctx, userID, embedding, threshold, 20, innerCircles)
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

	var artifactCandidates []reranker.ArtifactCandidate
	if s.cfg.Artifacts.Enabled && s.cfg.RAG.Enabled && s.cfg.Agents.Reranker.Enabled && len(s.artifactVectors) > 0 {
		artifacts, err := s.SearchArtifactsBySummary(ctx, userID, embedding, threshold)
		if err != nil {
			s.logger.Warn("artifact summary search failed", "error", err)
		} else {
			for _, ar := range artifacts {
				artifactCandidates = append(artifactCandidates, reranker.ArtifactCandidate{
					ArtifactID:   ar.ArtifactID,
					Score:        ar.Score,
					FileType:     ar.FileType,
					OriginalName: ar.OriginalName,
					Summary:      ar.Summary,
					Keywords:     ar.Keywords,
					Entities:     ar.Entities,
					RAGHints:     ar.RAGHints,
				})
			}
		}
	}

	return personCandidates, artifactCandidates
}

// limitMatches limits topic candidates to maxCandidates.
func limitMatches(matches []topicCandidate, maxCandidates int) []topicCandidate {
	if maxCandidates > 0 && len(matches) > maxCandidates {
		return matches[:maxCandidates]
	}
	return matches
}

// loadTopicMap fetches topics by IDs and returns a map.
func (s *Service) loadTopicMap(ctx context.Context, userID int64, matches []topicCandidate) (map[int64]storage.Topic, error) {
	topicIDs := make([]int64, len(matches))
	for i, m := range matches {
		topicIDs[i] = m.topicID
	}

	topics, err := s.topicRepo.GetTopicsByIDs(userID, topicIDs)
	if err != nil {
		return nil, err
	}

	topicMap := make(map[int64]storage.Topic)
	for _, t := range topics {
		topicMap[t.ID] = t
	}
	return topicMap, nil
}

// shouldUseReranker determines if reranker should be used based on candidates.
func (s *Service) shouldUseReranker(lenMatches, lenPeople, lenArtifacts int) bool {
	return s.cfg.Agents.Reranker.Enabled && (lenMatches > 0 || lenPeople > 0 || lenArtifacts > 0)
}

// getMaxCandidates returns the max candidates based on reranker usage.
func (s *Service) getMaxCandidates(useReranker bool) int {
	if useReranker {
		maxCandidates := s.cfg.Agents.Reranker.Candidates
		if maxCandidates <= 0 {
			return 50
		}
		return maxCandidates
	}
	return s.cfg.RAG.GetRetrievedTopicsCount()
}

// rerankViaAgent delegates reranking to the Reranker agent.
// Extracted from retrieval.go for pipeline module.
func (s *Service) rerankViaAgent(
	ctx context.Context,
	userID int64,
	candidates []reranker.Candidate,
	personCandidates []reranker.PersonCandidate,
	artifactCandidates []reranker.ArtifactCandidate,
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
			reranker.ParamArtifactCandidates:  artifactCandidates,
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
// Extracted from retrieval.go for pipeline module.
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

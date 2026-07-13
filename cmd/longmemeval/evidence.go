package main

import (
	"fmt"
	"sort"

	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

type evidenceRecall struct {
	RequiredSessions int      `json:"required_sessions"`
	MatchedSessions  int      `json:"matched_sessions"`
	Recall           float64  `json:"recall"`
	MatchedIDs       []string `json:"matched_session_ids,omitempty"`
	MissingIDs       []string `json:"missing_session_ids,omitempty"`
}

type retrievalEvidence struct {
	Candidate    evidenceRecall `json:"candidate"`
	PostReranker evidenceRecall `json:"post_reranker"`
	FinalContext evidenceRecall `json:"final_context"`
	UsedReranker bool           `json:"used_reranker"`
}

func calculateRetrievalEvidence(store *storage.Store, scopeID storage.ScopeID, eval evalCase, snapshot ingestionSnapshot, debug *rag.RetrievalDebugInfo) (*retrievalEvidence, error) {
	if debug == nil || len(eval.AnswerSessionIDs) == 0 {
		return nil, nil
	}
	messageSession := make(map[int64]string)
	allIDs := make([]int64, 0)
	for _, session := range snapshot.Sessions {
		for _, messageID := range session.MessageIDs {
			messageSession[messageID] = session.SessionID
			allIDs = append(allIDs, messageID)
		}
	}
	messages, err := store.GetMessagesByIDs(scopeID, allIDs)
	if err != nil {
		return nil, fmt.Errorf("load provenance messages: %w", err)
	}
	topicSessions := make(map[int64]map[string]struct{})
	for _, message := range messages {
		if message.TopicID == nil {
			continue
		}
		sessionID, ok := messageSession[message.ID]
		if !ok {
			continue
		}
		if topicSessions[*message.TopicID] == nil {
			topicSessions[*message.TopicID] = make(map[string]struct{})
		}
		topicSessions[*message.TopicID][sessionID] = struct{}{}
	}

	candidateSessions := make(map[string]struct{})
	for _, candidate := range debug.Candidates {
		addSessions(candidateSessions, topicSessions[candidate.TopicID])
	}
	return &retrievalEvidence{
		Candidate:    buildEvidenceRecall(eval.AnswerSessionIDs, candidateSessions),
		PostReranker: buildEvidenceRecall(eval.AnswerSessionIDs, sessionsFromResults(debug.PostReranker, messageSession)),
		FinalContext: buildEvidenceRecall(eval.AnswerSessionIDs, sessionsFromResults(debug.FinalContext, messageSession)),
		UsedReranker: debug.UsedReranker,
	}, nil
}

func sessionsFromResults(results []rag.TopicSearchResult, messageSession map[int64]string) map[string]struct{} {
	sessions := make(map[string]struct{})
	for _, result := range results {
		for _, message := range result.Messages {
			if sessionID, ok := messageSession[message.ID]; ok {
				sessions[sessionID] = struct{}{}
			}
		}
	}
	return sessions
}

func addSessions(destination, source map[string]struct{}) {
	for sessionID := range source {
		destination[sessionID] = struct{}{}
	}
}

func buildEvidenceRecall(requiredIDs []string, represented map[string]struct{}) evidenceRecall {
	required := make(map[string]struct{}, len(requiredIDs))
	for _, id := range requiredIDs {
		required[id] = struct{}{}
	}
	result := evidenceRecall{RequiredSessions: len(required)}
	for id := range required {
		if _, ok := represented[id]; ok {
			result.MatchedIDs = append(result.MatchedIDs, id)
		} else {
			result.MissingIDs = append(result.MissingIDs, id)
		}
	}
	sort.Strings(result.MatchedIDs)
	sort.Strings(result.MissingIDs)
	result.MatchedSessions = len(result.MatchedIDs)
	if result.RequiredSessions > 0 {
		result.Recall = float64(result.MatchedSessions) / float64(result.RequiredSessions)
	}
	return result
}

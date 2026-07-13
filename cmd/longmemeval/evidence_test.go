package main

import (
	"testing"

	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestBuildEvidenceRecall(t *testing.T) {
	recall := buildEvidenceRecall([]string{"s2", "s1", "s1"}, map[string]struct{}{"s1": {}})
	require.Equal(t, 2, recall.RequiredSessions)
	require.Equal(t, 1, recall.MatchedSessions)
	require.Equal(t, 0.5, recall.Recall)
	require.Equal(t, []string{"s1"}, recall.MatchedIDs)
	require.Equal(t, []string{"s2"}, recall.MissingIDs)
}

func TestSessionsFromResultsUsesActualMessages(t *testing.T) {
	messageSessions := map[int64]string{1: "s1", 2: "s2", 3: "s3"}
	results := []rag.TopicSearchResult{
		{Topic: storage.Topic{ID: 10}, Messages: []storage.Message{{ID: 1}, {ID: 2}}},
		{Topic: storage.Topic{ID: 11}, Messages: []storage.Message{{ID: 2}, {ID: 99}}},
	}
	sessions := sessionsFromResults(results, messageSessions)
	require.Equal(t, map[string]struct{}{"s1": {}, "s2": {}}, sessions)
}

func TestCandidateMergedTopicCoversMultipleSessions(t *testing.T) {
	represented := make(map[string]struct{})
	addSessions(represented, map[string]struct{}{"s1": {}, "s2": {}})
	recall := buildEvidenceRecall([]string{"s1", "s2"}, represented)
	require.Equal(t, 1.0, recall.Recall)
}

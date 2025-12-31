package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestStats(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	stats := []Stat{
		{UserID: 123, TokensUsed: 100, CostUSD: 0.01},
		{UserID: 456, TokensUsed: 200, CostUSD: 0.02},
		{UserID: 123, TokensUsed: 50, CostUSD: 0.005},
	}

	// Add stats
	for _, stat := range stats {
		err := store.AddStat(stat)
		assert.NoError(t, err)
	}

	// Get stats
	result, err := store.GetStats()
	assert.NoError(t, err)
	assert.Equal(t, 2, len(result))

	// Check user 123
	assert.Equal(t, 150, result[123].TokensUsed)
	assert.InDelta(t, 0.015, result[123].CostUSD, 0.0001)

	// Check user 456
	assert.Equal(t, 200, result[456].TokensUsed)
	assert.InDelta(t, 0.02, result[456].CostUSD, 0.0001)
}

func TestGetDashboardStats(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)

	// 1. Add Topics
	// Topic 1: Processed, Consolidated
	_, _ = store.AddTopic(Topic{UserID: userID, Summary: "T1", StartMsgID: 1, EndMsgID: 2, FactsExtracted: true, IsConsolidated: true})
	// Topic 2: Not Processed, Not Consolidated
	_, _ = store.AddTopic(Topic{UserID: userID, Summary: "T2", StartMsgID: 3, EndMsgID: 5, FactsExtracted: false, IsConsolidated: false})

	// 2. Add Facts
	_, _ = store.AddFact(Fact{UserID: userID, Content: "F1", Category: "bio", Type: "identity"})
	_, _ = store.AddFact(Fact{UserID: userID, Content: "F2", Category: "bio", Type: "identity"})
	_, _ = store.AddFact(Fact{UserID: userID, Content: "F3", Category: "work", Type: "context"})

	// 3. Add Messages
	_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: "m1", TopicID: new(int64)}) // Processed
	_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: "m2"})                      // Unprocessed

	// 4. Add RAG Logs
	_ = store.AddRAGLog(RAGLog{UserID: userID, TotalCostUSD: 0.01})
	_ = store.AddRAGLog(RAGLog{UserID: userID, TotalCostUSD: 0.02})

	// Get Stats
	stats, err := store.GetDashboardStats(userID)
	assert.NoError(t, err)

	// Verify Topics
	assert.Equal(t, 2, stats.TotalTopics)
	assert.Equal(t, 1, stats.ConsolidatedTopics)
	assert.Equal(t, 50.0, stats.ProcessedTopicsPct)
	// Avg size: (2-1+1) + (5-3+1) = 2 + 3 = 5 / 2 = 2.5
	assert.Equal(t, 2.5, stats.AvgTopicSize)

	// Verify Facts
	assert.Equal(t, 3, stats.TotalFacts)
	assert.Equal(t, 2, stats.FactsByCategory["bio"])
	assert.Equal(t, 1, stats.FactsByCategory["work"])
	assert.Equal(t, 2, stats.FactsByType["identity"])
	assert.Equal(t, 1, stats.FactsByType["context"])

	// Verify Messages
	assert.Equal(t, 2, stats.TotalMessages)
	assert.Equal(t, 1, stats.UnprocessedMessages)

	// Verify RAG
	assert.Equal(t, 2, stats.TotalRAGQueries)
	assert.InDelta(t, 0.015, stats.AvgRAGCost, 0.0001)

	// Verify Activity (Today)
	// SQLite date() function returns UTC date by default, so we should expect UTC date here
	today := time.Now().UTC().Format("2006-01-02")
	assert.Equal(t, 2, stats.MessagesPerDay[today])
	assert.Equal(t, 3, stats.FactsGrowth[today])
}

package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRAGLogs(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	log1 := RAGLog{
		UserID:        userID,
		OriginalQuery: "Q1",
		LLMResponse:   "A1",
		TotalCostUSD:  0.01,
	}
	log2 := RAGLog{
		UserID:        userID,
		OriginalQuery: "Topic Extraction",
		LLMResponse:   "Extracted",
		TotalCostUSD:  0.02,
	}

	// 1. Add
	err := store.AddRAGLog(log1)
	assert.NoError(t, err)
	err = store.AddRAGLog(log2)
	assert.NoError(t, err)

	// 2. Get All (User)
	logs, err := store.GetRAGLogs(userID, 10)
	assert.NoError(t, err)
	assert.Len(t, logs, 2)
	assert.Equal(t, "Topic Extraction", logs[0].OriginalQuery) // DESC

	// 3. Get All (Admin)
	logs, err = store.GetRAGLogs(0, 10)
	assert.NoError(t, err)
	assert.Len(t, logs, 2)

	// 4. Get Topic Extraction Logs
	logs, total, err := store.GetTopicExtractionLogs(10, 0)
	assert.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, logs, 1)
	assert.Equal(t, "Topic Extraction", logs[0].OriginalQuery)
}

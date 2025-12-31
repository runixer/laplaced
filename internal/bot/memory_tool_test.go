package bot

import (
	"context"
	"testing"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestPerformManageMemory_Add(t *testing.T) {
	// Setup
	mockStore := new(MockStorage)
	mockORClient := new(MockOpenRouterClient)
	cfg := &config.Config{
		RAG: config.RAGConfig{
			EmbeddingModel: "test-embedding-model",
		},
	}
	bot := &Bot{
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		logRepo:         mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		cfg:             cfg,
	}

	userID := int64(123)
	queryJSON := `{"action": "add", "entity": "User", "content": "Likes pizza", "category": "food", "type": "preference", "importance": 80}`
	args := map[string]interface{}{
		"query": queryJSON,
	}

	// Mocks
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
		return req.Input[0] == "Likes pizza"
	})).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{
			{Embedding: []float32{0.1, 0.2}},
		},
	}, nil)

	mockStore.On("AddFact", mock.MatchedBy(func(f storage.Fact) bool {
		return f.UserID == userID && f.Content == "Likes pizza" && f.Entity == "User" && f.Category == "food" && f.Type == "preference" && f.Importance == 80
	})).Return(int64(1), nil)

	mockStore.On("AddFactHistory", mock.Anything).Return(nil)

	// Execute
	result, err := bot.performManageMemory(context.Background(), userID, args)

	// Assert
	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully processed 1 operations")
	assert.Contains(t, result, "Op 1 (add): Success")
	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

func TestPerformManageMemory_InvalidJSON(t *testing.T) {
	bot := &Bot{}
	userID := int64(123)
	args := map[string]interface{}{
		"query": "{invalid json",
	}

	result, err := bot.performManageMemory(context.Background(), userID, args)

	assert.NoError(t, err) // It returns error string, not error object
	assert.Contains(t, result, "Error parsing query JSON")
}

func TestPerformManageMemory_MissingQuery(t *testing.T) {
	bot := &Bot{}
	userID := int64(123)
	args := map[string]interface{}{}

	result, err := bot.performManageMemory(context.Background(), userID, args)

	assert.NoError(t, err)
	assert.Equal(t, "Error: query argument is missing", result)
}

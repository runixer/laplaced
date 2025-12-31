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

func TestPerformManageMemory_BatchOperations(t *testing.T) {
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
	queryJSON := `{
		"operations": [
			{
				"action": "add",
				"entity": "User",
				"content": "Likes pizza",
				"category": "food",
				"type": "preference",
				"importance": 80
			},
			{
				"action": "update",
				"fact_id": 10,
				"content": "Likes sushi",
				"type": "preference",
				"importance": 90
			},
			{
				"action": "delete",
				"fact_id": 20
			}
		]
	}`
	args := map[string]interface{}{
		"query": queryJSON,
	}

	// Mocks for Add
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
		return req.Input[0] == "Likes pizza"
	})).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{
			{Embedding: []float32{0.1, 0.1}},
		},
	}, nil)

	mockStore.On("AddFact", mock.MatchedBy(func(f storage.Fact) bool {
		return f.UserID == userID && f.Content == "Likes pizza"
	})).Return(int64(1), nil)

	mockStore.On("AddFactHistory", mock.Anything).Return(nil)

	// Mocks for Update
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
		return req.Input[0] == "Likes sushi"
	})).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{
			{Embedding: []float32{0.2, 0.2}},
		},
	}, nil)

	mockStore.On("GetFactsByIDs", []int64{10}).Return([]storage.Fact{{ID: 10, Content: "Likes pizza"}}, nil)

	mockStore.On("UpdateFact", mock.MatchedBy(func(f storage.Fact) bool {
		return f.ID == 10 && f.UserID == userID && f.Content == "Likes sushi"
	})).Return(nil)

	// Mocks for Delete
	mockStore.On("GetFactsByIDs", []int64{20}).Return([]storage.Fact{{ID: 20, Content: "Likes burgers"}}, nil)
	mockStore.On("DeleteFact", userID, int64(20)).Return(nil)

	// Execute
	result, err := bot.performManageMemory(context.Background(), userID, args)

	// Assert
	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully processed 3 operations")
	assert.Contains(t, result, "Op 1 (add): Success")
	assert.Contains(t, result, "Op 2 (update): Success")
	assert.Contains(t, result, "Op 3 (delete): Success")

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

func TestPerformManageMemory_BatchOperations_PartialFailure(t *testing.T) {
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
	queryJSON := `{
		"operations": [
			{
				"action": "add",
				"entity": "User",
				"content": "Likes pizza"
			},
			{
				"action": "delete",
				"fact_id": 999
			}
		]
	}`
	args := map[string]interface{}{
		"query": queryJSON,
	}

	// Mocks for Add (Success)
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1}}},
	}, nil)
	mockStore.On("AddFact", mock.Anything).Return(int64(1), nil)
	mockStore.On("AddFactHistory", mock.Anything).Return(nil)

	// Mocks for Delete (Failure)
	mockStore.On("GetFactsByIDs", []int64{999}).Return([]storage.Fact{}, nil)
	mockStore.On("DeleteFact", userID, int64(999)).Return(assert.AnError)

	// Execute
	result, err := bot.performManageMemory(context.Background(), userID, args)

	// Assert
	assert.NoError(t, err) // The function itself shouldn't return error, but report it in string
	assert.Contains(t, result, "Completed with 1 errors")
	assert.Contains(t, result, "Op 1 (add): Success")
	assert.Contains(t, result, "Op 2 (delete): Failed")

	mockStore.AssertExpectations(t)
}

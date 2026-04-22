package bot

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/runixer/laplaced/internal/bot/tools"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestParseMemoryOpParams(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]interface{}
		want   tools.MemoryOpParams
	}{
		{
			name: "all fields present",
			params: map[string]interface{}{
				"action":     "add",
				"content":    "Likes Go",
				"category":   "tech",
				"type":       "preference",
				"reason":     "user said so",
				"importance": float64(85),
				"fact_id":    float64(42),
			},
			want: tools.MemoryOpParams{
				Action:     "add",
				Content:    "Likes Go",
				Category:   "tech",
				FactType:   "preference",
				Reason:     "user said so",
				Importance: 85,
				FactID:     42,
			},
		},
		{
			name: "minimal fields",
			params: map[string]interface{}{
				"action": "delete",
			},
			want: tools.MemoryOpParams{
				Action: "delete",
			},
		},
		{
			name: "numeric fields as float64 from JSON",
			params: map[string]interface{}{
				"action":     "update",
				"fact_id":    float64(123),
				"importance": float64(50),
			},
			want: tools.MemoryOpParams{
				Action:     "update",
				FactID:     123,
				Importance: 50,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tools.ParseMemoryOpParams(tt.params)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPerformManageMemory_Add(t *testing.T) {
	// Setup
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	cfg.Embedding.Model = "test-embedding-model"

	toolExecutor := tools.NewToolExecutor(mockORClient, mockStore, mockStore, cfg, testutil.TestLogger())

	userID := int64(123)
	queryJSON := `{"action": "add", "content": "Likes pizza", "category": "food", "type": "preference", "importance": 80}`

	// Mocks
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
		return req.Input[0] == "Likes pizza"
	})).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{
			{Embedding: []float32{0.1, 0.2}},
		},
	}, nil)

	mockStore.On("AddFact", mock.MatchedBy(func(f storage.Fact) bool {
		return f.UserID == userID && f.Content == "Likes pizza" && f.Category == "food" && f.Type == "preference" && f.Importance == 80
	})).Return(int64(1), nil)

	mockStore.On("AddFactHistory", mock.Anything).Return(nil)

	// Execute - properly escape the JSON
	arguments, _ := json.Marshal(map[string]string{"query": queryJSON})
	result, err := toolExecutor.ExecuteToolCall(context.Background(), tools.CallContext{UserID: userID}, "manage_memory", string(arguments))

	// Assert
	assert.NoError(t, err)
	assert.Contains(t, result.Content, "Successfully processed 1 operations")
	assert.Contains(t, result.Content, "Op 1 (add): Success")
	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

func TestPerformManageMemory_InvalidJSON(t *testing.T) {
	cfg := testutil.TestConfig()
	toolExecutor := tools.NewToolExecutor(nil, nil, nil, cfg, testutil.TestLogger())

	userID := int64(123)

	result, err := toolExecutor.ExecuteToolCall(context.Background(), tools.CallContext{UserID: userID}, "manage_memory", `{"query":"{invalid json"}`)

	assert.Error(t, err)
	assert.Empty(t, result) // error returned, no result string
	assert.Contains(t, err.Error(), "parsing query JSON")
}

func TestPerformManageMemory_MissingQuery(t *testing.T) {
	cfg := testutil.TestConfig()
	toolExecutor := tools.NewToolExecutor(nil, nil, nil, cfg, testutil.TestLogger())

	userID := int64(123)

	result, err := toolExecutor.ExecuteToolCall(context.Background(), tools.CallContext{UserID: userID}, "manage_memory", `{}`)

	assert.Error(t, err)
	assert.Empty(t, result)
	assert.Contains(t, err.Error(), "query argument is missing")
}

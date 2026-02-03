package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAddAgentLog(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	cost := 0.001
	log := AgentLog{
		UserID:            123,
		AgentType:         "laplace",
		InputPrompt:       "Test prompt",
		InputContext:      `{"model":"test"}`,
		OutputResponse:    "Test response",
		OutputParsed:      `{"result":"test"}`,
		OutputContext:     `{"usage":{"prompt_tokens":10,"completion_tokens":20}}`,
		Model:             "gemini-2.0-flash",
		PromptTokens:      10,
		CompletionTokens:  20,
		TotalCost:         &cost,
		DurationMs:        100,
		Metadata:          `{"test":"data"}`,
		Success:           true,
		ErrorMessage:      "",
		ConversationTurns: `[{"role":"user","content":"test"}]`,
	}

	// Add log
	err := store.AddAgentLog(log)
	assert.NoError(t, err)
}

func TestGetAgentLogs(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	now := time.Now().UTC()

	// Create test logs
	cost1 := 0.001
	cost2 := 0.002
	logs := []AgentLog{
		{
			UserID:           userID,
			AgentType:        "laplace",
			InputPrompt:      "Prompt 1",
			OutputResponse:   "Response 1",
			Model:            "gemini-2.0-flash",
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalCost:        &cost1,
			DurationMs:       100,
			Metadata:         "{}",
			Success:          true,
			CreatedAt:        now,
		},
		{
			UserID:           userID,
			AgentType:        "laplace",
			InputPrompt:      "Prompt 2",
			OutputResponse:   "Response 2",
			Model:            "gemini-2.0-flash",
			PromptTokens:     15,
			CompletionTokens: 25,
			TotalCost:        &cost2,
			DurationMs:       150,
			Metadata:         "{}",
			Success:          true,
			CreatedAt:        now.Add(-time.Hour),
		},
		{
			UserID:           456, // Different user
			AgentType:        "laplace",
			InputPrompt:      "Prompt 3",
			OutputResponse:   "Response 3",
			Model:            "gemini-2.0-flash",
			PromptTokens:     5,
			CompletionTokens: 10,
			TotalCost:        &cost1,
			DurationMs:       50,
			Metadata:         "{}",
			Success:          true,
			CreatedAt:        now,
		},
		{
			UserID:           userID,
			AgentType:        "reranker", // Different agent type
			InputPrompt:      "Prompt 4",
			OutputResponse:   "Response 4",
			Model:            "gemini-2.0-flash",
			PromptTokens:     20,
			CompletionTokens: 30,
			TotalCost:        &cost2,
			DurationMs:       200,
			Metadata:         "{}",
			Success:          true,
			CreatedAt:        now.Add(-2 * time.Hour),
		},
	}

	for _, log := range logs {
		err := store.AddAgentLog(log)
		assert.NoError(t, err)
	}

	t.Run("get logs for specific user and agent type", func(t *testing.T) {
		result, err := store.GetAgentLogs("laplace", userID, 10)
		assert.NoError(t, err)
		assert.Len(t, result, 2)

		// Should be ordered by created_at DESC
		assert.Equal(t, "Prompt 1", result[0].InputPrompt)
		assert.Equal(t, "Prompt 2", result[1].InputPrompt)
	})

	t.Run("get logs for agent type across all users", func(t *testing.T) {
		result, err := store.GetAgentLogs("laplace", 0, 10)
		assert.NoError(t, err)
		assert.Len(t, result, 3)
	})

	t.Run("get logs with limit", func(t *testing.T) {
		result, err := store.GetAgentLogs("laplace", 0, 2)
		assert.NoError(t, err)
		assert.Len(t, result, 2)
	})

	t.Run("get logs for different agent type", func(t *testing.T) {
		result, err := store.GetAgentLogs("reranker", userID, 10)
		assert.NoError(t, err)
		assert.Len(t, result, 1)
		assert.Equal(t, "Prompt 4", result[0].InputPrompt)
	})
}

func TestGetAgentLogsExtended(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	now := time.Now().UTC()

	cost := 0.001

	logs := []AgentLog{
		{
			UserID:           userID,
			AgentType:        "laplace",
			InputPrompt:      "Search query about AI",
			OutputResponse:   "Found relevant topics",
			Model:            "gemini-2.0-flash",
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalCost:        &cost,
			DurationMs:       100,
			Metadata:         `{"search":"AI"}`,
			Success:          true,
			CreatedAt:        now,
		},
		{
			UserID:           userID,
			AgentType:        "laplace",
			InputPrompt:      "Another query",
			OutputResponse:   "Error occurred",
			Model:            "gemini-2.0-flash",
			PromptTokens:     5,
			CompletionTokens: 0,
			TotalCost:        &cost,
			DurationMs:       50,
			Metadata:         `{"search":"another"}`,
			Success:          false,
			ErrorMessage:     "API timeout",
			CreatedAt:        now.Add(-time.Hour),
		},
		{
			UserID:           456, // Different user
			AgentType:        "reranker",
			InputPrompt:      "Rerank these topics",
			OutputResponse:   "Reranked",
			Model:            "gemini-2.0-flash",
			PromptTokens:     30,
			CompletionTokens: 40,
			TotalCost:        &cost,
			DurationMs:       200,
			Metadata:         `{"reranked":5}`,
			Success:          true,
			CreatedAt:        now,
		},
	}

	for _, log := range logs {
		err := store.AddAgentLog(log)
		assert.NoError(t, err)
	}

	t.Run("filter by agent type", func(t *testing.T) {
		filter := AgentLogFilter{AgentType: "laplace"}
		result, err := store.GetAgentLogsExtended(filter, 10, 0)
		assert.NoError(t, err)
		assert.Equal(t, 2, result.TotalCount)
		assert.Len(t, result.Data, 2)
	})

	t.Run("filter by user ID", func(t *testing.T) {
		filter := AgentLogFilter{UserID: userID}
		result, err := store.GetAgentLogsExtended(filter, 10, 0)
		assert.NoError(t, err)
		assert.Equal(t, 2, result.TotalCount)
		assert.Len(t, result.Data, 2)
	})

	t.Run("filter by success", func(t *testing.T) {
		successTrue := true
		filter := AgentLogFilter{Success: &successTrue}
		result, err := store.GetAgentLogsExtended(filter, 10, 0)
		assert.NoError(t, err)
		assert.Equal(t, 2, result.TotalCount)
		assert.Len(t, result.Data, 2)

		// All results should be successful
		for _, log := range result.Data {
			assert.True(t, log.Success)
		}
	})

	t.Run("filter by failure", func(t *testing.T) {
		successFalse := false
		filter := AgentLogFilter{Success: &successFalse}
		result, err := store.GetAgentLogsExtended(filter, 10, 0)
		assert.NoError(t, err)
		assert.Equal(t, 1, result.TotalCount)
		assert.Len(t, result.Data, 1)
		assert.False(t, result.Data[0].Success)
	})

	t.Run("filter by search term", func(t *testing.T) {
		filter := AgentLogFilter{Search: "AI"}
		result, err := store.GetAgentLogsExtended(filter, 10, 0)
		assert.NoError(t, err)
		assert.Equal(t, 1, result.TotalCount)
		assert.Contains(t, result.Data[0].InputPrompt, "AI")
	})

	t.Run("combined filters", func(t *testing.T) {
		successTrue := true
		filter := AgentLogFilter{
			AgentType: "laplace",
			UserID:    userID,
			Success:   &successTrue,
		}
		result, err := store.GetAgentLogsExtended(filter, 10, 0)
		assert.NoError(t, err)
		assert.Equal(t, 1, result.TotalCount)
		assert.Equal(t, "Search query about AI", result.Data[0].InputPrompt)
	})

	t.Run("pagination", func(t *testing.T) {
		filter := AgentLogFilter{}
		result, err := store.GetAgentLogsExtended(filter, 1, 0)
		assert.NoError(t, err)
		assert.Equal(t, 3, result.TotalCount)
		assert.Len(t, result.Data, 1)

		// Second page
		result2, err := store.GetAgentLogsExtended(filter, 1, 1)
		assert.NoError(t, err)
		assert.Len(t, result2.Data, 1)

		// Different log on second page
		assert.NotEqual(t, result.Data[0].ID, result2.Data[0].ID)
	})
}

func TestGetAgentLogFull(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	ctx := context.Background()
	userID := int64(123)
	cost := 0.001

	log := AgentLog{
		UserID:            userID,
		AgentType:         "laplace",
		InputPrompt:       "Full context prompt",
		InputContext:      `{"model":"gemini-2.0-flash","messages":[{"role":"user","content":"test"}]}`,
		OutputResponse:    "Full response",
		OutputParsed:      `{"result":"parsed"}`,
		OutputContext:     `{"usage":{"prompt_tokens":100,"completion_tokens":200}}`,
		Model:             "gemini-2.0-flash",
		PromptTokens:      100,
		CompletionTokens:  200,
		TotalCost:         &cost,
		DurationMs:        500,
		Metadata:          `{"context":"full"}`,
		Success:           true,
		ErrorMessage:      "",
		ConversationTurns: `[{"role":"user","content":"test"},{"role":"assistant","content":"response"}]`,
	}

	err := store.AddAgentLog(log)
	assert.NoError(t, err)

	// Get the log ID by querying
	logs, err := store.GetAgentLogs("laplace", userID, 1)
	assert.NoError(t, err)
	assert.Len(t, logs, 1)
	logID := logs[0].ID

	t.Run("get full log by ID and user", func(t *testing.T) {
		fullLog, err := store.GetAgentLogFull(ctx, logID, userID)
		assert.NoError(t, err)
		assert.NotNil(t, fullLog)

		assert.Equal(t, logID, fullLog.ID)
		assert.Equal(t, userID, fullLog.UserID)
		assert.Equal(t, "laplace", fullLog.AgentType)
		assert.Equal(t, "Full context prompt", fullLog.InputPrompt)
		assert.Equal(t, `{"model":"gemini-2.0-flash","messages":[{"role":"user","content":"test"}]}`, fullLog.InputContext)
		assert.Equal(t, "Full response", fullLog.OutputResponse)
		assert.Equal(t, `{"result":"parsed"}`, fullLog.OutputParsed)
		assert.Equal(t, 100, fullLog.PromptTokens)
		assert.Equal(t, 200, fullLog.CompletionTokens)
		assert.Equal(t, `[{"role":"user","content":"test"},{"role":"assistant","content":"response"}]`, fullLog.ConversationTurns)
		assert.True(t, fullLog.Success)
	})

	t.Run("not found for different user", func(t *testing.T) {
		_, err := store.GetAgentLogFull(ctx, logID, 999)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("not found for non-existent ID", func(t *testing.T) {
		_, err := store.GetAgentLogFull(ctx, 99999, userID)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

func TestAgentLogConversationTurns(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	cost := 0.001

	log := AgentLog{
		UserID:            userID,
		AgentType:         "laplace",
		InputPrompt:       "Multi-turn prompt",
		OutputResponse:    "Multi-turn response",
		Model:             "gemini-2.0-flash",
		PromptTokens:      50,
		CompletionTokens:  100,
		TotalCost:         &cost,
		DurationMs:        300,
		Metadata:          "{}",
		Success:           true,
		ConversationTurns: `[{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi"},{"role":"user","content":"How are you?"},{"role":"assistant","content":"I'm good"}]`,
	}

	err := store.AddAgentLog(log)
	assert.NoError(t, err)

	logs, err := store.GetAgentLogs("laplace", userID, 1)
	assert.NoError(t, err)
	assert.Len(t, logs, 1)
	assert.NotEmpty(t, logs[0].ConversationTurns)
	assert.Contains(t, logs[0].ConversationTurns, "Hello")
}

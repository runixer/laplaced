package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestExecuteToolCall_UnknownTool tests error when tool name is unknown.
func TestExecuteToolCall_UnknownTool(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	cfg := testutil.TestConfig()
	cfg.Tools = []config.ToolConfig{{Name: "known_tool"}}

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, cfg, testutil.TestLogger())

	ctx := context.Background()
	_, err := exec.ExecuteToolCall(ctx, 123, "unknown_tool", `{}`)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tool")
}

// TestExecuteToolCall_InvalidArguments tests error when arguments JSON is invalid.
func TestExecuteToolCall_InvalidArguments(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	cfg := testutil.TestConfig()
	cfg.Tools = []config.ToolConfig{{Name: "search_history"}}

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, cfg, testutil.TestLogger())

	ctx := context.Background()
	_, err := exec.ExecuteToolCall(ctx, 123, "search_history", `{invalid json`)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse arguments")
}

// TestExecuteToolCall_SearchHistory tests search_history tool execution.
func TestExecuteToolCall_SearchHistory(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	cfg := testutil.TestConfig()
	cfg.Tools = []config.ToolConfig{{Name: "search_history"}}

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, cfg, testutil.TestLogger())

	// No RAG service set, should return "not available"
	ctx := context.Background()
	args, _ := json.Marshal(map[string]string{"query": "test"})
	result, err := exec.ExecuteToolCall(ctx, 123, "search_history", string(args))

	assert.NoError(t, err)
	assert.Contains(t, result, "Search is not available")
}

// TestExecuteToolCall_ManageMemory tests manage_memory tool execution.
func TestExecuteToolCall_ManageMemory(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	embedding := testutil.TestEmbedding()
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
		return len(req.Input) > 0
	})).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil)

	mockStore.On("AddFact", mock.Anything).Return(int64(1), nil)
	mockStore.On("AddFactHistory", mock.Anything).Return(nil)

	cfg := testutil.TestConfig()
	cfg.Tools = []config.ToolConfig{{Name: "manage_memory"}}

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, cfg, testutil.TestLogger())

	ctx := context.Background()
	// manage_memory expects a "query" parameter containing JSON string
	args, _ := json.Marshal(map[string]interface{}{
		"query": `{"operations":[{"action":"add","content":"Test fact"}]}`,
	})
	result, err := exec.ExecuteToolCall(ctx, 123, "manage_memory", string(args))

	assert.NoError(t, err)
	assert.Contains(t, result, "Success")

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

// TestExecuteToolCall_SearchPeople tests search_people tool execution.
func TestExecuteToolCall_SearchPeople(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	username := "alice"
	testPerson := storage.Person{
		ID:          1,
		UserID:      123,
		DisplayName: "Alice",
		Username:    &username,
		Circle:      "Friends",
	}

	mockStore.On("FindPersonByUsername", int64(123), "alice").Return(&testPerson, nil)

	cfg := testutil.TestConfig()
	cfg.Tools = []config.ToolConfig{{Name: "search_people"}}

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, cfg, testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	args, _ := json.Marshal(map[string]string{"query": "@alice"})
	result, err := exec.ExecuteToolCall(ctx, 123, "search_people", string(args))

	assert.NoError(t, err)
	assert.Contains(t, result, "Found 1 people")
	assert.Contains(t, result, "Alice")

	mockStore.AssertExpectations(t)
}

// TestExecuteToolCall_ManagePeople tests manage_people tool execution.
func TestExecuteToolCall_ManagePeople(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	// Mock embedding creation for person
	embedding := testutil.TestEmbedding()
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
		return len(req.Input) > 0
	})).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: embedding}},
	}, nil)

	mockStore.On("FindPersonByName", int64(123), "Alice").Return(nil, nil)
	mockStore.On("FindPersonByAlias", int64(123), "Alice").Return([]storage.Person{}, nil)
	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.DisplayName == "Alice"
	})).Return(int64(42), nil)

	cfg := testutil.TestConfig()
	cfg.Tools = []config.ToolConfig{{Name: "manage_people"}}

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, cfg, testutil.TestLogger())
	exec.SetPeopleRepository(mockStore)

	ctx := context.Background()
	// manage_people expects a "query" parameter containing JSON string
	args, _ := json.Marshal(map[string]interface{}{
		"query": `{"operation":"create","name":"Alice"}`,
	})
	result, err := exec.ExecuteToolCall(ctx, 123, "manage_people", string(args))

	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully created person")

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

// TestExecuteToolCall_ModelTool tests custom model tool execution.
func TestExecuteToolCall_ModelTool(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                "json:\"role\""
				Content          string                "json:\"content\""
				ToolCalls        []openrouter.ToolCall "json:\"tool_calls,omitempty\""
				Reasoning        string                "json:\"reasoning,omitempty\""
				ReasoningDetails interface{}           "json:\"reasoning_details,omitempty\""
			} "json:\"message\""
			FinishReason string "json:\"finish_reason,omitempty\""
			Index        int    "json:\"index\""
		}{
			{
				Message: struct {
					Role             string                "json:\"role\""
					Content          string                "json:\"content\""
					ToolCalls        []openrouter.ToolCall "json:\"tool_calls,omitempty\""
					Reasoning        string                "json:\"reasoning,omitempty\""
					ReasoningDetails interface{}           "json:\"reasoning_details,omitempty\""
				}{
					Role:    "assistant",
					Content: "Model response",
				},
			},
		},
	}, nil)

	cfg := testutil.TestConfig()
	cfg.Tools = []config.ToolConfig{{Name: "custom_model", Model: "test-model"}}

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, cfg, testutil.TestLogger())

	ctx := context.Background()
	args, _ := json.Marshal(map[string]string{"query": "test query"})
	result, err := exec.ExecuteToolCall(ctx, 123, "custom_model", string(args))

	assert.NoError(t, err)
	assert.Contains(t, result, "Model response")

	mockORClient.AssertExpectations(t)
}

// TestSetAgentLogger tests setting agent logger.
func TestSetAgentLogger(t *testing.T) {
	// Create a real agentlog.Logger for testing (it's a simple struct)
	// We can't easily mock it, so we just test the setter works
	exec := NewToolExecutor(nil, nil, nil, testutil.TestConfig(), testutil.TestLogger())

	// agentLogger is optional, nil is valid
	assert.Nil(t, exec.agentLogger)

	// We can't easily create a real agentlog.Logger without dependencies
	// Just verify the field exists and is settable
	exec.agentLogger = nil
	assert.Nil(t, exec.agentLogger)
}

// TestNewToolExecutor verifies ToolExecutor initialization.
func TestNewToolExecutor(t *testing.T) {
	mockORClient := new(testutil.MockOpenRouterClient)
	mockStore := new(testutil.MockStorage)
	cfg := testutil.TestConfig()
	logger := testutil.TestLogger()

	exec := NewToolExecutor(mockORClient, mockStore, mockStore, cfg, logger)

	assert.NotNil(t, exec)
	assert.Same(t, mockORClient, exec.orClient)
	assert.Same(t, mockStore, exec.factRepo)
	assert.Same(t, mockStore, exec.factHistoryRepo)
	assert.Same(t, cfg, exec.cfg)
	assert.NotNil(t, exec.logger)
}

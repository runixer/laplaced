package laplace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestExecuteToolCalls tests the executeToolCalls method.
func TestExecuteToolCalls(t *testing.T) {
	tests := []struct {
		name      string
		toolCalls []openrouter.ToolCall
		mockSetup func(*testutil.MockToolHandler)
		verify    func(*testing.T, []openrouter.Message)
	}{
		{
			name: "single successful tool call",
			toolCalls: []openrouter.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "search_web",
						Arguments: `{"query": "test"}`,
					},
				},
			},
			mockSetup: func(h *testutil.MockToolHandler) {
				h.On("ExecuteToolCall", "search_web", `{"query": "test"}`).
					Return("Search results: found 5 items", nil)
			},
			verify: func(t *testing.T, msgs []openrouter.Message) {
				require.Len(t, msgs, 1)
				assert.Equal(t, "tool", msgs[0].Role)
				assert.Equal(t, "call_1", msgs[0].ToolCallID)
				assert.Equal(t, "Search results: found 5 items", msgs[0].Content)
			},
		},
		{
			name: "tool call with error",
			toolCalls: []openrouter.ToolCall{
				{
					ID:   "call_2",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "search_history",
						Arguments: `{"query": "test"}`,
					},
				},
			},
			mockSetup: func(h *testutil.MockToolHandler) {
				h.On("ExecuteToolCall", "search_history", mock.Anything).
					Return("", errors.New("database error"))
			},
			verify: func(t *testing.T, msgs []openrouter.Message) {
				require.Len(t, msgs, 1)
				assert.Equal(t, "tool", msgs[0].Role)
				assert.Contains(t, msgs[0].Content, "Tool execution failed")
				assert.Contains(t, msgs[0].Content, "database error")
			},
		},
		{
			name: "multiple tool calls",
			toolCalls: []openrouter.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "search_web",
						Arguments: `{"query": "golang"}`,
					},
				},
				{
					ID:   "call_2",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "search_history",
						Arguments: `{"query": "memory"}`,
					},
				},
			},
			mockSetup: func(h *testutil.MockToolHandler) {
				h.On("ExecuteToolCall", "search_web", `{"query": "golang"}`).
					Return("Golang results", nil)
				h.On("ExecuteToolCall", "search_history", `{"query": "memory"}`).
					Return("History results", nil)
			},
			verify: func(t *testing.T, msgs []openrouter.Message) {
				require.Len(t, msgs, 2)
				assert.Equal(t, "call_1", msgs[0].ToolCallID)
				assert.Equal(t, "Golang results", msgs[0].Content)
				assert.Equal(t, "call_2", msgs[1].ToolCallID)
				assert.Equal(t, "History results", msgs[1].Content)
			},
		},
		{
			name:      "empty tool calls list",
			toolCalls: []openrouter.ToolCall{},
			mockSetup: func(h *testutil.MockToolHandler) {},
			verify: func(t *testing.T, msgs []openrouter.Message) {
				assert.Len(t, msgs, 0)
			},
		},
		{
			name: "mixed success and failure",
			toolCalls: []openrouter.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "search_web",
						Arguments: `{"query": "good"}`,
					},
				},
				{
					ID:   "call_2",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "search_history",
						Arguments: `{"query": "bad"}`,
					},
				},
			},
			mockSetup: func(h *testutil.MockToolHandler) {
				h.On("ExecuteToolCall", "search_web", `{"query": "good"}`).
					Return("Success", nil)
				h.On("ExecuteToolCall", "search_history", `{"query": "bad"}`).
					Return("", errors.New("failed"))
			},
			verify: func(t *testing.T, msgs []openrouter.Message) {
				require.Len(t, msgs, 2)
				assert.Equal(t, "Success", msgs[0].Content)
				assert.Contains(t, msgs[1].Content, "Tool execution failed")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := new(testutil.MockToolHandler)
			tt.mockSetup(handler)

			agent := &Laplace{
				logger: testutil.TestLogger(),
			}

			messages := agent.executeToolCalls(
				context.Background(),
				handler,
				tt.toolCalls,
				nil,
				agent.logger,
			)

			tt.verify(t, messages)
			handler.AssertExpectations(t)
		})
	}
}

// TestExecute_HappyPath tests successful execution with a simple text response.
func TestExecute_HappyPath(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := int64(123)
	req := &Request{
		UserID:              userID,
		RawQuery:            "Hello",
		HistoryContent:      "Hello",
		CurrentMessageParts: []interface{}{openrouter.TextPart{Type: "text", Text: "Hello"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeChatResponse("Hello! How can I help you today?",
			WithID("resp-1"),
			WithModel("gemini-2.0-flash-exp"),
			WithTokens(10, 9, 19),
		), nil,
	)

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "Hello! How can I help you today?", resp.Content)
	assert.Equal(t, 10, resp.PromptTokens)
	assert.Equal(t, 9, resp.CompletionTokens)
	assert.Equal(t, 1, resp.TotalTurns)
	assert.Nil(t, resp.Error)

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)

	// Clean up
	_ = cfg
	_ = translator
}

// TestExecute_WithToolCall tests execution with a tool call.
func TestExecute_WithToolCall(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := int64(123)
	req := &Request{
		UserID:              userID,
		RawQuery:            "Search for golang",
		HistoryContent:      "Search for golang",
		CurrentMessageParts: []interface{}{openrouter.TextPart{Type: "text", Text: "Search for golang"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// First call: returns tool call
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeToolCallResponse("search_web", `{"query": "golang"}`,
			WithID("resp-1"),
			WithModel("gemini-2.0-flash-exp"),
			WithTokens(15, 20, 35),
		), nil,
	).Once()

	handler.On("ExecuteToolCall", "search_web", `{"query": "golang"}`).
		Return("Golang is a programming language", nil)

	// Second call: returns final response
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeChatResponse("Based on the search, Golang is a programming language created by Google.",
			WithID("resp-2"),
			WithModel("gemini-2.0-flash-exp"),
			WithTokens(50, 15, 65),
		), nil,
	).Once()

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "Based on the search, Golang is a programming language created by Google.", resp.Content)
	// Tokens accumulate across both LLM calls: (15+20) + (50+15) = 100
	assert.Equal(t, 100, resp.PromptTokens+resp.CompletionTokens)
	assert.Equal(t, 2, resp.TotalTurns)
	assert.Nil(t, resp.Error)

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestExecute_EmptyResponseWithRetry tests the empty response retry mechanism.
func TestExecute_EmptyResponseWithRetry(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := int64(123)
	req := &Request{
		UserID:              userID,
		RawQuery:            "test",
		HistoryContent:      "test",
		CurrentMessageParts: []interface{}{openrouter.TextPart{Type: "text", Text: "test"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// First call empty, second call success - use Once() for each
	emptyResp := makeEmptyResponse(WithTokens(10, 0, 10))
	successResp := makeChatResponse("Now I have a response!", WithTokens(12, 6, 18))

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(emptyResp, nil).Once()
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(successResp, nil).Once()

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "Now I have a response!", resp.Content)
	// TotalTurns includes both attempts (empty + retry)
	assert.Equal(t, 2, resp.TotalTurns)

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestExecute_MaxEmptyRetries tests hitting the max empty retries limit.
func TestExecute_MaxEmptyRetries(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := int64(123)
	req := &Request{
		UserID:              userID,
		RawQuery:            "test",
		HistoryContent:      "test",
		CurrentMessageParts: []interface{}{openrouter.TextPart{Type: "text", Text: "test"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// Always return empty response
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeEmptyResponse(WithTokens(10, 0, 10)), nil,
	)

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Empty(t, resp.Content)
	assert.Error(t, resp.Error)
	assert.Contains(t, resp.Error.Error(), "max empty response retries")

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestExecute_LLMError tests handling of LLM errors.
func TestExecute_LLMError(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := int64(123)
	req := &Request{
		UserID:              userID,
		RawQuery:            "test",
		HistoryContent:      "test",
		CurrentMessageParts: []interface{}{openrouter.TextPart{Type: "text", Text: "test"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openrouter.ChatCompletionResponse{}, errors.New("API error"))

	resp, err := agent.Execute(context.Background(), req, handler)
	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "LLM call failed")

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestExecute_ResponseSanitization tests hallucination tag sanitization.
func TestExecute_ResponseSanitization(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := int64(123)
	req := &Request{
		UserID:              userID,
		RawQuery:            "test",
		HistoryContent:      "test",
		CurrentMessageParts: []interface{}{openrouter.TextPart{Type: "text", Text: "test"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// Response with hallucination tags
	hallucinatedContent := "This is the response.</tool_code>garbage text"
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeChatResponse(hallucinatedContent, WithTokens(10, 10, 20)), nil,
	)

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "This is the response.", resp.Content)
	assert.Equal(t, 1, resp.TotalTurns)

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestExecute_WithPDFParserPlugin tests PDF parser plugin configuration.
func TestExecute_WithPDFParserPlugin(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.OpenRouter.PDFParserEngine = "legacy"
	cfg.RAG.Enabled = false // Disable RAG since we pass nil for ragService

	translator, err := i18n.NewTranslator("en")
	require.NoError(t, err)

	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	// Pass nil for ragService and artifactRepo
	agent := New(cfg, mockORClient, nil, mockStore, mockStore, nil, translator, testutil.TestLogger())

	userID := int64(123)
	req := &Request{
		UserID:              userID,
		RawQuery:            "test",
		HistoryContent:      "test",
		CurrentMessageParts: []interface{}{openrouter.TextPart{Type: "text", Text: "test"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// Verify plugin is in request
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(r openrouter.ChatCompletionRequest) bool {
		return len(r.Plugins) == 1 && r.Plugins[0].ID == "file-parser" && r.Plugins[0].PDF.Engine == "legacy"
	})).Return(
		makeChatResponse("Response", WithTokens(5, 3, 8)), nil,
	)

	handler := new(testutil.MockToolHandler)

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "Response", resp.Content)

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)
}

// TestExecute_IntermediateMessageWithToolCall tests intermediate message callback during tool execution.
func TestExecute_IntermediateMessageWithToolCall(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := int64(123)

	var intermediateMessages []string
	req := &Request{
		UserID:              userID,
		RawQuery:            "search",
		HistoryContent:      "search",
		CurrentMessageParts: []interface{}{openrouter.TextPart{Type: "text", Text: "search"}},
		OnIntermediateMessage: func(text string) {
			intermediateMessages = append(intermediateMessages, text)
		},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// First response has both content and tool call
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeToolCallWithContentResponse("Let me search that for you.", "search_web", `{"query":"test"}`, "call_1",
			WithTokens(10, 15, 25),
		), nil,
	).Once()

	handler.On("ExecuteToolCall", "search_web", mock.Anything).Return("Results", nil)

	// Second response: final
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeChatResponse("Done searching.", WithTokens(30, 5, 35)), nil,
	).Once()

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.Equal(t, "Done searching.", resp.Content)
	assert.Len(t, intermediateMessages, 1)
	assert.Equal(t, "Let me search that for you.", intermediateMessages[0])

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestExecute_MaxIterations tests the max tool iterations limit.
func TestExecute_MaxIterations(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := int64(123)
	req := &Request{
		UserID:              userID,
		RawQuery:            "loop",
		HistoryContent:      "loop",
		CurrentMessageParts: []interface{}{openrouter.TextPart{Type: "text", Text: "loop"}},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// Always return a tool call (infinite loop scenario)
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeToolCallWithContentResponse("Searching again...", "search_web", `{"query":"more"}`, "call_1",
			WithTokens(10, 0, 10),
		), nil,
	)

	handler.On("ExecuteToolCall", "search_web", mock.Anything).Return("results", nil)

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	// Should stop after max iterations (10)
	assert.LessOrEqual(t, resp.TotalTurns, 11) // maxToolIterations + 1 for final attempt

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// TestLogExecution tests the LogExecution method.
func TestLogExecution(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false // Disable RAG since we pass nil for ragService
	translator, err := i18n.NewTranslator("en")
	require.NoError(t, err)

	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	// Pass nil for ragService and artifactRepo
	agent := New(cfg, mockORClient, nil, mockStore, mockStore, nil, translator, testutil.TestLogger())

	// Create a real agent logger with nil repo (will be a no-op)
	agentLogger := agentlog.NewLogger(nil, testutil.TestLogger(), true)
	agent.agentLogger = agentLogger

	userID := int64(123)
	cost := 0.001

	resp := &Response{
		Content:          "Test response",
		PromptTokens:     10,
		CompletionTokens: 5,
		LLMDuration:      100 * time.Millisecond,
		ToolDuration:     50 * time.Millisecond,
		TotalTurns:       1,
		RAGInfo: &rag.RetrievalDebugInfo{
			OriginalQuery: "test query",
			EnrichedQuery: "enriched query",
		},
		Messages: []openrouter.Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi"},
		},
	}

	// LogExecution should not panic even with nil repo
	agent.LogExecution(context.Background(), userID, resp, cost)

	_ = cfg
	_ = translator
}

// TestExecute_WithToolTypingAction tests the typing action callback during tool execution.
func TestExecute_WithToolTypingAction(t *testing.T) {
	cfg, translator, agent, mockStore, mockORClient, handler := setupExecuteTest(t)

	userID := int64(123)
	typingCallCount := 0
	req := &Request{
		UserID:              userID,
		RawQuery:            "search",
		HistoryContent:      "search",
		CurrentMessageParts: []interface{}{openrouter.TextPart{Type: "text", Text: "search"}},
		OnTypingAction: func() {
			typingCallCount++
		},
	}

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeToolCallResponse("search_web", `{}`),
		nil,
	).Once()

	handler.On("ExecuteToolCall", "search_web", mock.Anything).Return("done", nil)

	// Second call: no more tools
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		makeChatResponse("Final", WithTokens(20, 5, 25)),
		nil,
	).Once()

	resp, err := agent.Execute(context.Background(), req, handler)
	require.NoError(t, err)
	assert.Equal(t, 1, typingCallCount, "typing action should be called once per tool execution")
	assert.Equal(t, "Final", resp.Content)

	mockStore.AssertExpectations(t)
	handler.AssertExpectations(t)

	_ = cfg
	_ = translator
}

// setupExecuteTest is a helper for Execute tests.
func setupExecuteTest(t *testing.T) (*config.Config, *i18n.Translator, *Laplace, *testutil.MockStorage, *testutil.MockOpenRouterClient, *testutil.MockToolHandler) {
	t.Helper()

	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false // Disable RAG since we pass nil for ragService
	translator, err := i18n.NewTranslator("en")
	require.NoError(t, err)

	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	// Pass nil for ragService and artifactRepo (concrete structs, not interfaces)
	agent := New(cfg, mockORClient, nil, mockStore, mockStore, nil, translator, testutil.TestLogger())

	handler := new(testutil.MockToolHandler)

	return cfg, translator, agent, mockStore, mockORClient, handler
}

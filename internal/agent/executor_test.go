package agent

import (
	"context"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestExecutor_ExecuteSingleShot(t *testing.T) {
	tests := []struct {
		name        string
		req         SingleShotRequest
		mockResp    llm.ChatCompletionResponse
		mockErr     error
		wantContent string
		wantErr     bool
	}{
		{
			name: "successful execution",
			req: SingleShotRequest{
				AgentType:    TypeEnricher,
				UserID:       "123",
				Model:        "test-model",
				SystemPrompt: "You are a helpful assistant",
				UserPrompt:   "Hello",
			},
			mockResp:    testutil.MockChatResponse("Hello! How can I help you?"),
			wantContent: "Hello! How can I help you?",
			wantErr:     false,
		},
		{
			name: "handles LLM error",
			req: SingleShotRequest{
				AgentType:    TypeEnricher,
				UserID:       "123",
				Model:        "test-model",
				SystemPrompt: "test",
				UserPrompt:   "test",
			},
			mockResp: llm.ChatCompletionResponse{},
			mockErr:  assert.AnError,
			wantErr:  true,
		},
		{
			name: "handles empty response",
			req: SingleShotRequest{
				AgentType:    TypeEnricher,
				UserID:       "123",
				Model:        "test-model",
				SystemPrompt: "test",
				UserPrompt:   "test",
			},
			mockResp: llm.ChatCompletionResponse{}, // empty response for error case
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &testutil.MockLLMClient{}
			if tt.mockErr != nil {
				mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
					Return(tt.mockResp, tt.mockErr)
			} else {
				mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
					Return(tt.mockResp, nil)
			}

			executor := NewExecutor(mockClient, nil, testutil.TestLogger())

			resp, err := executor.ExecuteSingleShot(context.Background(), tt.req)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wantContent, resp.Content)
			assert.NotZero(t, resp.Duration)
			mockClient.AssertExpectations(t)
		})
	}
}

func TestExecutor_ExecuteSingleShot_JSONMode(t *testing.T) {
	mockClient := &testutil.MockLLMClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req llm.ChatCompletionRequest) bool {
		return req.ResponseFormat != nil
	})).Return(testutil.MockChatResponse(`{"result": "success"}`), nil)

	executor := NewExecutor(mockClient, nil, testutil.TestLogger())

	resp, err := executor.ExecuteSingleShot(context.Background(), SingleShotRequest{
		AgentType:    TypeArchivist,
		UserID:       "123",
		Model:        "test-model",
		SystemPrompt: "You are a JSON generator",
		UserPrompt:   "Generate JSON",
		JSONMode:     true,
	})

	assert.NoError(t, err)
	assert.Contains(t, resp.Content, "success")
	mockClient.AssertExpectations(t)
}

func TestExecutor_ExecuteSingleShot_WithMessages(t *testing.T) {
	mockClient := &testutil.MockLLMClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req llm.ChatCompletionRequest) bool {
		return len(req.Messages) == 3 // system + user + assistant
	})).Return(testutil.MockChatResponse("Response based on context"), nil)

	executor := NewExecutor(mockClient, nil, testutil.TestLogger())

	resp, err := executor.ExecuteSingleShot(context.Background(), SingleShotRequest{
		AgentType: TypeLaplace,
		UserID:    "123",
		Model:     "test-model",
		Messages: []llm.Message{
			{Role: "system", Content: "You are helpful"},
			{Role: "user", Content: "Hi"},
			{Role: "assistant", Content: "Hello!"},
		},
	})

	assert.NoError(t, err)
	assert.Equal(t, "Response based on context", resp.Content)
	mockClient.AssertExpectations(t)
}

func TestExecutor_Client(t *testing.T) {
	mockClient := &testutil.MockLLMClient{}
	executor := NewExecutor(mockClient, nil, testutil.TestLogger())

	assert.Equal(t, mockClient, executor.Client())
}

func TestExecutor_AgentLogger(t *testing.T) {
	mockClient := &testutil.MockLLMClient{}
	executor := NewExecutor(mockClient, nil, testutil.TestLogger())

	assert.Nil(t, executor.AgentLogger())
}

func TestExecutor_ExecuteAgentic(t *testing.T) {
	tests := []struct {
		name            string
		req             SingleShotRequest
		opts            AgenticOptions
		setupMocks      func(*testutil.MockLLMClient)
		wantContent     string
		wantErr         bool
		wantErrContains string
	}{
		{
			name: "single turn without tool calls",
			req: SingleShotRequest{
				AgentType:    TypeReranker,
				UserID:       "123",
				Model:        "test-model",
				SystemPrompt: "You are a helpful assistant",
				UserPrompt:   "Hello",
			},
			opts: AgenticOptions{
				MaxTurns: 3,
			},
			setupMocks: func(mc *testutil.MockLLMClient) {
				mc.On("CreateChatCompletion", mock.Anything, mock.Anything).
					Return(testutil.MockChatResponseWithTokens("Hello! How can I help?", 10, 5), nil)
			},
			wantContent: "Hello! How can I help?",
			wantErr:     false,
		},
		{
			name: "multi turn with tool call then finish",
			req: SingleShotRequest{
				AgentType:    TypeReranker,
				UserID:       "123",
				Model:        "test-model",
				SystemPrompt: "test",
				UserPrompt:   "search",
			},
			opts: AgenticOptions{
				MaxTurns: 5,
				Tools: []llm.Tool{
					{
						Type: "function",
						Function: llm.ToolFunction{
							Name:        "search",
							Description: "Search",
							Parameters:  map[string]any{},
						},
					},
				},
				ToolHandler: func(ctx context.Context, calls []llm.ToolCall) []llm.Message {
					return []llm.Message{
						{Role: "tool", Content: "search results", ToolCallID: calls[0].ID},
					}
				},
			},
			setupMocks: func(mc *testutil.MockLLMClient) {
				// First turn returns tool call
				mc.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req llm.ChatCompletionRequest) bool {
					return len(req.Tools) > 0
				})).Return(testutil.MockChatResponseWithToolCalls("I'll search for that",
					[]llm.ToolCall{testutil.MockToolCall("call_1", "search", "{}")}), nil).Once()

				// Second turn returns final answer
				mc.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req llm.ChatCompletionRequest) bool {
					return len(req.Messages) > 2 // system, user, assistant(tool), tool result
				})).Return(testutil.MockChatResponseWithTokens("Based on search results: answer", 30, 15), nil).Once()
			},
			wantContent: "Based on search results: answer",
			wantErr:     false,
		},
		{
			name: "max turns exceeded",
			req: SingleShotRequest{
				AgentType:    TypeReranker,
				UserID:       "123",
				Model:        "test-model",
				SystemPrompt: "test",
				UserPrompt:   "keep calling tools",
			},
			opts: AgenticOptions{
				MaxTurns: 2,
				Tools: []llm.Tool{
					{
						Type: "function",
						Function: llm.ToolFunction{
							Name:        "tool",
							Description: "A tool",
							Parameters:  map[string]any{},
						},
					},
				},
				ToolHandler: func(ctx context.Context, calls []llm.ToolCall) []llm.Message {
					return []llm.Message{
						{Role: "tool", Content: "result", ToolCallID: calls[0].ID},
					}
				},
			},
			setupMocks: func(mc *testutil.MockLLMClient) {
				// Always return tool call
				mc.On("CreateChatCompletion", mock.Anything, mock.Anything).
					Return(testutil.MockChatResponseWithToolCalls("",
						[]llm.ToolCall{testutil.MockToolCall("call_1", "tool", "{}")}), nil)
			},
			wantErr:         true,
			wantErrContains: "max turns",
		},
		{
			name: "LLM error on first turn",
			req: SingleShotRequest{
				AgentType:    TypeReranker,
				UserID:       "123",
				Model:        "test-model",
				SystemPrompt: "test",
				UserPrompt:   "error",
			},
			opts: AgenticOptions{
				MaxTurns: 3,
			},
			setupMocks: func(mc *testutil.MockLLMClient) {
				mc.On("CreateChatCompletion", mock.Anything, mock.Anything).
					Return(llm.ChatCompletionResponse{}, assert.AnError)
			},
			wantErr:         true,
			wantErrContains: "turn 0 failed",
		},
		{
			name: "empty response from LLM",
			req: SingleShotRequest{
				AgentType:    TypeReranker,
				UserID:       "123",
				Model:        "test-model",
				SystemPrompt: "test",
				UserPrompt:   "empty",
			},
			opts: AgenticOptions{
				MaxTurns: 3,
			},
			setupMocks: func(mc *testutil.MockLLMClient) {
				// Empty Choices slice
				var resp llm.ChatCompletionResponse
				mc.On("CreateChatCompletion", mock.Anything, mock.Anything).
					Return(resp, nil)
			},
			wantErr:         true,
			wantErrContains: "empty response",
		},
		{
			name: "with timeout",
			req: SingleShotRequest{
				AgentType:    TypeReranker,
				UserID:       "123",
				Model:        "test-model",
				SystemPrompt: "test",
				UserPrompt:   "quick",
			},
			opts: AgenticOptions{
				MaxTurns: 3,
				Timeout:  10 * time.Second,
			},
			setupMocks: func(mc *testutil.MockLLMClient) {
				mc.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req llm.ChatCompletionRequest) bool {
					return true
				})).Return(testutil.MockChatResponseWithTokens("quick response", 5, 3), nil)
			},
			wantContent: "quick response",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &testutil.MockLLMClient{}
			tt.setupMocks(mockClient)

			executor := NewExecutor(mockClient, nil, testutil.TestLogger())

			resp, err := executor.ExecuteAgentic(context.Background(), tt.req, tt.opts)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.wantErrContains != "" {
					assert.Contains(t, err.Error(), tt.wantErrContains)
				}
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wantContent, resp.Content)
			assert.NotZero(t, resp.Duration)

			mockClient.AssertExpectations(t)
		})
	}
}

func TestExecutor_ExecuteAgentic_ToolChoice(t *testing.T) {
	mockClient := &testutil.MockLLMClient{}

	var capturedToolChoice any
	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req llm.ChatCompletionRequest) bool {
		capturedToolChoice = req.ToolChoice
		return true
	})).Return(testutil.MockChatResponseWithToolCalls("response",
		[]llm.ToolCall{testutil.MockToolCall("call_1", "tool", "{}")}), nil).Once()

	// Second call returns final answer without tool calls
	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req llm.ChatCompletionRequest) bool {
		return len(req.Messages) > 2
	})).Return(testutil.MockChatResponseWithTokens("final answer", 20, 10), nil).Once()

	executor := NewExecutor(mockClient, nil, testutil.TestLogger())

	resp, err := executor.ExecuteAgentic(context.Background(), SingleShotRequest{
		AgentType:    TypeReranker,
		UserID:       "123",
		Model:        "test-model",
		SystemPrompt: "test",
		UserPrompt:   "test",
	}, AgenticOptions{
		MaxTurns:   3,
		ToolChoice: "required",
		Tools: []llm.Tool{
			{
				Type: "function",
				Function: llm.ToolFunction{
					Name:        "tool",
					Description: "Tool",
					Parameters:  map[string]any{},
				},
			},
		},
		ToolHandler: func(ctx context.Context, calls []llm.ToolCall) []llm.Message {
			return []llm.Message{
				{Role: "tool", Content: "result", ToolCallID: calls[0].ID},
			}
		},
	})

	assert.NoError(t, err)
	assert.Equal(t, "final answer", resp.Content)
	assert.Equal(t, "required", capturedToolChoice)
	mockClient.AssertExpectations(t)
}

func TestExecutor_ExecuteAgentic_NoToolHandler(t *testing.T) {
	mockClient := &testutil.MockLLMClient{}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponseWithToolCalls("I need tools",
			[]llm.ToolCall{testutil.MockToolCall("call_1", "search", "{}")}), nil).Once()

	// Second call should still work even without tool handler
	// (messages are appended but no tool results)
	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req llm.ChatCompletionRequest) bool {
		return len(req.Messages) > 2
	})).Return(testutil.MockChatResponseWithTokens("final response", 15, 8), nil).Once()

	executor := NewExecutor(mockClient, nil, testutil.TestLogger())

	resp, err := executor.ExecuteAgentic(context.Background(), SingleShotRequest{
		AgentType:    TypeReranker,
		UserID:       "123",
		Model:        "test-model",
		SystemPrompt: "test",
		UserPrompt:   "test",
	}, AgenticOptions{
		MaxTurns: 3,
		Tools: []llm.Tool{
			{
				Type: "function",
				Function: llm.ToolFunction{
					Name:        "search",
					Description: "Search",
					Parameters:  map[string]any{},
				},
			},
		},
		// No ToolHandler - tool calls are appended but no results
	})

	assert.NoError(t, err)
	assert.Equal(t, "final response", resp.Content)
	mockClient.AssertExpectations(t)
}

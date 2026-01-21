package agent

import (
	"context"
	"testing"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestExecutor_ExecuteSingleShot(t *testing.T) {
	tests := []struct {
		name        string
		req         SingleShotRequest
		mockResp    openrouter.ChatCompletionResponse
		mockErr     error
		wantContent string
		wantErr     bool
	}{
		{
			name: "successful execution",
			req: SingleShotRequest{
				AgentType:    TypeEnricher,
				UserID:       123,
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
				UserID:       123,
				Model:        "test-model",
				SystemPrompt: "test",
				UserPrompt:   "test",
			},
			mockResp: openrouter.ChatCompletionResponse{},
			mockErr:  assert.AnError,
			wantErr:  true,
		},
		{
			name: "handles empty response",
			req: SingleShotRequest{
				AgentType:    TypeEnricher,
				UserID:       123,
				Model:        "test-model",
				SystemPrompt: "test",
				UserPrompt:   "test",
			},
			mockResp: openrouter.ChatCompletionResponse{}, // empty response for error case
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &testutil.MockOpenRouterClient{}
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
	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openrouter.ChatCompletionRequest) bool {
		return req.ResponseFormat != nil
	})).Return(testutil.MockChatResponse(`{"result": "success"}`), nil)

	executor := NewExecutor(mockClient, nil, testutil.TestLogger())

	resp, err := executor.ExecuteSingleShot(context.Background(), SingleShotRequest{
		AgentType:    TypeArchivist,
		UserID:       123,
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
	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openrouter.ChatCompletionRequest) bool {
		return len(req.Messages) == 3 // system + user + assistant
	})).Return(testutil.MockChatResponse("Response based on context"), nil)

	executor := NewExecutor(mockClient, nil, testutil.TestLogger())

	resp, err := executor.ExecuteSingleShot(context.Background(), SingleShotRequest{
		AgentType: TypeLaplace,
		UserID:    123,
		Model:     "test-model",
		Messages: []openrouter.Message{
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
	mockClient := &testutil.MockOpenRouterClient{}
	executor := NewExecutor(mockClient, nil, testutil.TestLogger())

	assert.Equal(t, mockClient, executor.Client())
}

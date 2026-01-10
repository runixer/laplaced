package enricher

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

// mockChatResponse creates a mock ChatCompletionResponse with the given content.
func mockChatResponse(content string) openrouter.ChatCompletionResponse {
	var resp openrouter.ChatCompletionResponse
	resp.Choices = append(resp.Choices, struct {
		Message struct {
			Role             string                `json:"role"`
			Content          string                `json:"content"`
			ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
			ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason,omitempty"`
		Index        int    `json:"index"`
	}{
		Message: struct {
			Role             string                `json:"role"`
			Content          string                `json:"content"`
			ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
			ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
		}{
			Role:    "assistant",
			Content: content,
		},
	})
	resp.Usage.PromptTokens = 100
	resp.Usage.CompletionTokens = 20
	resp.Usage.TotalTokens = 120
	return resp
}

func TestEnricher_Execute(t *testing.T) {
	tests := []struct {
		name           string
		query          string
		history        []storage.Message
		llmResponse    string
		expectedResult string
		expectError    bool
	}{
		{
			name:           "simple query expansion",
			query:          "что мы обсуждали про Go?",
			llmResponse:    "Go programming golang обсуждение разработка",
			expectedResult: "Go programming golang обсуждение разработка",
		},
		{
			name:  "query with history",
			query: "а ещё?",
			history: []storage.Message{
				{Role: "user", Content: "Расскажи про Docker"},
				{Role: "assistant", Content: "Docker - это платформа контейнеризации..."},
			},
			llmResponse:    "Docker контейнеры контейнеризация развертывание",
			expectedResult: "Docker контейнеры контейнеризация развертывание",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockClient := &testutil.MockOpenRouterClient{}
			mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
				Return(mockChatResponse(tt.llmResponse), nil)

			executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
			cfg := testutil.TestConfig()
			cfg.Agents.Enricher.Model = "test-model"
			translator := testutil.TestTranslator(t)

			enricher := New(executor, translator, cfg)

			// Build request
			req := &agent.Request{
				Query: tt.query,
				Shared: &agent.SharedContext{
					UserID:       123,
					Profile:      "<user_profile>\n- Software engineer\n</user_profile>",
					RecentTopics: "<recent_topics>\n</recent_topics>",
				},
			}
			if len(tt.history) > 0 {
				req.Params = map[string]any{
					ParamHistory: tt.history,
				}
			}

			// Execute
			resp, err := enricher.Execute(context.Background(), req)

			// Assert
			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedResult, resp.Content)
			assert.NotZero(t, resp.Tokens.Total)

			// Check metadata
			assert.Equal(t, tt.query, resp.Metadata["original_query"])

			mockClient.AssertExpectations(t)
		})
	}
}

func TestEnricher_NoModel(t *testing.T) {
	// When no model is configured, enricher should return original query
	mockClient := &testutil.MockOpenRouterClient{}
	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Enricher.Model = "" // No model configured
	cfg.Agents.Default.Model = ""  // No default either
	translator := testutil.TestTranslator(t)

	enricher := New(executor, translator, cfg)

	req := &agent.Request{
		Query: "test query",
	}

	resp, err := enricher.Execute(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "test query", resp.Content)

	// Should not call LLM
	mockClient.AssertNotCalled(t, "CreateChatCompletion")
}

func TestEnricher_Type(t *testing.T) {
	enricher := &Enricher{}
	assert.Equal(t, agent.TypeEnricher, enricher.Type())
}

func TestEnricher_Capabilities(t *testing.T) {
	enricher := &Enricher{}
	caps := enricher.Capabilities()

	assert.False(t, caps.IsAgentic)
	assert.Contains(t, caps.SupportedMedia, "image")
	assert.Contains(t, caps.SupportedMedia, "audio")
	assert.Equal(t, "text", caps.OutputFormat)
}

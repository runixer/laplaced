package splitter

import (
	"context"
	"testing"
	"time"

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
	resp.Usage.PromptTokens = 200
	resp.Usage.CompletionTokens = 50
	resp.Usage.TotalTokens = 250
	return resp
}

func TestSplitter_Execute(t *testing.T) {
	messages := []storage.Message{
		{ID: 100, Role: "user", Content: "Привет!", CreatedAt: time.Now()},
		{ID: 101, Role: "assistant", Content: "Привет! Чем могу помочь?", CreatedAt: time.Now()},
		{ID: 102, Role: "user", Content: "Расскажи про Go", CreatedAt: time.Now()},
		{ID: 103, Role: "assistant", Content: "Go - это язык программирования...", CreatedAt: time.Now()},
	}

	llmResponse := `{"topics":[{"summary":"Приветствие и начало разговора","start_msg_id":100,"end_msg_id":101},{"summary":"Обсуждение языка Go","start_msg_id":102,"end_msg_id":103}]}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(mockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)

	splitter := New(executor, translator, cfg, nil, nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID:       123,
			Profile:      "<user_profile>\n</user_profile>",
			RecentTopics: "<recent_topics>\n</recent_topics>",
		},
		Params: map[string]any{
			ParamMessages: messages,
		},
	}

	resp, err := splitter.Execute(context.Background(), req)
	require.NoError(t, err)

	// Check structured result
	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	require.Len(t, result.Topics, 2)

	assert.Equal(t, "Приветствие и начало разговора", result.Topics[0].Summary)
	assert.Equal(t, int64(100), result.Topics[0].StartMsgID)
	assert.Equal(t, int64(101), result.Topics[0].EndMsgID)

	assert.Equal(t, "Обсуждение языка Go", result.Topics[1].Summary)
	assert.Equal(t, int64(102), result.Topics[1].StartMsgID)
	assert.Equal(t, int64(103), result.Topics[1].EndMsgID)

	mockClient.AssertExpectations(t)
}

func TestSplitter_EmptyMessages(t *testing.T) {
	executor := agent.NewExecutor(nil, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	splitter := New(executor, translator, cfg, nil, nil)

	req := &agent.Request{
		Params: map[string]any{
			ParamMessages: []storage.Message{},
		},
	}

	resp, err := splitter.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	assert.Empty(t, result.Topics)
}

func TestSplitter_Type(t *testing.T) {
	splitter := &Splitter{}
	assert.Equal(t, agent.TypeSplitter, splitter.Type())
}

func TestSplitter_Capabilities(t *testing.T) {
	splitter := &Splitter{}
	caps := splitter.Capabilities()

	assert.False(t, caps.IsAgentic)
	assert.Equal(t, "json", caps.OutputFormat)
}

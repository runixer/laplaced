package archivist

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
	resp.Usage.PromptTokens = 500
	resp.Usage.CompletionTokens = 100
	resp.Usage.TotalTokens = 600
	return resp
}

func TestArchivist_Execute_AddFacts(t *testing.T) {
	llmResponse := `{
		"added": [{
			"entity": "User",
			"relation": "works_as",
			"content": "Software Engineer",
			"category": "work",
			"type": "identity",
			"importance": 90,
			"reason": "User mentioned their profession"
		}],
		"updated": [],
		"removed": []
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(mockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID: 123,
		},
		Params: map[string]any{
			ParamMessages: []storage.Message{
				{ID: 1, Role: "user", Content: "I work as a Software Engineer", CreatedAt: time.Now()},
				{ID: 2, Role: "assistant", Content: "That's great!", CreatedAt: time.Now()},
			},
			ParamFacts:         []storage.Fact{},
			ParamReferenceDate: time.Now(),
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	require.Len(t, result.Added, 1)
	assert.Equal(t, "User", result.Added[0].Entity)
	assert.Equal(t, "works_as", result.Added[0].Relation)
	assert.Equal(t, "Software Engineer", result.Added[0].Content)
	assert.Equal(t, 1, resp.Metadata["added_count"])
	assert.Equal(t, 0, resp.Metadata["updated_count"])
	assert.Equal(t, 0, resp.Metadata["removed_count"])

	mockClient.AssertExpectations(t)
}

func TestArchivist_Execute_UpdateFacts(t *testing.T) {
	llmResponse := `{
		"added": [],
		"updated": [{
			"id": 42,
			"content": "Senior Software Engineer",
			"type": "identity",
			"importance": 95,
			"reason": "User got promoted"
		}],
		"removed": []
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(mockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID: 123,
		},
		Params: map[string]any{
			ParamMessages: []storage.Message{
				{ID: 1, Role: "user", Content: "I just got promoted!", CreatedAt: time.Now()},
			},
			ParamFacts: []storage.Fact{
				{ID: 42, UserID: 123, Entity: "User", Relation: "works_as", Content: "Software Engineer", Category: "work", Type: "identity", Importance: 90},
			},
			ParamReferenceDate: time.Now(),
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	require.Len(t, result.Updated, 1)
	assert.Equal(t, int64(42), result.Updated[0].ID)
	assert.Equal(t, "Senior Software Engineer", result.Updated[0].Content)

	mockClient.AssertExpectations(t)
}

func TestArchivist_Execute_RemoveFacts(t *testing.T) {
	llmResponse := `{
		"added": [],
		"updated": [],
		"removed": [{
			"id": 99,
			"reason": "User said this is no longer true"
		}]
	}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(mockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "test-model"
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID: 123,
		},
		Params: map[string]any{
			ParamMessages: []storage.Message{
				{ID: 1, Role: "user", Content: "I no longer work there", CreatedAt: time.Now()},
			},
			ParamFacts: []storage.Fact{
				{ID: 99, UserID: 123, Entity: "User", Content: "Works at Company X"},
			},
			ParamReferenceDate: time.Now(),
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	require.Len(t, result.Removed, 1)
	assert.Equal(t, int64(99), result.Removed[0].ID)
	assert.Equal(t, 1, resp.Metadata["removed_count"])

	mockClient.AssertExpectations(t)
}

func TestArchivist_Execute_EmptyMessages(t *testing.T) {
	executor := agent.NewExecutor(nil, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	archivist := New(executor, translator, cfg)

	req := &agent.Request{
		Params: map[string]any{
			ParamMessages: []storage.Message{},
		},
	}

	resp, err := archivist.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	assert.Empty(t, result.Added)
	assert.Empty(t, result.Updated)
	assert.Empty(t, result.Removed)
}

func TestArchivist_Type(t *testing.T) {
	archivist := &Archivist{}
	assert.Equal(t, agent.TypeArchivist, archivist.Type())
}

func TestArchivist_Capabilities(t *testing.T) {
	archivist := &Archivist{}
	caps := archivist.Capabilities()

	assert.False(t, caps.IsAgentic)
	assert.Equal(t, "json", caps.OutputFormat)
}

func TestArchivist_FormatUserName(t *testing.T) {
	a := &Archivist{}

	tests := []struct {
		name     string
		user     *storage.User
		expected string
	}{
		{
			name:     "nil user",
			user:     nil,
			expected: "User",
		},
		{
			name: "full name with username",
			user: &storage.User{
				ID:        123,
				FirstName: "John",
				LastName:  "Doe",
				Username:  "johndoe",
			},
			expected: "John Doe (@johndoe)",
		},
		{
			name: "only first name",
			user: &storage.User{
				ID:        123,
				FirstName: "John",
			},
			expected: "John",
		},
		{
			name: "only username",
			user: &storage.User{
				ID:       123,
				Username: "johndoe",
			},
			expected: "@johndoe",
		},
		{
			name: "only id",
			user: &storage.User{
				ID: 123,
			},
			expected: "ID:123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := a.formatUserName(tt.user)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestArchivist_PrepareFacts(t *testing.T) {
	a := &Archivist{}

	facts := []storage.Fact{
		{ID: 1, Entity: "User", Relation: "works_as", Content: "Engineer", Category: "work", Type: "identity", Importance: 90},
		{ID: 2, Entity: "user", Relation: "lives_in", Content: "Moscow", Category: "bio", Type: "context", Importance: 80},
		{ID: 3, Entity: "Bot", Relation: "created_by", Content: "Developer", Category: "other", Type: "identity", Importance: 50},
	}

	userFacts, otherFacts := a.prepareFacts(facts)

	assert.Len(t, userFacts, 2)
	assert.Len(t, otherFacts, 1)

	assert.Equal(t, int64(1), userFacts[0].ID)
	assert.Equal(t, int64(2), userFacts[1].ID)
	assert.Equal(t, int64(3), otherFacts[0].ID)
}

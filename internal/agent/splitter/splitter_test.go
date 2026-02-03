package splitter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

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
		Return(testutil.MockChatResponse(llmResponse), nil)

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

func TestSplitter_getMessages(t *testing.T) {
	t.Helper()

	messages := []storage.Message{
		{ID: 1, Role: "user", Content: "Hello"},
		{ID: 2, Role: "assistant", Content: "Hi!"},
	}

	tests := []struct {
		name     string
		params   map[string]any
		expected []storage.Message
	}{
		{
			name:     "no params",
			params:   nil,
			expected: nil,
		},
		{
			name:     "empty params",
			params:   map[string]any{},
			expected: nil,
		},
		{
			name: "wrong type",
			params: map[string]any{
				ParamMessages: "not a slice",
			},
			expected: nil,
		},
		{
			name: "valid messages",
			params: map[string]any{
				ParamMessages: messages,
			},
			expected: messages,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Splitter{}
			result := s.getMessages(&agent.Request{Params: tt.params})
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSplitter_getUserID(t *testing.T) {
	t.Helper()

	tests := []struct {
		name     string
		req      *agent.Request
		expected int64
	}{
		{
			name:     "nil request",
			req:      nil,
			expected: 0,
		},
		{
			name:     "no shared context",
			req:      &agent.Request{},
			expected: 0,
		},
		{
			name: "with shared context",
			req: &agent.Request{
				Shared: &agent.SharedContext{UserID: 123},
			},
			expected: 123,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Splitter{}
			result := s.getUserID(tt.req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSplitter_buildUserMessage(t *testing.T) {
	t.Helper()

	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)
	s := New(nil, translator, cfg, nil, nil)

	messages := []storage.Message{
		{
			ID:        100,
			Role:      "user",
			Content:   "Привет! Как дела?",
			CreatedAt: time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		},
		{
			ID:        101,
			Role:      "assistant",
			Content:   "Привет! Всё отлично, спасибо.",
			CreatedAt: time.Date(2025, 1, 15, 10, 30, 5, 0, time.UTC),
		},
	}

	result := s.buildUserMessage(messages)

	assert.Contains(t, result, "Chat Log JSON:")
	assert.Contains(t, result, `"id":100`)
	assert.Contains(t, result, `"role":"user"`)
	assert.Contains(t, result, "Привет! Как дела?")
	assert.Contains(t, result, `"date":"2025-01-15 10:30:00"`)
}

func TestSplitter_buildJSONSchema(t *testing.T) {
	t.Helper()

	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)
	s := New(nil, translator, cfg, nil, nil)

	schema := s.buildJSONSchema()

	assert.NotNil(t, schema)
	assert.Equal(t, "json_schema", schema["type"])

	jsonSchema := schema["json_schema"].(map[string]interface{})
	assert.Equal(t, "topic_extraction", jsonSchema["name"])
	assert.True(t, jsonSchema["strict"].(bool))

	schemaDef := jsonSchema["schema"].(map[string]interface{})
	assert.Equal(t, "object", schemaDef["type"])

	properties := schemaDef["properties"].(map[string]interface{})
	assert.Contains(t, properties, "topics")

	topicsProp := properties["topics"].(map[string]interface{})
	assert.Equal(t, "array", topicsProp["type"])

	items := topicsProp["items"].(map[string]interface{})
	itemProps := items["properties"].(map[string]interface{})
	assert.Contains(t, itemProps, "summary")
	assert.Contains(t, itemProps, "start_msg_id")
	assert.Contains(t, itemProps, "end_msg_id")
}

func TestSplitter_SingleMessage(t *testing.T) {
	// Test single message results in single topic
	messages := []storage.Message{
		{ID: 100, Role: "user", Content: "Hello world", CreatedAt: time.Now()},
	}

	llmResponse := `{"topics":[{"summary":"Greeting","start_msg_id":100,"end_msg_id":100}]}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

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

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	require.Len(t, result.Topics, 1)
	assert.Equal(t, "Greeting", result.Topics[0].Summary)

	mockClient.AssertExpectations(t)
}

func TestSplitter_ParseError(t *testing.T) {
	// Test invalid JSON response from LLM
	messages := []storage.Message{
		{ID: 100, Role: "user", Content: "Hello", CreatedAt: time.Now()},
	}

	llmResponse := `not valid json at all`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

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

	_, err := splitter.Execute(context.Background(), req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "json parse error")

	mockClient.AssertExpectations(t)
}

func TestSplitter_NoModel(t *testing.T) {
	executor := agent.NewExecutor(nil, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Archivist.Model = "" // No model
	cfg.Agents.Default.Model = ""   // No default
	translator := testutil.TestTranslator(t)

	splitter := New(executor, translator, cfg, nil, nil)

	req := &agent.Request{
		Params: map[string]any{
			ParamMessages: []storage.Message{
				{ID: 1, Role: "user", Content: "Hello"},
			},
		},
	}

	_, err := splitter.Execute(context.Background(), req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no model configured")
}

func TestSplitter_Description(t *testing.T) {
	splitter := &Splitter{}
	assert.Equal(t, "Segments conversation logs into distinct topics", splitter.Description())
}

func TestSplitter_getContext_WithFallback(t *testing.T) {
	// Test getContext fallback to direct loading when SharedContext is empty
	mockStore := new(testutil.MockStorage)

	// Setup mock fact repo to return facts
	mockStore.On("GetFacts", int64(123)).Return([]storage.Fact{
		{ID: 1, Relation: "test_relation", Content: "Test fact", Category: "test", Type: "identity", Importance: 80},
	}, nil)

	// Setup mock topic repo
	mockStore.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{
			Data: []storage.TopicExtended{
				{Topic: storage.Topic{ID: 1, Summary: "Test topic"}},
			},
		}, nil)

	cfg := testutil.TestConfig()
	cfg.RAG.RecentTopicsInContext = 5
	translator := testutil.TestTranslator(t)

	splitter := New(nil, translator, cfg, mockStore, mockStore)

	// Call with empty SharedContext (should trigger fallback)
	profile, recentTopics := splitter.getContext(context.Background(), &agent.Request{
		Shared: &agent.SharedContext{
			UserID:       123,
			Profile:      "", // Empty - triggers fallback
			RecentTopics: "", // Empty - triggers fallback
		},
	}, 123)

	// Should have loaded from storage due to fallback
	// FormatUserProfile formats facts differently, just check it's not empty and contains fact content
	assert.NotEmpty(t, profile)
	assert.Contains(t, profile, "Test fact")
	assert.Contains(t, recentTopics, "Test topic")

	mockStore.AssertExpectations(t)
}

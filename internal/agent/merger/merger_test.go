package merger

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestMerger_Execute_ShouldMerge(t *testing.T) {
	llmResponse := `{"should_merge": true, "new_summary": "Обсуждение Go и его применения в разработке"}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Merger.Model = "test-model"
	translator := testutil.TestTranslator(t)

	merger := New(executor, translator, cfg, nil, nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID:       123,
			Profile:      "<user_profile>\n</user_profile>",
			RecentTopics: "<recent_topics>\n</recent_topics>",
		},
		Params: map[string]any{
			ParamTopic1Summary: "Знакомство с языком Go",
			ParamTopic2Summary: "Применение Go в веб-разработке",
		},
	}

	resp, err := merger.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	assert.True(t, result.ShouldMerge)
	assert.Equal(t, "Обсуждение Go и его применения в разработке", result.NewSummary)
	assert.Equal(t, true, resp.Metadata["should_merge"])

	mockClient.AssertExpectations(t)
}

func TestMerger_Execute_ShouldNotMerge(t *testing.T) {
	llmResponse := `{"should_merge": false, "new_summary": ""}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Merger.Model = "test-model"
	translator := testutil.TestTranslator(t)

	merger := New(executor, translator, cfg, nil, nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID:       123,
			Profile:      "<user_profile>\n</user_profile>",
			RecentTopics: "<recent_topics>\n</recent_topics>",
		},
		Params: map[string]any{
			ParamTopic1Summary: "Обсуждение Go",
			ParamTopic2Summary: "Планы на отпуск",
		},
	}

	resp, err := merger.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	assert.False(t, result.ShouldMerge)

	mockClient.AssertExpectations(t)
}

func TestMerger_MissingTopicSummaries(t *testing.T) {
	executor := agent.NewExecutor(nil, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Merger.Model = "test-model"
	translator := testutil.TestTranslator(t)

	merger := New(executor, translator, cfg, nil, nil)

	req := &agent.Request{
		Params: map[string]any{
			ParamTopic1Summary: "Only one topic",
			// Missing ParamTopic2Summary
		},
	}

	_, err := merger.Execute(context.Background(), req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "both topic summaries are required")
}

func TestMerger_Type(t *testing.T) {
	merger := &Merger{}
	assert.Equal(t, agent.TypeMerger, merger.Type())
}

func TestMerger_Capabilities(t *testing.T) {
	merger := &Merger{}
	caps := merger.Capabilities()

	assert.False(t, caps.IsAgentic)
	assert.Equal(t, "json", caps.OutputFormat)
}

func TestMerger_getTopicSummaries(t *testing.T) {
	t.Helper()

	tests := []struct {
		name           string
		params         map[string]any
		expectedTopic1 string
		expectedTopic2 string
	}{
		{
			name:           "no params",
			params:         nil,
			expectedTopic1: "",
			expectedTopic2: "",
		},
		{
			name:           "empty params",
			params:         map[string]any{},
			expectedTopic1: "",
			expectedTopic2: "",
		},
		{
			name: "only topic1",
			params: map[string]any{
				ParamTopic1Summary: "First topic",
			},
			expectedTopic1: "First topic",
			expectedTopic2: "",
		},
		{
			name: "only topic2",
			params: map[string]any{
				ParamTopic2Summary: "Second topic",
			},
			expectedTopic1: "",
			expectedTopic2: "Second topic",
		},
		{
			name: "both topics",
			params: map[string]any{
				ParamTopic1Summary: "First topic",
				ParamTopic2Summary: "Second topic",
			},
			expectedTopic1: "First topic",
			expectedTopic2: "Second topic",
		},
		{
			name: "wrong types",
			params: map[string]any{
				ParamTopic1Summary: 123,
				ParamTopic2Summary: true,
			},
			expectedTopic1: "",
			expectedTopic2: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Merger{}
			topic1, topic2 := m.getTopicSummaries(&agent.Request{Params: tt.params})
			assert.Equal(t, tt.expectedTopic1, topic1)
			assert.Equal(t, tt.expectedTopic2, topic2)
		})
	}
}

func TestMerger_getUserID(t *testing.T) {
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
				Shared: &agent.SharedContext{UserID: 456},
			},
			expected: 456,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Merger{}
			result := m.getUserID(tt.req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMerger_SameTopics(t *testing.T) {
	// Test merger with identical topic summaries
	llmResponse := `{"should_merge": true, "new_summary": "Обсуждение Go"}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Merger.Model = "test-model"
	translator := testutil.TestTranslator(t)

	merger := New(executor, translator, cfg, nil, nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID:       123,
			Profile:      "<user_profile>\n</user_profile>",
			RecentTopics: "<recent_topics>\n</recent_topics>",
		},
		Params: map[string]any{
			ParamTopic1Summary: "Обсуждение Go",
			ParamTopic2Summary: "Обсуждение Go",
		},
	}

	resp, err := merger.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	assert.True(t, result.ShouldMerge)

	mockClient.AssertExpectations(t)
}

func TestMerger_DifferentTopics(t *testing.T) {
	// Test merger with completely different topics
	llmResponse := `{"should_merge": false, "new_summary": ""}`

	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse(llmResponse), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Merger.Model = "test-model"
	translator := testutil.TestTranslator(t)

	merger := New(executor, translator, cfg, nil, nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID:       123,
			Profile:      "<user_profile>\n</user_profile>",
			RecentTopics: "<recent_topics>\n</recent_topics>",
		},
		Params: map[string]any{
			ParamTopic1Summary: "Обсуждение программирования на Go",
			ParamTopic2Summary: "Рецепт пирога с яблоками",
		},
	}

	resp, err := merger.Execute(context.Background(), req)
	require.NoError(t, err)

	result, ok := resp.Structured.(*Result)
	require.True(t, ok)
	assert.False(t, result.ShouldMerge)

	mockClient.AssertExpectations(t)
}

func TestMerger_ParseError(t *testing.T) {
	// Test invalid JSON response from LLM
	mockClient := &testutil.MockOpenRouterClient{}
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse("not valid json"), nil)

	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Merger.Model = "test-model"
	translator := testutil.TestTranslator(t)

	merger := New(executor, translator, cfg, nil, nil)

	req := &agent.Request{
		Shared: &agent.SharedContext{
			UserID:       123,
			Profile:      "<user_profile>\n</user_profile>",
			RecentTopics: "<recent_topics>\n</recent_topics>",
		},
		Params: map[string]any{
			ParamTopic1Summary: "Topic 1",
			ParamTopic2Summary: "Topic 2",
		},
	}

	_, err := merger.Execute(context.Background(), req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "json parse error")

	mockClient.AssertExpectations(t)
}

func TestMerger_NoModel(t *testing.T) {
	executor := agent.NewExecutor(nil, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Merger.Model = ""  // No model
	cfg.Agents.Default.Model = "" // No default
	translator := testutil.TestTranslator(t)

	merger := New(executor, translator, cfg, nil, nil)

	req := &agent.Request{
		Params: map[string]any{
			ParamTopic1Summary: "Topic 1",
			ParamTopic2Summary: "Topic 2",
		},
	}

	_, err := merger.Execute(context.Background(), req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no model configured")
}

func TestMerger_Description(t *testing.T) {
	merger := &Merger{}
	assert.Equal(t, "Evaluates whether two topics should be merged", merger.Description())
}

func TestMerger_getContext_WithFallback(t *testing.T) {
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

	merger := New(nil, translator, cfg, mockStore, mockStore)

	// Call with empty SharedContext (should trigger fallback)
	profile, recentTopics := merger.getContext(context.Background(), &agent.Request{
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

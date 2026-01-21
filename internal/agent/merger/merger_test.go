package merger

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/agent"
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

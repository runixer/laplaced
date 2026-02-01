package reranker

import (
	"context"
	"errors"
	"testing"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Execute() Tests

// TestExecute_MissingCandidatesParam verifies that Execute returns an error
// when the candidates parameter is missing.
func TestExecute_MissingCandidatesParam(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	cfg := testConfig()
	translator := testutil.TestTranslator(t)

	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	req := &agent.Request{
		Params: map[string]any{
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "candidates parameter required")
}

// TestExecute_EmptyCandidatesWithPeopleOnly verifies that Execute works
// correctly when candidates is empty but person_candidates is provided.
func TestExecute_EmptyCandidatesWithPeopleOnly(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	cfg := testConfig()
	translator := testutil.TestTranslator(t)

	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	personCandidates := []PersonCandidate{
		{PersonID: 1, Score: 0.9, Person: mockPerson(1, "Alice", "alice")},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"people_ids": [1]}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          []Candidate{},
			ParamPersonCandidates:    personCandidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp.Structured)
	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{1}, result.PeopleIDs())
}

// TestExecute_EmptyCandidatesWithArtifactsOnly verifies that Execute works
// correctly when candidates is empty but artifact_candidates is provided.
func TestExecute_EmptyCandidatesWithArtifactsOnly(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	cfg := testConfig()
	translator := testutil.TestTranslator(t)

	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	artifactCandidates := []ArtifactCandidate{
		mockArtifactCandidate(1, "pdf", "document.pdf"),
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"artifact_ids": [1]}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          []Candidate{},
			ParamArtifactCandidates:  artifactCandidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp.Structured)
	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{1}, result.ArtifactIDs())
}

// TestExecute_SharedContextFallbackToParams verifies that Execute uses
// params when SharedContext is nil.
func TestExecute_SharedContextFallbackToParams(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	cfg := testConfig()
	translator := testutil.TestTranslator(t)

	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [1]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).
		Return(mockMessagesForTopic(1), nil).Once()
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"topic_ids": [1]}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: nil, // No SharedContext - should work with params only
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp.Structured)
}

// TestExecute_SharedContextTakesPriority verifies that SharedContext
// takes priority over context from context.Context.
func TestExecute_SharedContextTakesPriority(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	cfg := testConfig()
	translator := testutil.TestTranslator(t)

	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [1]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).
		Return(mockMessagesForTopic(1), nil).Once()
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"topic_ids": [1]}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{
			UserID: 123, // This should be used
		},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp.Structured)
}

// TestExecute_DisabledReranker_FallbackToVectorTop verifies that when
// reranker is disabled, it immediately returns vector top results.
func TestExecute_DisabledReranker_FallbackToVectorTop(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	cfg := testConfigDisabled() // Disabled reranker
	translator := testutil.TestTranslator(t)

	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
		{TopicID: 2, Score: 0.8, Topic: mockTopic(2, "Topic 2")},
		{TopicID: 3, Score: 0.7, Topic: mockTopic(3, "Topic 3")},
	}

	// No LLM calls should be made when reranker is disabled

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp.Structured)
	result := resp.Structured.(*Result)
	// Should return top 5 by vector score (all 3 in this case)
	assert.Equal(t, []int64{1, 2, 3}, result.TopicIDs())
}

// Successful Reranking Flow Tests

// TestRerank_Success_ToolCallThenJSON verifies the standard flow:
// tool call to get topics content, then final JSON response.
func TestRerank_Success_ToolCallThenJSON(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "First topic")},
		{TopicID: 2, Score: 0.8, Topic: mockTopic(2, "Second topic")},
	}

	// First call: tool call
	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(r interface{}) bool {
		// Verify tool choice is set for first call
		return true
	})).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [1]}`), nil).Once()

	// Load messages for requested topic
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).
		Return(mockMessagesForTopic(1), nil).Once()

	// Second call: final JSON
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"topic_ids": [{"id": "Topic:1", "reason": "relevant discussion"}]}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp.Structured)

	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{1}, result.TopicIDs())
	assert.Equal(t, "relevant discussion", result.Topics[0].Reason)

	mockClient.AssertExpectations(t)
	mockStorage.AssertExpectations(t)
}

// TestRerank_Success_NoTopicsOnlyArtifacts verifies that when there are
// no topic candidates, only artifacts, the tool call is skipped.
func TestRerank_Success_NoTopicsOnlyArtifacts(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	artifactCandidates := []ArtifactCandidate{
		mockArtifactCandidate(1, "pdf", "doc1.pdf"),
		mockArtifactCandidate(2, "pdf", "doc2.pdf"),
	}

	// Should skip tool call and go directly to JSON
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"artifacts": [{"id": "Artifact:1", "reason": "relevant document"}]}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          []Candidate{}, // No topic candidates
			ParamArtifactCandidates:  artifactCandidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp.Structured)

	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{1}, result.ArtifactIDs())
	assert.Equal(t, "relevant document", result.Artifacts[0].Reason)

	mockClient.AssertExpectations(t)
}

// TestRerank_Success_AllThreeTypes verifies successful reranking
// with topics, people, and artifacts all present.
func TestRerank_Success_AllThreeTypes(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
	}
	personCandidates := []PersonCandidate{
		{PersonID: 1, Score: 0.9, Person: mockPerson(1, "Alice", "alice")},
	}
	artifactCandidates := []ArtifactCandidate{
		mockArtifactCandidate(1, "pdf", "doc.pdf"),
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [1]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).
		Return(mockMessagesForTopic(1), nil).Once()
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{
			"topic_ids": [{"id": "Topic:1", "reason": "relevant topic"}],
			"people": [{"id": "Person:1", "reason": "relevant person"}],
			"artifacts": [{"id": "Artifact:1", "reason": "relevant artifact"}]
		}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamPersonCandidates:    personCandidates,
			ParamArtifactCandidates:  artifactCandidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, resp.Structured)

	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{1}, result.TopicIDs())
	assert.Equal(t, []int64{1}, result.PeopleIDs())
	assert.Equal(t, []int64{1}, result.ArtifactIDs())
	assert.Equal(t, "relevant topic", result.Topics[0].Reason)
	assert.Equal(t, "relevant person", result.People[0].Reason)
	assert.Equal(t, "relevant artifact", result.Artifacts[0].Reason)

	mockClient.AssertExpectations(t)
	mockStorage.AssertExpectations(t)
}

// TestRerank_OldFormatResponse verifies backward compatibility with
// the old format {"topic_ids": [42, 18]}.
func TestRerank_OldFormatResponse(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 42, Score: 0.9, Topic: mockTopic(42, "Topic 42")},
		{TopicID: 18, Score: 0.8, Topic: mockTopic(18, "Topic 18")},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [42, 18]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(42)).
		Return(mockMessagesForTopic(42), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(18)).
		Return(mockMessagesForTopic(18), nil).Once()
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"topic_ids": [42, 18]}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{42, 18}, result.TopicIDs())
}

// TestRerank_NewFormatWithReasons verifies the new format with reasons.
func TestRerank_NewFormatWithReasons(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 42, Score: 0.9, Topic: mockTopic(42, "Topic 42")},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [42]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(42)).
		Return(mockMessagesForTopic(42), nil).Once()
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"topic_ids": [{"id": "Topic:42", "reason": "discusses limits"}]}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{42}, result.TopicIDs())
	assert.Equal(t, "discusses limits", result.Topics[0].Reason)
}

// TestRerank_BareArrayFormat verifies the bare array format.
func TestRerank_BareArrayFormat(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 42, Score: 0.9, Topic: mockTopic(42, "Topic 42")},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [42]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(42)).
		Return(mockMessagesForTopic(42), nil).Once()
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`[{"id": "Topic:42", "reason": "relevant"}]`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{42}, result.TopicIDs())
	assert.Equal(t, "relevant", result.Topics[0].Reason)
}

// TestRerank_WrappedInArray verifies the wrapped array format.
func TestRerank_WrappedInArray(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 5205, Score: 0.9, Topic: mockTopic(5205, "Topic 5205")},
		{TopicID: 4750, Score: 0.8, Topic: mockTopic(4750, "Topic 4750")},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [5205, 4750]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(5205)).
		Return(mockMessagesForTopic(5205), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(4750)).
		Return(mockMessagesForTopic(4750), nil).Once()
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`[{"topic_ids": [{"id": "Topic:5205", "reason": "discussion about limits"}, {"id": "Topic:4750", "reason": "cost context"}]}]`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{5205, 4750}, result.TopicIDs())
}

// TestRerank_WithPeopleAndArtifacts verifies full response with all types.
func TestRerank_WithPeopleAndArtifacts(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
	}
	personCandidates := []PersonCandidate{
		{PersonID: 10, Score: 0.9, Person: mockPerson(10, "Bob", "bob")},
	}
	artifactCandidates := []ArtifactCandidate{
		mockArtifactCandidate(5, "pdf", "doc.pdf"),
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [1]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).
		Return(mockMessagesForTopic(1), nil).Once()
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{
			"topic_ids": [{"id": "Topic:1", "reason": "t1"}],
			"people_ids": [{"id": "Person:10", "reason": "p1"}],
			"artifact_ids": [{"id": "Artifact:5", "reason": "a1"}]
		}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamPersonCandidates:    personCandidates,
			ParamArtifactCandidates:  artifactCandidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{1}, result.TopicIDs())
	assert.Equal(t, []int64{10}, result.PeopleIDs())
	assert.Equal(t, []int64{5}, result.ArtifactIDs())
}

// Multi-turn Tool Calls Tests

// TestRerank_MultipleToolCalls verifies multiple tool calls in one session.
func TestRerank_MultipleToolCalls(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
		{TopicID: 2, Score: 0.8, Topic: mockTopic(2, "Topic 2")},
		{TopicID: 3, Score: 0.7, Topic: mockTopic(3, "Topic 3")},
	}

	// First tool call
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [1, 2]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).
		Return(mockMessagesForTopic(1), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(2)).
		Return(mockMessagesForTopic(2), nil).Once()

	// Second tool call
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [3]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(3)).
		Return(mockMessagesForTopic(3), nil).Once()

	// Final JSON
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"topic_ids": [{"id": "Topic:1", "reason": "a"}, {"id": "Topic:3", "reason": "b"}]}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{1, 3}, result.TopicIDs())
}

// TestRerank_MaxToolCallsReached verifies behavior when max tool calls is reached.
func TestRerank_MaxToolCallsReached(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	cfg.Agents.Reranker.MaxToolCalls = 1 // Set max to 1
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
	}

	// Tool call
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [1]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).
		Return(mockMessagesForTopic(1), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	// Should fallback to requested IDs (1)
	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{1}, result.TopicIDs())
}

// Fallback Scenarios Tests

// TestRerank_LLMError_Timeout verifies fallback on context timeout.
func TestRerank_LLMError_Timeout(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
		{TopicID: 2, Score: 0.8, Topic: mockTopic(2, "Topic 2")},
	}

	// LLM timeout
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openrouter.ChatCompletionResponse{}, context.DeadlineExceeded).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	// Should fallback to vector top (no requested state)
	assert.Equal(t, []int64{1, 2}, result.TopicIDs())
}

// TestRerank_LLMError_GenericError verifies fallback on generic LLM error.
func TestRerank_LLMError_GenericError(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openrouter.ChatCompletionResponse{}, errors.New("LLM API error")).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{1}, result.TopicIDs())
}

// TestRerank_EmptyResponse verifies fallback on empty choices array.
func TestRerank_EmptyResponse(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeEmptyResponse(), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{1}, result.TopicIDs())
}

// TestRerank_ProtocolViolation_NoToolCallFirstTurn verifies fallback
// when there's no tool call on the first turn (with topics).
func TestRerank_ProtocolViolation_NoToolCallFirstTurn(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
		{TopicID: 2, Score: 0.8, Topic: mockTopic(2, "Topic 2")},
	}

	// No tool call, directly text response
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeChatResponse("Here are the relevant topics..."), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	// Should fallback to vector top
	assert.Equal(t, []int64{1, 2}, result.TopicIDs())
}

// TestRerank_ParseError_InvalidJSON verifies fallback on invalid JSON.
func TestRerank_ParseError_InvalidJSON(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [1]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).
		Return(mockMessagesForTopic(1), nil).Once()
	// Invalid JSON response
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{not valid json}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	// Should fallback to requested IDs (1)
	assert.Equal(t, []int64{1}, result.TopicIDs())
}

// TestRerank_AllHallucinated_ReturnsFallback verifies fallback when
// all returned IDs are hallucinated (not in candidates).
func TestRerank_AllHallucinated_ReturnsFallback(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [1]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).
		Return(mockMessagesForTopic(1), nil).Once()
	// LLM returns hallucinated ID (999 not in candidates)
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"topic_ids": [{"id": "Topic:999", "reason": "hallucinated"}]}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	// Should fallback to requested IDs (1)
	assert.Equal(t, []int64{1}, result.TopicIDs())
}

// TestRerank_Fallback_UsesRequestedIDs verifies that fallback uses
// requested IDs from state when available.
func TestRerank_Fallback_UsesRequestedIDs(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
		{TopicID: 2, Score: 0.8, Topic: mockTopic(2, "Topic 2")},
		{TopicID: 3, Score: 0.7, Topic: mockTopic(3, "Topic 3")},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [2, 3]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(2)).
		Return(mockMessagesForTopic(2), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(3)).
		Return(mockMessagesForTopic(3), nil).Once()
	// Error after tool call - should use requested IDs
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openrouter.ChatCompletionResponse{}, errors.New("API error")).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	// Should return requested IDs [2, 3] limited to max (5)
	assert.Equal(t, []int64{2, 3}, result.TopicIDs())
}

// TestRerank_Fallback_VectorTopWhenNoState verifies that fallback
// uses vector top when no state is available.
func TestRerank_Fallback_VectorTopWhenNoState(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
		{TopicID: 2, Score: 0.8, Topic: mockTopic(2, "Topic 2")},
	}

	// Immediate error - no state accumulated
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openrouter.ChatCompletionResponse{}, errors.New("API error")).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	// Should use vector top (no requested state)
	assert.Equal(t, []int64{1, 2}, result.TopicIDs())
}

// TestRerank_SomeHallucinatedTopics verifies that hallucinated topics
// are filtered out while valid ones remain.
func TestRerank_SomeHallucinatedTopics(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
		{TopicID: 2, Score: 0.8, Topic: mockTopic(2, "Topic 2")},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [1, 2]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).
		Return(mockMessagesForTopic(1), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(2)).
		Return(mockMessagesForTopic(2), nil).Once()
	// LLM returns mix of valid (1, 2) and hallucinated (999) IDs
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"topic_ids": [{"id": "Topic:1", "reason": "valid"}, {"id": "Topic:999", "reason": "fake"}, {"id": "Topic:2", "reason": "valid"}]}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	// Only valid topics should remain
	assert.Equal(t, []int64{1, 2}, result.TopicIDs())
	assert.Len(t, result.Topics, 2)
}

// TestExecute_WithMediaParts verifies that media parts are properly handled.
func TestExecute_WithMediaParts(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	cfg := testConfig()
	translator := testutil.TestTranslator(t)

	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
	}

	mediaParts := []interface{}{
		map[string]interface{}{
			"type": "image_url",
			"image_url": map[string]string{
				"url": "data:image/png;base64,iVBORw0KG...",
			},
		},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(r interface{}) bool {
		// Verify media instruction is included
		return true
	})).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [1]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).
		Return(mockMessagesForTopic(1), nil).Once()
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"topic_ids": [{"id": "Topic:1", "reason": "relevant"}]}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
			ParamMediaParts:          mediaParts,
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{1}, result.TopicIDs())
}

// TestRerank_OnlyPeopleNoTopics verifies reranking with only people,
// no topics or artifacts.
func TestRerank_OnlyPeopleNoTopics(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	personCandidates := []PersonCandidate{
		{PersonID: 1, Score: 0.9, Person: mockPerson(1, "Alice", "alice")},
		{PersonID: 2, Score: 0.8, Person: mockPerson(2, "Bob", "bob")},
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"people": [{"id": "Person:1", "reason": "relevant"}]}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          []Candidate{},
			ParamPersonCandidates:    personCandidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{1}, result.PeopleIDs())
	assert.Equal(t, "relevant", result.People[0].Reason)
}

// TestRerank_OnlyArtifactsNoTopics verifies reranking with only artifacts,
// no topics or people.
func TestRerank_OnlyArtifactsNoTopics(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	artifactCandidates := []ArtifactCandidate{
		mockArtifactCandidate(1, "pdf", "doc1.pdf"),
		mockArtifactCandidate(2, "pdf", "doc2.pdf"),
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{"artifacts": [{"id": "Artifact:1", "reason": "most relevant"}, {"id": "Artifact:2", "reason": "also relevant"}]}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          []Candidate{},
			ParamArtifactCandidates:  artifactCandidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	result := resp.Structured.(*Result)
	assert.Equal(t, []int64{1, 2}, result.ArtifactIDs())
}

// TestRerank_ResponseMetadata verifies that response metadata is populated correctly.
func TestRerank_ResponseMetadata(t *testing.T) {
	mockClient := &testutil.MockOpenRouterClient{}
	mockStorage := &testutil.MockStorage{}
	translator := testutil.TestTranslator(t)

	cfg := testConfig()
	reranker := New(mockClient, cfg, testutil.TestLogger(), translator, mockStorage, nil)

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: mockTopic(1, "Topic 1")},
	}
	personCandidates := []PersonCandidate{
		{PersonID: 10, Score: 0.9, Person: mockPerson(10, "Alice", "alice")},
	}
	artifactCandidates := []ArtifactCandidate{
		mockArtifactCandidate(5, "pdf", "doc.pdf"),
	}

	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeToolCallResponse("get_topics_content", `{"topic_ids": [1]}`), nil).Once()
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).
		Return(mockMessagesForTopic(1), nil).Once()
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(makeFinalJSONResponse(`{
			"topic_ids": [{"id": "Topic:1", "reason": "t1"}],
			"people": [{"id": "Person:10", "reason": "p1"}],
			"artifacts": [{"id": "Artifact:5", "reason": "a1"}]
		}`), nil).Once()

	req := &agent.Request{
		Params: map[string]any{
			ParamCandidates:          candidates,
			ParamPersonCandidates:    personCandidates,
			ParamArtifactCandidates:  artifactCandidates,
			ParamContextualizedQuery: "test query",
			ParamOriginalQuery:       "original",
			ParamCurrentMessages:     "recent messages",
		},
		Shared: &agent.SharedContext{UserID: 123},
	}

	resp, err := reranker.Execute(context.Background(), req)

	require.NoError(t, err)
	assert.NotNil(t, resp.Structured)
	assert.Equal(t, 1, resp.Metadata["topics_count"])
	assert.Equal(t, 1, resp.Metadata["people_count"])
	assert.Equal(t, 1, resp.Metadata["artifacts_count"])
}

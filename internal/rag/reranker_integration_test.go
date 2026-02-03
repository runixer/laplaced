package rag

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/reranker"
	agenttesting "github.com/runixer/laplaced/internal/agent/testing"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestExecuteReranker_Success tests successful reranker execution with topic selections.
func TestExecuteReranker_Success(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Agents.Reranker.Enabled = true

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	// Create test topics
	topicMap := map[int64]storage.Topic{
		1: {ID: 1, UserID: userID, Summary: "Topic 1", StartMsgID: 1, EndMsgID: 5},
		2: {ID: 2, UserID: userID, Summary: "Topic 2", StartMsgID: 6, EndMsgID: 10},
		3: {ID: 3, UserID: userID, Summary: "Topic 3", StartMsgID: 11, EndMsgID: 15},
	}

	// Create mock reranker agent
	mockReranker := new(agenttesting.MockAgent)

	// Setup reranker expectations using builders
	mockReranker.On("Type").Return(agent.TypeReranker).Maybe()
	mockReranker.On("Execute", mock.Anything, mock.MatchedBy(func(req *agent.Request) bool {
		// Verify request has required parameters
		_, hasCandidates := req.Params[reranker.ParamCandidates]
		_, hasQuery := req.Params[reranker.ParamContextualizedQuery]
		return hasCandidates && hasQuery
	})).Return(agent.NewResponseBuilder().
		WithStructured(reranker.NewRerankerResultBuilder().
			WithTopicWithReason(1, "Direct match to query").
			WithTopicWithReason(3, "Related context").
			Build()).
		Build(), nil)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetRerankerAgent(mockReranker)

	// Build reranker input
	candidates := []topicCandidate{
		{topicID: 1, score: 0.9},
		{topicID: 2, score: 0.7},
		{topicID: 3, score: 0.8},
	}

	input := rerankerInput{
		topicCandidates:     candidates,
		topicMap:            topicMap,
		contextualizedQuery: "What did we discuss about Go?",
		originalQuery:       "Go discussion",
		currentMessages:     "[User]: Tell me about Go",
		userProfile:         "User is a developer",
		recentTopics:        "Recent: Topic 1, Topic 2",
	}

	// Execute reranker
	output, err := svc.executeReranker(context.Background(), userID, input)

	assert.NoError(t, err)
	assert.NotNil(t, output)
	assert.Equal(t, []int64{1, 3}, output.selectedTopicIDs)
	assert.Equal(t, "Direct match to query", output.topicReasons[1])
	assert.Equal(t, "Related context", output.topicReasons[3])
	assert.Empty(t, output.selectedPersonIDs)
	assert.Empty(t, output.selectedArtifactIDs)

	mockReranker.AssertExpectations(t)
}

// TestExecuteReranker_AgentNotConfigured tests error when agent is nil.
func TestExecuteReranker_AgentNotConfigured(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Agents.Reranker.Enabled = true

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	// Don't set reranker agent

	input := rerankerInput{}

	_, err = svc.executeReranker(context.Background(), 123, input)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reranker agent not configured")
}

// TestExecuteReranker_AgentError tests fallback on agent error.
func TestExecuteReranker_AgentError(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Agents.Reranker.Enabled = true

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	topicMap := map[int64]storage.Topic{
		1: {ID: 1, UserID: userID, Summary: "Topic 1", StartMsgID: 1, EndMsgID: 5},
	}

	mockReranker := new(agenttesting.MockAgent)
	mockReranker.On("Type").Return(agent.TypeReranker).Maybe()
	mockReranker.On("Execute", mock.Anything, mock.Anything).
		Return(nil, errors.New("LLM API timeout"))

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetRerankerAgent(mockReranker)

	candidates := []topicCandidate{{topicID: 1, score: 0.9}}

	input := rerankerInput{
		topicCandidates:     candidates,
		topicMap:            topicMap,
		contextualizedQuery: "test query",
		originalQuery:       "test",
		currentMessages:     "",
		userProfile:         "",
		recentTopics:        "",
	}

	_, err = svc.executeReranker(context.Background(), userID, input)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "LLM API timeout")

	mockReranker.AssertExpectations(t)
}

// TestExecuteReranker_InvalidTopicID tests handling of invalid topic IDs.
func TestExecuteReranker_InvalidTopicID(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Agents.Reranker.Enabled = true

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	topicMap := map[int64]storage.Topic{
		1: {ID: 1, UserID: userID, Summary: "Topic 1", StartMsgID: 1, EndMsgID: 5},
	}

	mockReranker := new(agenttesting.MockAgent)
	mockReranker.On("Type").Return(agent.TypeReranker).Maybe()

	// Return a result with an invalid ID (non-numeric)
	mockReranker.On("Execute", mock.Anything, mock.Anything).
		Return(agent.NewResponseBuilder().
			WithStructured(&reranker.Result{
				Topics: []reranker.TopicSelection{
					{ID: "Topic:1", Reason: "Valid"},
					{ID: "Topic:invalid", Reason: "Invalid ID"}, // This should be skipped
					{ID: "Topic:abc", Reason: "Also invalid"},   // This should be skipped
				},
			}).
			Build(), nil)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetRerankerAgent(mockReranker)

	candidates := []topicCandidate{{topicID: 1, score: 0.9}}

	input := rerankerInput{
		topicCandidates:     candidates,
		topicMap:            topicMap,
		contextualizedQuery: "test query",
		originalQuery:       "test",
		currentMessages:     "",
		userProfile:         "",
		recentTopics:        "",
	}

	output, err := svc.executeReranker(context.Background(), userID, input)

	assert.NoError(t, err)
	assert.NotNil(t, output)
	// Only valid ID should be in the output
	assert.Equal(t, []int64{1}, output.selectedTopicIDs)
	assert.Len(t, output.topicReasons, 1)

	mockReranker.AssertExpectations(t)
}

// TestExecuteReranker_WithPeople tests loading selected people.
func TestExecuteReranker_WithPeople(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Agents.Reranker.Enabled = true

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	// Setup mock for GetPeopleByIDs
	people := []storage.Person{
		{ID: 100, DisplayName: "Alice", Circle: "Friends"},
		{ID: 200, DisplayName: "Bob", Circle: "Family"},
	}
	mockStore.On("GetPeopleByIDs", userID, []int64{100, 200}).Return(people, nil).Once()

	mockReranker := new(agenttesting.MockAgent)
	mockReranker.On("Type").Return(agent.TypeReranker).Maybe()
	mockReranker.On("Execute", mock.Anything, mock.Anything).
		Return(agent.NewResponseBuilder().
			WithStructured(reranker.NewRerankerResultBuilder().
				WithTopics(1).
				WithPersonWithReason(100, "Mentioned in query").
				WithPersonWithReason(200, "Related to topic").
				Build()).
			Build(), nil)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetRerankerAgent(mockReranker)
	svc.SetPeopleRepository(mockStore)

	input := rerankerInput{
		topicCandidates:     []topicCandidate{{topicID: 1, score: 0.9}},
		topicMap:            map[int64]storage.Topic{1: {ID: 1, UserID: userID, StartMsgID: 1, EndMsgID: 5}},
		personCandidates:    []reranker.PersonCandidate{},
		contextualizedQuery: "What did Alice say?",
		originalQuery:       "Alice",
		currentMessages:     "",
		userProfile:         "",
		recentTopics:        "",
	}

	output, err := svc.executeReranker(context.Background(), userID, input)

	assert.NoError(t, err)
	assert.NotNil(t, output)
	assert.Equal(t, []int64{100, 200}, output.selectedPersonIDs)
	assert.Len(t, output.personCandidates, 2)
	assert.Equal(t, int64(100), output.personCandidates[0].PersonID)
	assert.Equal(t, "Alice", output.personCandidates[0].Person.DisplayName)

	mockReranker.AssertExpectations(t)
	mockStore.AssertExpectations(t)
}

// TestRerankViaAgent_Success tests successful agent call.
func TestRerankViaAgent_Success(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	mockReranker := new(agenttesting.MockAgent)
	mockReranker.On("Type").Return(agent.TypeReranker).Maybe()
	mockReranker.On("Execute", mock.Anything, mock.MatchedBy(func(req *agent.Request) bool {
		// Verify request has correct parameters
		_, hasCandidates := req.Params[reranker.ParamCandidates]
		_, hasQuery := req.Params[reranker.ParamContextualizedQuery]
		_, hasOriginalQuery := req.Params[reranker.ParamOriginalQuery]
		return hasCandidates && hasQuery && hasOriginalQuery
	})).Return(agent.NewResponseBuilder().
		WithStructured(reranker.NewRerankerResultBuilder().
			WithTopics(1, 2, 3).
			Build()).
		WithTokens(100, 50, 150).
		WithDuration(1000000000). // 1 second
		Build(), nil)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetRerankerAgent(mockReranker)

	candidates := []reranker.Candidate{
		{TopicID: 1, Score: 0.9, Topic: storage.Topic{ID: 1, Summary: "Topic 1"}},
		{TopicID: 2, Score: 0.8, Topic: storage.Topic{ID: 2, Summary: "Topic 2"}},
	}

	result, err := svc.rerankViaAgent(
		context.Background(),
		userID,
		candidates,
		nil, // personCandidates
		nil, // artifactCandidates
		"enriched query",
		"original query",
		"[User]: test message",
		"user profile",
		"recent topics",
		nil, // mediaParts
	)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, []int64{1, 2, 3}, result.TopicIDs())

	mockReranker.AssertExpectations(t)
}

// TestRerankViaAgent_SharedContext tests with SharedContext in ctx.
func TestRerankViaAgent_SharedContext(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	// Create SharedContext
	shared := agent.NewSharedContextBuilder().
		WithUserID(userID).
		WithProfile("User is a developer").
		WithRecentTopics("Recent: Go, Python").
		Build()

	ctx := agent.WithContext(context.Background(), shared)

	mockReranker := new(agenttesting.MockAgent)
	mockReranker.On("Type").Return(agent.TypeReranker).Maybe()
	mockReranker.On("Execute", mock.Anything, mock.MatchedBy(func(req *agent.Request) bool {
		// Verify SharedContext is attached
		return req.Shared != nil &&
			req.Shared.UserID == userID &&
			req.Shared.Profile == "User is a developer"
	})).Return(agent.NewResponseBuilder().
		WithStructured(reranker.NewRerankerResultBuilder().
			WithTopics(1).
			Build()).
		Build(), nil)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetRerankerAgent(mockReranker)

	candidates := []reranker.Candidate{
		{TopicID: 1, Score: 0.9, Topic: storage.Topic{ID: 1, Summary: "Topic 1"}},
	}

	result, err := svc.rerankViaAgent(
		ctx,
		userID,
		candidates,
		nil, nil,
		"query",
		"original",
		"",
		"",
		"",
		nil,
	)

	assert.NoError(t, err)
	assert.NotNil(t, result)

	mockReranker.AssertExpectations(t)
}

// TestRerankViaAgent_UnexpectedType tests wrong result type.
func TestRerankViaAgent_UnexpectedType(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	mockReranker := new(agenttesting.MockAgent)
	mockReranker.On("Type").Return(agent.TypeReranker).Maybe()
	// Return wrong type in Structured
	mockReranker.On("Execute", mock.Anything, mock.Anything).
		Return(agent.NewResponseBuilder().
			WithStructured("not a reranker.Result").
			Build(), nil)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetRerankerAgent(mockReranker)

	candidates := []reranker.Candidate{
		{TopicID: 1, Score: 0.9, Topic: storage.Topic{ID: 1, Summary: "Topic 1"}},
	}

	_, err = svc.rerankViaAgent(
		context.Background(),
		userID,
		candidates,
		nil, nil,
		"query",
		"original",
		"",
		"",
		"",
		nil,
	)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected result type")

	mockReranker.AssertExpectations(t)
}

// TestRerankViaAgent_WithArtifacts tests reranking with artifact candidates.
func TestRerankViaAgent_WithArtifacts(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Artifacts.Enabled = true

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	mockReranker := new(agenttesting.MockAgent)
	mockReranker.On("Type").Return(agent.TypeReranker).Maybe()
	mockReranker.On("Execute", mock.Anything, mock.MatchedBy(func(req *agent.Request) bool {
		_, hasArtifactCandidates := req.Params[reranker.ParamArtifactCandidates]
		return hasArtifactCandidates
	})).Return(agent.NewResponseBuilder().
		WithStructured(reranker.NewRerankerResultBuilder().
			WithTopics(1).
			WithArtifactWithReason(10, "Contains relevant documentation").
			Build()).
		Build(), nil)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetRerankerAgent(mockReranker)

	candidates := []reranker.Candidate{
		{TopicID: 1, Score: 0.9, Topic: storage.Topic{ID: 1, Summary: "Topic 1"}},
	}

	artifactCandidates := []reranker.ArtifactCandidate{
		{ArtifactID: 10, Score: 0.85, FileType: "pdf", OriginalName: "doc.pdf"},
		{ArtifactID: 20, Score: 0.75, FileType: "image", OriginalName: "photo.jpg"},
	}

	result, err := svc.rerankViaAgent(
		context.Background(),
		userID,
		candidates,
		nil, // personCandidates
		artifactCandidates,
		"query",
		"original",
		"",
		"",
		"",
		nil,
	)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, []int64{10}, result.ArtifactIDs())

	mockReranker.AssertExpectations(t)
}

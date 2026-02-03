package rag

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestSearchTopicCandidates tests vector search for topics.
func TestSearchTopicCandidates(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()

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

	userID := int64(123)

	// Add some topic vectors
	svc.mu.Lock()
	svc.topicVectors[userID] = []TopicVectorItem{
		{TopicID: 1, Embedding: []float32{1.0, 0.0, 0.0}},
		{TopicID: 2, Embedding: []float32{0.9, 0.1, 0.0}}, // Changed to be more similar to query
		{TopicID: 3, Embedding: []float32{0.8, 0.2, 0.0}}, // Changed to be similar to query
	}
	svc.mu.Unlock()

	queryEmbedding := []float32{1.0, 0.0, 0.0}

	// Test with limit=0 (no limit)
	candidates, vectorsScanned := svc.searchTopicCandidates(userID, queryEmbedding, 0)
	assert.Equal(t, 3, vectorsScanned)
	assert.Len(t, candidates, 3)
	assert.Equal(t, int64(1), candidates[0].topicID) // Highest score

	// Test with limit=2
	candidates, _ = svc.searchTopicCandidates(userID, queryEmbedding, 2)
	assert.Len(t, candidates, 2)
}

// TestPrepareRerankerContext_SharedContext tests context loading with SharedContext.
func TestPrepareRerankerContext_SharedContext(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()

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
	svc.SetPeopleRepository(mockStore)

	userID := int64(123)

	// Setup SharedContext in context
	profileFacts := []storage.Fact{
		{ID: 1, UserID: userID, Content: "User is a developer", Relation: "identity", Importance: 90},
	}
	shared := &agent.SharedContext{
		UserID:       userID,
		ProfileFacts: profileFacts,
		RecentTopics: "Recent: Topic 1, Topic 2",
		Language:     "en",
	}

	ctx := agent.WithContext(context.Background(), shared)

	userProfile, recentTopics, currentMessages, err := svc.prepareRerankerContext(ctx, userID, nil)

	assert.NoError(t, err)
	assert.Contains(t, userProfile, "User is a developer")
	assert.Contains(t, recentTopics, "Topic 1")
	assert.Equal(t, "(no current session messages)", currentMessages)
}

// TestPrepareRerankerContext_Fallback tests context loading without SharedContext.
func TestPrepareRerankerContext_Fallback(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	allFacts := []storage.Fact{
		{ID: 1, UserID: userID, Content: "Fact 1", Relation: "test", Importance: 50},
		{ID: 2, UserID: userID, Content: "Fact 2", Relation: "identity", Importance: 90},
	}

	mockStore.On("GetFacts", userID).Return(allFacts, nil).Once()
	mockStore.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{Data: []storage.TopicExtended{
			{Topic: storage.Topic{ID: 1, Summary: "Recent Topic 1"}},
		}}, nil).Maybe()

	// Create service without SharedContext
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
	svc.SetPeopleRepository(mockStore)

	ctx := context.Background()

	userProfile, _, _, err := svc.prepareRerankerContext(ctx, userID, nil)

	assert.NoError(t, err)
	assert.Contains(t, userProfile, "Fact 2")    // Profile fact with high importance
	assert.NotContains(t, userProfile, "Fact 1") // Regular fact not in profile
}

// TestFormatSessionMessages tests formatting of session messages.
func TestFormatSessionMessages(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()

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

	history := []storage.Message{
		{Role: "user", Content: "First message"},
		{Role: "assistant", Content: "First response"},
		{Role: "user", Content: "Second message"},
	}

	formatted := svc.formatSessionMessages(history)

	assert.Contains(t, formatted, "[User]: First message")
	assert.Contains(t, formatted, "[Assistant]: First response")
	assert.Contains(t, formatted, "[User]: Second message")
}

// TestFormatSessionMessages_EmptyHistory tests empty history.
func TestFormatSessionMessages_EmptyHistory(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()

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

	formatted := svc.formatSessionMessages([]storage.Message{})

	assert.Equal(t, "(no current session messages)", formatted)
}

// TestFormatSessionMessages_Truncation tests long message truncation.
func TestFormatSessionMessages_Truncation(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()

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

	// Create a message with >500 chars
	longContent := strings.Repeat("This is a long message. ", 30) // ~540 chars

	history := []storage.Message{
		{Role: "user", Content: longContent},
	}

	formatted := svc.formatSessionMessages(history)

	assert.Contains(t, formatted, "...")
	assert.LessOrEqual(t, len(formatted), 550) // Allow some margin for the truncation
}

// TestLimitMatches tests limiting topic candidates.
func TestLimitMatches(t *testing.T) {
	tests := []struct {
		name     string
		matches  []topicCandidate
		max      int
		expected int
	}{
		{
			name: "no limit",
			matches: []topicCandidate{
				{topicID: 1},
				{topicID: 2},
				{topicID: 3},
			},
			max:      0,
			expected: 3,
		},
		{
			name: "limit 2",
			matches: []topicCandidate{
				{topicID: 1},
				{topicID: 2},
				{topicID: 3},
			},
			max:      2,
			expected: 2,
		},
		{
			name: "limit 10 with 3 items",
			matches: []topicCandidate{
				{topicID: 1},
				{topicID: 2},
				{topicID: 3},
			},
			max:      10,
			expected: 3,
		},
		{
			name:     "empty matches",
			matches:  []topicCandidate{},
			max:      5,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := limitMatches(tt.matches, tt.max)
			assert.Len(t, result, tt.expected)
		})
	}
}

// TestLoadTopicMap tests loading topics by IDs.
func TestLoadTopicMap(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	topics := []storage.Topic{
		{ID: 1, UserID: userID, Summary: "Topic 1"},
		{ID: 2, UserID: userID, Summary: "Topic 2"},
	}

	mockStore.On("GetTopicsByIDs", userID, []int64{1, 2}).Return(topics, nil).Once()

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

	matches := []topicCandidate{
		{topicID: 1},
		{topicID: 2},
	}

	topicMap, err := svc.loadTopicMap(context.Background(), userID, matches)
	assert.NoError(t, err)
	assert.Len(t, topicMap, 2)
	assert.Equal(t, int64(1), topicMap[1].ID)
	assert.Equal(t, int64(2), topicMap[2].ID)

	mockStore.AssertExpectations(t)
}

// TestShouldUseReranker tests when reranker should be used.
func TestShouldUseReranker(t *testing.T) {
	tests := []struct {
		name         string
		enabled      bool
		lenMatches   int
		lenPeople    int
		lenArtifacts int
		expected     bool
	}{
		{
			name:         "reranker disabled",
			enabled:      false,
			lenMatches:   5,
			lenPeople:    0,
			lenArtifacts: 0,
			expected:     false,
		},
		{
			name:         "reranker enabled with candidates",
			enabled:      true,
			lenMatches:   5,
			lenPeople:    0,
			lenArtifacts: 0,
			expected:     true,
		},
		{
			name:         "reranker enabled with only people",
			enabled:      true,
			lenMatches:   0,
			lenPeople:    3,
			lenArtifacts: 0,
			expected:     true,
		},
		{
			name:         "reranker enabled with only artifacts",
			enabled:      true,
			lenMatches:   0,
			lenPeople:    0,
			lenArtifacts: 2,
			expected:     true,
		},
		{
			name:         "reranker enabled with no candidates",
			enabled:      true,
			lenMatches:   0,
			lenPeople:    0,
			lenArtifacts: 0,
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testutil.TestConfig()
			cfg.Agents.Reranker.Enabled = tt.enabled

			svc := &Service{cfg: cfg}
			result := svc.shouldUseReranker(tt.lenMatches, tt.lenPeople, tt.lenArtifacts)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetMaxCandidates tests getting max candidates based on reranker usage.
func TestGetMaxCandidates(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		candidates int
		retrieved  int
		expected   int
	}{
		{
			name:       "reranker enabled",
			enabled:    true,
			candidates: 50,
			expected:   50,
		},
		{
			name:      "reranker disabled",
			enabled:   false,
			retrieved: 10,
			expected:  10,
		},
		{
			name:       "reranker enabled with zero candidates",
			enabled:    true,
			candidates: 0,
			expected:   50, // Function returns default 50 when candidates is 0
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testutil.TestConfig()
			cfg.Agents.Reranker.Enabled = tt.enabled
			cfg.Agents.Reranker.Candidates = tt.candidates
			cfg.RAG.RetrievedTopicsCount = tt.retrieved

			svc := &Service{cfg: cfg}
			result := svc.getMaxCandidates(tt.enabled)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestFilterMatchesByReranker tests filtering matches by reranker selection.
func TestFilterMatchesByReranker(t *testing.T) {
	matches := []topicCandidate{
		{topicID: 1, score: 0.8},
		{topicID: 2, score: 0.6},
		{topicID: 3, score: 0.9},
	}
	selectedIDs := []int64{1, 3}
	reasons := map[int64]string{
		1: "Direct match to query",
		3: "Related context",
	}

	result := filterMatchesByReranker(matches, selectedIDs, reasons)

	assert.Len(t, result, 2)
	assert.Equal(t, int64(1), result[0].topicID)
	assert.Equal(t, "Direct match to query", result[0].reason)
	assert.Equal(t, int64(3), result[1].topicID)
	assert.Equal(t, "Related context", result[1].reason)
}

// TestFilterMatchesByReranker_NoReason tests filtering when no reason provided.
func TestFilterMatchesByReranker_NoReason(t *testing.T) {
	matches := []topicCandidate{
		{topicID: 1, score: 0.8},
		{topicID: 2, score: 0.6},
	}
	selectedIDs := []int64{1}
	reasons := map[int64]string{} // No reasons

	result := filterMatchesByReranker(matches, selectedIDs, reasons)

	assert.Len(t, result, 1)
	assert.Equal(t, int64(1), result[0].topicID)
	assert.Empty(t, result[0].reason, "Reason should be empty when not provided")
}

// TestFilterMatchesByReranker_EmptySelection tests with empty selection.
func TestFilterMatchesByReranker_EmptySelection(t *testing.T) {
	matches := []topicCandidate{
		{topicID: 1, score: 0.8},
		{topicID: 2, score: 0.6},
	}
	selectedIDs := []int64{}
	reasons := map[int64]string{}

	result := filterMatchesByReranker(matches, selectedIDs, reasons)

	assert.Empty(t, result)
}

// TestExecuteReranker_AgentNil tests error when agent is nil.
func TestExecuteReranker_AgentNil(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.Agents.Reranker.Enabled = true

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

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

	ctx := context.Background()
	input := rerankerInput{}

	_, err = svc.executeReranker(ctx, 123, input)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reranker agent not configured")
}

// TestBuildRetrievalResult tests building RetrievalResult from components.
func TestBuildRetrievalResult(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	topicMap := map[int64]storage.Topic{
		1: {ID: 1, UserID: userID, Summary: "Test Topic"},
		2: {ID: 2, UserID: userID, Summary: "Another Topic"},
	}

	messages := []storage.Message{
		{ID: 10, Role: "user", Content: "Hello"},
	}

	// Setup expectations for loading messages
	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return(messages, nil).Once()
	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(2)).Return(messages, nil).Once()

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

	topicMatches := []topicCandidate{
		{topicID: 1, score: 0.8, reason: "Direct match"},
		{topicID: 2, score: 0.6},
	}

	// Test with useReranker=true
	rerankerOut := &rerankerOutput{
		selectedTopicIDs:    []int64{1},
		topicReasons:        map[int64]string{1: "Direct match"},
		selectedArtifactIDs: []int64{},
	}

	result, err := svc.buildRetrievalResult(
		context.Background(),
		userID,
		topicMatches,
		topicMap,
		rerankerOut,
		nil,  // No artifact candidates
		true, // useReranker
	)

	assert.NoError(t, err)
	assert.Len(t, result.Topics, 1)
	assert.Equal(t, int64(1), result.Topics[0].Topic.ID)
	assert.Equal(t, "Direct match", result.Topics[0].Reason)

	mockStore.AssertExpectations(t)
}

// TestBuildRetrievalResult_WithoutReranker tests building result without reranker.
func TestBuildRetrievalResult_WithoutReranker(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.RAG.RetrievedTopicsCount = 2

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	topicMap := map[int64]storage.Topic{
		1: {ID: 1, UserID: userID, Summary: "Test Topic"},
		2: {ID: 2, UserID: userID, Summary: "Another Topic"},
		3: {ID: 3, UserID: userID, Summary: "Third Topic"},
	}

	messages1 := []storage.Message{
		{ID: 10, Role: "user", Content: "Hello"},
	}
	messages2 := []storage.Message{
		{ID: 11, Role: "user", Content: "Hello 2"},
	}
	messages3 := []storage.Message{
		{ID: 12, Role: "user", Content: "Hello 3"},
	}

	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return(messages1, nil).Once()
	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(2)).Return(messages2, nil).Once()
	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(3)).Return(messages3, nil).Once()

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

	topicMatches := []topicCandidate{
		{topicID: 1, score: 0.8},
		{topicID: 2, score: 0.6},
		{topicID: 3, score: 0.7},
	}

	// Test with useReranker=false (legacy behavior)
	result, err := svc.buildRetrievalResult(
		context.Background(),
		userID,
		topicMatches,
		topicMap,
		nil,   // No reranker output
		nil,   // No artifact candidates
		false, // useReranker=false
	)

	assert.NoError(t, err)
	// buildRetrievalResult doesn't limit topics - that's done by caller
	// With unique messages, all 3 topics are included
	assert.Len(t, result.Topics, 3)
}

// TestSearchPeopleAndArtifacts tests searching for people and artifacts.
func TestSearchPeopleAndArtifacts(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Artifacts.Enabled = true
	cfg.Agents.Reranker.Enabled = true

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockArtifactRepo := new(testutil.MockStorage)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	// Setup people search expectations
	people := []storage.Person{
		{ID: 100, DisplayName: "Alice"},
	}
	mockStore.On("GetPeopleByIDs", userID, mock.Anything).Return(people, nil).Once()

	// Setup artifact search expectations
	artifacts := []storage.Artifact{
		{ID: 5, FileType: "pdf", OriginalName: "doc.pdf"},
	}
	mockArtifactRepo.On("GetArtifactsByIDs", userID, []int64{5}).Return(artifacts, nil).Once()

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
	svc.SetPeopleRepository(mockStore)
	svc.SetArtifactRepository(mockArtifactRepo)

	// Setup people vectors
	svc.mu.Lock()
	svc.peopleVectors[userID] = []PersonVectorItem{
		{PersonID: 100, Embedding: []float32{0.5, 0.5}},
	}
	svc.artifactVectors[userID] = []ArtifactVectorItem{
		{ArtifactID: 5, UserID: userID, Embedding: []float32{0.6, 0.4}},
	}
	svc.mu.Unlock()

	embedding := []float32{0.1, 0.2}

	peopleCandidates, artifactCandidates := svc.searchPeopleAndArtifacts(
		context.Background(),
		userID,
		embedding,
		0.5, // threshold
	)

	// Should return people candidates
	assert.NotEmpty(t, peopleCandidates)
	assert.Len(t, peopleCandidates, 1)
	assert.Equal(t, int64(100), peopleCandidates[0].PersonID)

	// Should return artifact candidates
	assert.NotEmpty(t, artifactCandidates)
	assert.Len(t, artifactCandidates, 1)
	assert.Equal(t, int64(5), artifactCandidates[0].ArtifactID)

	mockStore.AssertExpectations(t)
	mockArtifactRepo.AssertExpectations(t)
}

// TestSearchPeopleAndArtifacts_Disabled tests when features are disabled.
func TestSearchPeopleAndArtifacts_Disabled(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Artifacts.Enabled = false
	cfg.Agents.Reranker.Enabled = false

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

	embedding := []float32{0.1, 0.2}

	peopleCandidates, artifactCandidates := svc.searchPeopleAndArtifacts(
		context.Background(),
		123,
		embedding,
		0.5,
	)

	// Should return empty when disabled
	assert.Empty(t, peopleCandidates)
	assert.Empty(t, artifactCandidates)
}

// TestFormatSessionMessages_Omission tests message omission when >10 messages.
func TestFormatSessionMessages_Omission(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()

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

	// Create 12 messages
	history := make([]storage.Message, 12)
	for i := 0; i < 12; i++ {
		history[i] = storage.Message{
			Role:    "user",
			Content: "Message",
		}
	}

	formatted := svc.formatSessionMessages(history)

	// Should include omission marker - with 12 messages, 1 is omitted
	assert.Contains(t, formatted, "omitted")
}

// TestVectorItemMethods tests the GetID and GetEmbedding methods for vector items.
func TestVectorItemMethods(t *testing.T) {
	// Test TopicVectorItem
	topicItem := TopicVectorItem{TopicID: 42, Embedding: []float32{0.1, 0.2}}
	assert.Equal(t, int64(42), topicItem.GetID())
	assert.Equal(t, []float32{0.1, 0.2}, topicItem.GetEmbedding())

	// Test FactVectorItem
	factItem := FactVectorItem{FactID: 123, Embedding: []float32{0.3, 0.4}}
	assert.Equal(t, int64(123), factItem.GetID())
	assert.Equal(t, []float32{0.3, 0.4}, factItem.GetEmbedding())

	// Test PersonVectorItem
	personItem := PersonVectorItem{PersonID: 456, Embedding: []float32{0.5, 0.6}}
	assert.Equal(t, int64(456), personItem.GetID())
	assert.Equal(t, []float32{0.5, 0.6}, personItem.GetEmbedding())

	// Test ArtifactVectorItem
	artifactItem := ArtifactVectorItem{ArtifactID: 789, UserID: 999, Embedding: []float32{0.7, 0.8}}
	assert.Equal(t, int64(789), artifactItem.GetID())
	assert.Equal(t, []float32{0.7, 0.8}, artifactItem.GetEmbedding())
}

// TestFormatUserProfileFormats tests profile formatting functions.
func TestFormatUserProfileFormats(t *testing.T) {
	facts := []storage.Fact{
		{ID: 1, Content: "User is a developer", Relation: "identity", Importance: 90},
		{ID: 2, Content: "Lives in NYC", Relation: "location", Importance: 70},
		{ID: 3, Content: "Likes coffee", Relation: "preference", Importance: 50},
	}

	// Test FormatUserProfile (wraps storage function)
	result := FormatUserProfile(facts)
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "developer")

	// Test FormatUserProfileCompact (wraps storage function)
	compactResult := FormatUserProfileCompact(facts)
	assert.NotEmpty(t, compactResult)
	// Compact format should not contain fact IDs
	// The exact format depends on storage.FormatUserProfileCompact implementation

	// Test FilterProfileFacts (wraps storage function)
	profileFacts := FilterProfileFacts(facts)
	assert.NotEmpty(t, profileFacts)
	// Should include identity and high-importance facts
	hasIdentity := false
	for _, f := range profileFacts {
		if f.Relation == "identity" || f.Importance >= 90 {
			hasIdentity = true
			break
		}
	}
	assert.True(t, hasIdentity, "Should include identity or high-importance facts")
}

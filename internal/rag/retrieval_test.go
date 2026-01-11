package rag

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/runixer/laplaced/internal/agent"
	agenttesting "github.com/runixer/laplaced/internal/agent/testing"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestRetrieve_TopicsGrouping(t *testing.T) {
	// 1. Setup
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.RAG.SimilarityThreshold = 0.5
	cfg.RAG.RetrievedTopicsCount = 5
	cfg.Embedding.Model = "test-model"
	cfg.Agents.Enricher.Model = "test-model"
	cfg.Agents.Default.Model = "test-model"

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	// User ID
	userID := int64(123)

	// 2. Data
	// Topics
	topicA := storage.Topic{
		ID:         1,
		UserID:     userID,
		Summary:    "Topic A",
		StartMsgID: 10,
		EndMsgID:   12,
		Embedding:  []float32{1.0, 0.0, 0.0}, // Matches [1,0,0] perfectly
	}
	topicB := storage.Topic{
		ID:         2,
		UserID:     userID,
		Summary:    "Topic B",
		StartMsgID: 20,
		EndMsgID:   21,
		Embedding:  []float32{0.0, 1.0, 0.0}, // Matches [1,0,0] poorly (0.0)
	}
	topicC := storage.Topic{
		ID:         3,
		UserID:     userID,
		Summary:    "Topic C",
		StartMsgID: 30,
		EndMsgID:   32,
		Embedding:  []float32{0.9, 0.1, 0.0}, // Matches [1,0,0] well (0.9ish)
	}

	topics := []storage.Topic{topicA, topicB, topicC}

	// Messages
	msgsA := []storage.Message{
		{ID: 10, Role: "user", Content: "A1"},
		{ID: 11, Role: "assistant", Content: "A2"},
		{ID: 12, Role: "user", Content: "A3"},
	}
	msgsC := []storage.Message{
		{ID: 30, Role: "user", Content: "C1"},
		{ID: 31, Role: "assistant", Content: "C2"},
		{ID: 32, Role: "user", Content: "C3"},
	}

	// 3. Expectations

	// Init: loadTopicVectors calls GetAllTopics
	mockStore.On("GetAllTopics").Return(topics, nil)
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)

	// Background loops expectations
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil).Maybe()
	mockStore.On("GetTopicsPendingFacts", userID).Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetTopics", userID).Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetMergeCandidates", userID).Return([]storage.MergeCandidate{}, nil).Maybe()
	mockStore.On("GetAllUsers").Return([]storage.User{}, nil).Maybe()

	// Mock enricher agent that returns "Enriched Query"
	mockEnricher := new(agenttesting.MockAgent)
	mockEnricher.On("Type").Return(string(agent.TypeEnricher)).Maybe()
	mockEnricher.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
		Content: "Enriched Query",
	}, nil)

	// CreateEmbeddings for "Enriched Query" -> Returns [1, 0, 0]
	mockClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
		return len(req.Input) > 0 && req.Input[0] == "Enriched Query"
	})).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{
			{Embedding: []float32{1.0, 0.0, 0.0}},
		},
	}, nil)

	// GetTopicsByIDs (called inside Retrieve to fetch matched topics)
	mockStore.On("GetTopicsByIDs", mock.MatchedBy(func(ids []int64) bool {
		// Should request IDs of matching topics (A and C)
		return len(ids) > 0
	})).Return(topics, nil)

	// GetMessagesByTopicID for matching topics
	// Topic A matches (similarity 1.0 > 0.5) - ID=1
	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return(msgsA, nil)
	// Topic B matches (similarity 0.0 < 0.5) -> Should NOT be called
	// Topic C matches (similarity 0.9 > 0.5) - ID=3
	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(3)).Return(msgsC, nil)

	// Create translator with required template keys
	translator := testutil.TestTranslator(t)

	// 4. Run Logic
	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
	svc.SetEnricherAgent(mockEnricher)

	err := svc.Start(context.Background())
	assert.NoError(t, err)

	result, _, err := svc.Retrieve(context.Background(), userID, "Test Query", nil)
	assert.NoError(t, err)

	// 5. Assertions
	assert.NotNil(t, result)
	assert.Len(t, result.Topics, 2, "Should return 2 topics (A and C)")

	// Sort order is by score descending in implementation
	assert.Equal(t, int64(1), result.Topics[0].Topic.ID) // Topic A (1.0)
	assert.Len(t, result.Topics[0].Messages, 3)

	assert.Equal(t, int64(3), result.Topics[1].Topic.ID) // Topic C (0.9)
	assert.Len(t, result.Topics[1].Messages, 3)

	mockStore.AssertExpectations(t)
	mockClient.AssertExpectations(t)
	mockEnricher.AssertExpectations(t)
}

func TestRetrieveFacts(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.Embedding.Model = "test-model"
	cfg.RAG.MinSafetyThreshold = 0.5

	userID := int64(123)

	t.Run("RAG disabled", func(t *testing.T) {
		disabledCfg := &config.Config{}
		disabledCfg.RAG.Enabled = false

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		// Init mocks
		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, disabledCfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, disabledCfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		facts, err := svc.RetrieveFacts(context.Background(), userID, "query")
		assert.NoError(t, err)
		assert.Nil(t, facts)
	})

	t.Run("success with matching facts", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		// Facts for user
		facts := []storage.Fact{
			{ID: 1, UserID: userID, Content: "User likes coffee", Embedding: []float32{0.9, 0.1, 0.0}},
			{ID: 2, UserID: userID, Content: "User works at Google", Embedding: []float32{0.0, 0.9, 0.1}},
		}

		// Init mocks
		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return(facts, nil)

		// Query embedding
		mockClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
			return len(req.Input) > 0 && req.Input[0] == "coffee query"
		})).Return(openrouter.EmbeddingResponse{
			Data: []openrouter.EmbeddingObject{{Embedding: []float32{1.0, 0.0, 0.0}}},
		}, nil)

		// GetFacts for fetching full fact data
		mockStore.On("GetFacts", userID).Return(facts, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		result, err := svc.RetrieveFacts(context.Background(), userID, "coffee query")
		assert.NoError(t, err)
		// Fact 1 should match better (0.9 similarity vs 0.0)
		assert.Len(t, result, 1)
		assert.Equal(t, "User likes coffee", result[0].Content)
	})

	t.Run("embedding error", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{}, assert.AnError)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		_, err := svc.RetrieveFacts(context.Background(), userID, "query")
		assert.Error(t, err)
	})

	t.Run("empty embedding response", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(
			openrouter.EmbeddingResponse{Data: []openrouter.EmbeddingObject{}}, nil,
		)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		_, err := svc.RetrieveFacts(context.Background(), userID, "query")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no embedding returned")
	})
}

func TestRetrieve_SkipEnrichment(t *testing.T) {
	t.Run("skips enrichment when option is set", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{
				Enabled: true,
			},
			Embedding: config.EmbeddingConfig{Model: "test-model"},
			Agents: config.AgentsConfig{
				Enricher: config.AgentConfig{Model: "query-model"}, // Would normally trigger enrichment
				Default:  config.AgentConfig{Model: "query-model"},
			},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetTopicsByIDs", mock.Anything).Return([]storage.Topic{}, nil)

		// Embedding call for the query
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(
			openrouter.EmbeddingResponse{
				Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2, 0.3}}},
			}, nil,
		)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		opts := &RetrievalOptions{SkipEnrichment: true}
		results, debugInfo, err := svc.Retrieve(context.Background(), 123, "test query", opts)

		assert.NoError(t, err)
		assert.Empty(t, results)                               // No topics loaded
		assert.Equal(t, "test query", debugInfo.EnrichedQuery) // Query should not be enriched
		assert.Empty(t, debugInfo.EnrichmentPrompt)            // No enrichment prompt used
	})
}

func TestRetrieve_NilOptions(t *testing.T) {
	t.Run("handles nil options", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{
				Enabled: true,
			},
			Embedding: config.EmbeddingConfig{Model: "test-model"},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetTopicsByIDs", mock.Anything).Return([]storage.Topic{}, nil)

		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(
			openrouter.EmbeddingResponse{
				Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2, 0.3}}},
			}, nil,
		)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		// Call with nil options - should not panic
		_, _, err := svc.Retrieve(context.Background(), 123, "test query", nil)

		assert.NoError(t, err)
	})
}

func TestEnrichQuery(t *testing.T) {
	t.Run("returns original query when enricher not configured", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)
		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		// Don't set enricher agent - should return original query

		result, prompt, tokens, err := svc.enrichQuery(context.Background(), int64(123), "test query", nil, nil)

		assert.NoError(t, err)
		assert.Equal(t, "test query", result)
		assert.Empty(t, prompt)
		assert.Equal(t, 0, tokens)
	})

	t.Run("handles agent error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Agents: config.AgentsConfig{
				Enricher: config.AgentConfig{Model: "test-model"},
				Default:  config.AgentConfig{Model: "test-model"},
			},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)
		translator := testutil.TestTranslator(t)

		// Mock enricher agent that returns error
		mockEnricher := new(agenttesting.MockAgent)
		mockEnricher.On("Type").Return(string(agent.TypeEnricher)).Maybe()
		mockEnricher.On("Execute", mock.Anything, mock.Anything).Return(nil, assert.AnError)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		svc.SetEnricherAgent(mockEnricher)

		_, _, _, err := svc.enrichQuery(context.Background(), int64(123), "test query", nil, nil)

		assert.Error(t, err)
	})

	t.Run("enriches query successfully", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Agents: config.AgentsConfig{
				Enricher: config.AgentConfig{Model: "test-model"},
				Default:  config.AgentConfig{Model: "test-model"},
			},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)
		translator := testutil.TestTranslator(t)

		// Mock enricher agent that returns enriched query
		mockEnricher := new(agenttesting.MockAgent)
		mockEnricher.On("Type").Return(string(agent.TypeEnricher)).Maybe()
		mockEnricher.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
			Content: "enriched search terms",
			Tokens: agent.TokenUsage{
				Prompt:     10,
				Completion: 5,
			},
		}, nil)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		svc.SetEnricherAgent(mockEnricher)

		history := []storage.Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there"},
		}

		result, _, tokens, err := svc.enrichQuery(context.Background(), int64(123), "test query", history, nil)

		assert.NoError(t, err)
		assert.Equal(t, "enriched search terms", result)
		assert.Equal(t, 15, tokens)
	})
}

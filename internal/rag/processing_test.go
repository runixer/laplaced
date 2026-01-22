package rag

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/splitter"
	agenttesting "github.com/runixer/laplaced/internal/agent/testing"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestProcessingStats_AddChatUsage(t *testing.T) {
	t.Run("adds tokens and cost to empty stats", func(t *testing.T) {
		stats := &ProcessingStats{}
		cost := 0.01

		stats.AddChatUsage(100, 50, &cost)

		assert.Equal(t, 100, stats.PromptTokens)
		assert.Equal(t, 50, stats.CompletionTokens)
		assert.NotNil(t, stats.TotalCost)
		assert.Equal(t, 0.01, *stats.TotalCost)
	})

	t.Run("accumulates tokens and cost", func(t *testing.T) {
		initialCost := 0.01
		stats := &ProcessingStats{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalCost:        &initialCost,
		}
		additionalCost := 0.02

		stats.AddChatUsage(200, 100, &additionalCost)

		assert.Equal(t, 300, stats.PromptTokens)
		assert.Equal(t, 150, stats.CompletionTokens)
		assert.Equal(t, 0.03, *stats.TotalCost)
	})

	t.Run("handles nil cost", func(t *testing.T) {
		stats := &ProcessingStats{}

		stats.AddChatUsage(100, 50, nil)

		assert.Equal(t, 100, stats.PromptTokens)
		assert.Equal(t, 50, stats.CompletionTokens)
		assert.Nil(t, stats.TotalCost)
	})

	t.Run("adds cost when stats cost is nil", func(t *testing.T) {
		stats := &ProcessingStats{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalCost:        nil,
		}
		cost := 0.05

		stats.AddChatUsage(0, 0, &cost)

		assert.NotNil(t, stats.TotalCost)
		assert.Equal(t, 0.05, *stats.TotalCost)
	})
}

func TestProcessingStats_AddEmbeddingUsage(t *testing.T) {
	t.Run("adds tokens and cost to empty stats", func(t *testing.T) {
		stats := &ProcessingStats{}
		cost := 0.005

		stats.AddEmbeddingUsage(1000, &cost)

		assert.Equal(t, 1000, stats.EmbeddingTokens)
		assert.NotNil(t, stats.TotalCost)
		assert.Equal(t, 0.005, *stats.TotalCost)
	})

	t.Run("accumulates tokens and cost", func(t *testing.T) {
		initialCost := 0.01
		stats := &ProcessingStats{
			EmbeddingTokens: 500,
			TotalCost:       &initialCost,
		}
		additionalCost := 0.005

		stats.AddEmbeddingUsage(1000, &additionalCost)

		assert.Equal(t, 1500, stats.EmbeddingTokens)
		assert.Equal(t, 0.015, *stats.TotalCost)
	})

	t.Run("handles nil cost", func(t *testing.T) {
		stats := &ProcessingStats{}

		stats.AddEmbeddingUsage(1000, nil)

		assert.Equal(t, 1000, stats.EmbeddingTokens)
		assert.Nil(t, stats.TotalCost)
	})

	t.Run("combined chat and embedding usage", func(t *testing.T) {
		stats := &ProcessingStats{}
		chatCost := 0.01
		embCost := 0.005

		stats.AddChatUsage(100, 50, &chatCost)
		stats.AddEmbeddingUsage(1000, &embCost)

		assert.Equal(t, 100, stats.PromptTokens)
		assert.Equal(t, 50, stats.CompletionTokens)
		assert.Equal(t, 1000, stats.EmbeddingTokens)
		assert.Equal(t, 0.015, *stats.TotalCost)
	})
}

func TestProcessChunk(t *testing.T) {
	t.Run("returns nil for empty chunk", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		err := svc.processChunk(context.Background(), 123, []storage.Message{})

		assert.NoError(t, err)
	})
}

func TestProcessChunkWithStats(t *testing.T) {
	t.Run("empty chunk returns nil", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)
		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		stats := &ProcessingStats{}
		topicIDs, err := svc.processChunkWithStats(context.Background(), 123, []storage.Message{}, stats)

		assert.NoError(t, err)
		assert.Nil(t, topicIDs)
	})

	t.Run("extractTopics error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG:       config.RAGConfig{Enabled: true},
			Embedding: config.EmbeddingConfig{Model: "test-embed"},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)
		translator := testutil.TestTranslator(t)

		// Mock splitter agent that returns error
		mockSplitter := new(agenttesting.MockAgent)
		mockSplitter.On("Type").Return(string(agent.TypeSplitter)).Maybe()
		mockSplitter.On("Execute", mock.Anything, mock.Anything).Return(nil, assert.AnError)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		svc.SetSplitterAgent(mockSplitter)

		chunk := []storage.Message{
			{ID: 1, Role: "user", Content: "Hello", CreatedAt: time.Now()},
		}
		stats := &ProcessingStats{}
		_, err := svc.processChunkWithStats(context.Background(), 123, chunk, stats)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "extract topics")
	})

	t.Run("successful processing with topics", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG:       config.RAGConfig{Enabled: true},
			Embedding: config.EmbeddingConfig{Model: "test-embed"},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)
		translator := testutil.TestTranslator(t)

		now := time.Now()
		chunk := []storage.Message{
			{ID: 1, Role: "user", Content: "Hello", CreatedAt: now},
			{ID: 2, Role: "assistant", Content: "Hi there", CreatedAt: now},
		}

		// Mock splitter agent that returns topics
		mockSplitter := new(agenttesting.MockAgent)
		mockSplitter.On("Type").Return(string(agent.TypeSplitter)).Maybe()
		mockSplitter.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
			Structured: &splitter.Result{
				Topics: []splitter.ExtractedTopic{
					{Summary: "Test topic", StartMsgID: 1, EndMsgID: 2},
				},
			},
			Tokens: agent.TokenUsage{
				Prompt:     10,
				Completion: 5,
				Total:      15,
			},
		}, nil)

		// Mock embeddings
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
			Data: []openrouter.EmbeddingObject{
				{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
			},
			Usage: struct {
				PromptTokens int      `json:"prompt_tokens"`
				TotalTokens  int      `json:"total_tokens"`
				Cost         *float64 `json:"cost,omitempty"`
			}{TotalTokens: 10},
		}, nil)

		// Mock AddTopic
		mockStore.On("AddTopic", mock.Anything).Return(int64(1), nil)

		// Mock for LoadNewVectors
		mockStore.On("GetTopicsAfterID", mock.Anything).Return([]storage.Topic{}, nil).Maybe()
		mockStore.On("GetFactsAfterID", mock.Anything).Return([]storage.Fact{}, nil).Maybe()

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		svc.SetSplitterAgent(mockSplitter)

		stats := &ProcessingStats{}
		topicIDs, err := svc.processChunkWithStats(context.Background(), 123, chunk, stats)

		assert.NoError(t, err)
		assert.Len(t, topicIDs, 1)
		assert.Greater(t, stats.PromptTokens, 0)
		mockClient.AssertExpectations(t)
		mockStore.AssertExpectations(t)
	})

	t.Run("embedding error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG:       config.RAGConfig{Enabled: true},
			Embedding: config.EmbeddingConfig{Model: "test-embed"},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)
		translator := testutil.TestTranslator(t)

		now := time.Now()
		chunk := []storage.Message{
			{ID: 1, Role: "user", Content: "Hello", CreatedAt: now},
		}

		// Mock splitter agent that returns topics
		mockSplitter := new(agenttesting.MockAgent)
		mockSplitter.On("Type").Return(string(agent.TypeSplitter)).Maybe()
		mockSplitter.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
			Structured: &splitter.Result{
				Topics: []splitter.ExtractedTopic{
					{Summary: "Test topic", StartMsgID: 1, EndMsgID: 1},
				},
			},
			Tokens: agent.TokenUsage{
				Prompt:     10,
				Completion: 5,
				Total:      15,
			},
		}, nil)

		// Mock embeddings error
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{}, assert.AnError)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		svc.SetSplitterAgent(mockSplitter)

		stats := &ProcessingStats{}
		_, err := svc.processChunkWithStats(context.Background(), 123, chunk, stats)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "create embeddings")
	})
}

func TestProcessTopicChunking(t *testing.T) {
	t.Run("handles GetUnprocessedMessages error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{}, assert.AnError)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processTopicChunking(context.Background(), 123)

		mockStore.AssertExpectations(t)
	})

	t.Run("returns early when no unprocessed messages", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{}, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processTopicChunking(context.Background(), 123)

		mockStore.AssertExpectations(t)
	})

	t.Run("uses default chunk interval on invalid config", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{
				Enabled:       true,
				ChunkInterval: "invalid-duration",
			},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{}, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		// Should not panic with invalid duration
		svc.processTopicChunking(context.Background(), 123)

		mockStore.AssertExpectations(t)
	})
}

func TestProcessFactExtraction(t *testing.T) {
	t.Run("handles context cancellation", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		// Create cancelled context
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// Should exit immediately due to cancelled context
		svc.processFactExtraction(ctx)

		// No mock calls expected since context was cancelled
	})

	t.Run("handles GetTopicsPendingFacts error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetTopicsPendingFacts", int64(123)).Return([]storage.Topic{}, assert.AnError)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processFactExtraction(context.Background())

		mockStore.AssertExpectations(t)
	})

	t.Run("skips topics not checked for consolidation", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		// Return topic that hasn't been checked for consolidation
		mockStore.On("GetTopicsPendingFacts", int64(123)).Return([]storage.Topic{
			{ID: 1, UserID: 123, ConsolidationChecked: false},
		}, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processFactExtraction(context.Background())

		mockStore.AssertExpectations(t)
		// Verify no further processing happened (no GetMessagesInRange call)
	})

	t.Run("handles empty messages for topic", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetTopicsPendingFacts", int64(123)).Return([]storage.Topic{
			{ID: 1, UserID: 123, ConsolidationChecked: true, StartMsgID: 1, EndMsgID: 10},
		}, nil)
		mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return([]storage.Message{}, nil)
		mockStore.On("SetTopicFactsExtracted", int64(123), int64(1), true).Return(nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processFactExtraction(context.Background())

		mockStore.AssertExpectations(t)
	})
}

func TestProcessConsolidation(t *testing.T) {
	t.Run("handles context cancellation", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		// Create cancelled context
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// Should exit immediately due to cancelled context
		svc.processConsolidation(ctx)

		// No mock calls expected since context was cancelled
	})

	t.Run("handles findMergeCandidates error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		// Return error for GetMergeCandidates
		mockStore.On("GetMergeCandidates", int64(123)).Return([]storage.MergeCandidate{}, assert.AnError)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processConsolidation(context.Background())

		mockStore.AssertExpectations(t)
	})

	t.Run("skips when no merge candidates", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		// Return empty merge candidates
		mockStore.On("GetMergeCandidates", int64(123)).Return([]storage.MergeCandidate{}, nil)
		// Return empty pending topics (for orphan check)
		mockStore.On("GetTopicsPendingFacts", int64(123)).Return([]storage.Topic{}, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processConsolidation(context.Background())

		mockStore.AssertExpectations(t)
	})
}

func TestProcessAllUsers(t *testing.T) {
	t.Run("handles context cancellation", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123, 456}},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		svc.processAllUsers(ctx)
		// Should return immediately without processing any users
	})

	t.Run("processes all users", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		// Mock GetUnprocessedMessages returning empty (no work to do)
		mockStore.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{}, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processAllUsers(context.Background())

		mockStore.AssertExpectations(t)
	})
}

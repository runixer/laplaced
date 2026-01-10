package rag

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/merger"
	agenttesting "github.com/runixer/laplaced/internal/agent/testing"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestFindMergeCandidates(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.RAG.ConsolidationSimilarityThreshold = 0.75

	userID := int64(123)

	t.Run("filters out low similarity candidates", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		// Candidate with low similarity
		candidates := []storage.MergeCandidate{
			{
				Topic1: storage.Topic{ID: 1, UserID: userID, Embedding: []float32{1.0, 0.0, 0.0}},
				Topic2: storage.Topic{ID: 2, UserID: userID, Embedding: []float32{0.0, 1.0, 0.0}}, // 0.0 similarity
			},
		}

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetMergeCandidates", userID).Return(candidates, nil)
		mockStore.On("SetTopicConsolidationChecked", int64(1), true).Return(nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		result, err := svc.findMergeCandidates(userID)
		assert.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("returns high similarity candidates", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		// Candidate with high similarity
		candidates := []storage.MergeCandidate{
			{
				Topic1: storage.Topic{ID: 1, UserID: userID, Embedding: []float32{1.0, 0.0, 0.0}},
				Topic2: storage.Topic{ID: 2, UserID: userID, Embedding: []float32{0.95, 0.05, 0.0}}, // ~0.95 similarity
			},
		}

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetMergeCandidates", userID).Return(candidates, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		result, err := svc.findMergeCandidates(userID)
		assert.NoError(t, err)
		assert.Len(t, result, 1)
	})

	t.Run("skips candidates without embeddings", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		// Candidate without embeddings
		candidates := []storage.MergeCandidate{
			{
				Topic1: storage.Topic{ID: 1, UserID: userID},
				Topic2: storage.Topic{ID: 2, UserID: userID},
			},
		}

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetMergeCandidates", userID).Return(candidates, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		result, err := svc.findMergeCandidates(userID)
		assert.NoError(t, err)
		assert.Empty(t, result)
	})
}

func TestVerifyMerge(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.Agents.Merger.Model = "test-model"
	cfg.Agents.Default.Model = "test-model"

	t.Run("should merge", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)

		// Mock merger agent that returns should_merge: true
		mockMerger := new(agenttesting.MockAgent)
		mockMerger.On("Type").Return(string(agent.TypeMerger)).Maybe()
		mockMerger.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
			Structured: &merger.Result{
				ShouldMerge: true,
				NewSummary:  "Combined summary",
			},
		}, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		svc.SetMergerAgent(mockMerger)
		_ = svc.Start(context.Background())

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{Summary: "Topic 1"},
			Topic2: storage.Topic{Summary: "Topic 2"},
		}

		shouldMerge, newSummary, _, err := svc.verifyMerge(context.Background(), candidate)
		assert.NoError(t, err)
		assert.True(t, shouldMerge)
		assert.Equal(t, "Combined summary", newSummary)
	})

	t.Run("should not merge", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)

		// Mock merger agent that returns should_merge: false
		mockMerger := new(agenttesting.MockAgent)
		mockMerger.On("Type").Return(string(agent.TypeMerger)).Maybe()
		mockMerger.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
			Structured: &merger.Result{
				ShouldMerge: false,
			},
		}, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		svc.SetMergerAgent(mockMerger)
		_ = svc.Start(context.Background())

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{Summary: "Topic 1"},
			Topic2: storage.Topic{Summary: "Topic 2"},
		}

		shouldMerge, _, _, err := svc.verifyMerge(context.Background(), candidate)
		assert.NoError(t, err)
		assert.False(t, shouldMerge)
	})

	t.Run("agent error", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)

		// Mock merger agent that returns error
		mockMerger := new(agenttesting.MockAgent)
		mockMerger.On("Type").Return(string(agent.TypeMerger)).Maybe()
		mockMerger.On("Execute", mock.Anything, mock.Anything).Return(nil, assert.AnError)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		svc.SetMergerAgent(mockMerger)
		_ = svc.Start(context.Background())

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{Summary: "Topic 1"},
			Topic2: storage.Topic{Summary: "Topic 2"},
		}

		_, _, _, err := svc.verifyMerge(context.Background(), candidate)
		assert.Error(t, err)
	})
}

func TestMergeTopics(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.Embedding.Model = "test-embedding"

	t.Run("successful merge", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)

		// GetMessagesByTopicID for building embedding input (called separately for each topic)
		mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return([]storage.Message{
			{ID: 1, Role: "user", Content: "Hello"},
			{ID: 5, Role: "assistant", Content: "Hi there"},
		}, nil)
		mockStore.On("GetMessagesByTopicID", mock.Anything, int64(2)).Return([]storage.Message{
			{ID: 11, Role: "user", Content: "More"},
			{ID: 15, Role: "assistant", Content: "Content"},
		}, nil)

		// CreateEmbeddings for new topic
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
			Data: []openrouter.EmbeddingObject{
				{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
			},
		}, nil)

		// AddTopicWithoutMessageUpdate for new merged topic (preserves intermediate topics)
		mockStore.On("AddTopicWithoutMessageUpdate", mock.Anything).Return(int64(100), nil)

		// UpdateMessagesTopicInRange for each old topic's messages (now includes userID)
		mockStore.On("UpdateMessagesTopicInRange", mock.Anything, int64(123), int64(1), int64(10), int64(100)).Return(nil)
		mockStore.On("UpdateMessagesTopicInRange", mock.Anything, int64(123), int64(11), int64(20), int64(100)).Return(nil)

		// Update fact references
		mockStore.On("UpdateFactTopic", int64(1), int64(100)).Return(nil)
		mockStore.On("UpdateFactTopic", int64(2), int64(100)).Return(nil)

		// Update fact history references
		mockStore.On("UpdateFactHistoryTopic", int64(1), int64(100)).Return(nil)
		mockStore.On("UpdateFactHistoryTopic", int64(2), int64(100)).Return(nil)

		// Delete old topics
		mockStore.On("DeleteTopicCascade", int64(1)).Return(nil)
		mockStore.On("DeleteTopicCascade", int64(2)).Return(nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 10},
			Topic2: storage.Topic{ID: 2, UserID: 123, StartMsgID: 11, EndMsgID: 20},
		}

		_, err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
		assert.NoError(t, err)
		mockStore.AssertExpectations(t)
	})

	t.Run("GetMessagesByTopicID error", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return([]storage.Message{}, assert.AnError)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 10},
			Topic2: storage.Topic{ID: 2, UserID: 123, StartMsgID: 11, EndMsgID: 20},
		}

		_, err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
		assert.Error(t, err)
	})

	t.Run("CreateEmbeddings error", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return([]storage.Message{
			{ID: 1, Role: "user", Content: "Hello"},
		}, nil)
		mockStore.On("GetMessagesByTopicID", mock.Anything, int64(2)).Return([]storage.Message{
			{ID: 11, Role: "user", Content: "World"},
		}, nil)
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{}, assert.AnError)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 10},
			Topic2: storage.Topic{ID: 2, UserID: 123, StartMsgID: 11, EndMsgID: 20},
		}

		_, err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
		assert.Error(t, err)
	})

	t.Run("no embedding returned", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return([]storage.Message{
			{ID: 1, Role: "user", Content: "Hello"},
		}, nil)
		mockStore.On("GetMessagesByTopicID", mock.Anything, int64(2)).Return([]storage.Message{
			{ID: 11, Role: "user", Content: "World"},
		}, nil)
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
			Data: []openrouter.EmbeddingObject{},
		}, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 10},
			Topic2: storage.Topic{ID: 2, UserID: 123, StartMsgID: 11, EndMsgID: 20},
		}

		_, err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no embedding returned")
	})
}

func TestRunConsolidationSync(t *testing.T) {
	t.Run("no merge candidates", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true, SimilarityThreshold: 0.85},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		// No candidates
		mockStore.On("GetMergeCandidates", int64(123), mock.Anything).Return([]storage.MergeCandidate{}, nil)
		mockStore.On("SetTopicConsolidationChecked", mock.Anything, true).Return(nil).Maybe()

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		stats := &ProcessingStats{}
		count := svc.runConsolidationSync(context.Background(), 123, []int64{1, 2}, stats)

		assert.Equal(t, 0, count)
		mockStore.AssertExpectations(t)
	})

	t.Run("findMergeCandidates error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true, SimilarityThreshold: 0.85},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetMergeCandidates", int64(123), mock.Anything).Return([]storage.MergeCandidate{}, assert.AnError)
		mockStore.On("SetTopicConsolidationChecked", mock.Anything, true).Return(nil).Maybe()

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		stats := &ProcessingStats{}
		count := svc.runConsolidationSync(context.Background(), 123, []int64{1}, stats)

		assert.Equal(t, 0, count)
	})

	t.Run("context cancellation during verification", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true, SimilarityThreshold: 0.85},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		topic1 := storage.Topic{ID: 1, UserID: 123}
		topic2 := storage.Topic{ID: 2, UserID: 123}
		candidates := []storage.MergeCandidate{{Topic1: topic1, Topic2: topic2}}

		mockStore.On("GetMergeCandidates", int64(123), mock.Anything).Return(candidates, nil)
		mockStore.On("SetTopicConsolidationChecked", mock.Anything, true).Return(nil).Maybe()

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		// Cancel context immediately
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		stats := &ProcessingStats{}
		count := svc.runConsolidationSync(ctx, 123, []int64{1}, stats)

		assert.Equal(t, 0, count)
	})
}

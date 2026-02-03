package rag

import (
	"context"
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

		mockStore.On("GetMergeCandidates", userID).Return(candidates, nil)
		mockStore.On("SetTopicConsolidationChecked", userID, int64(1), true).Return(nil)

		svc := newTestRAGService(t, mockStore, mockClient, func(cfg *config.Config) {
			cfg.RAG.ConsolidationSimilarityThreshold = 0.75
		})

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

		mockStore.On("GetMergeCandidates", userID).Return(candidates, nil)

		svc := newTestRAGService(t, mockStore, mockClient, func(cfg *config.Config) {
			cfg.RAG.ConsolidationSimilarityThreshold = 0.75
		})

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

		mockStore.On("GetMergeCandidates", userID).Return(candidates, nil)

		svc := newTestRAGService(t, mockStore, mockClient, func(cfg *config.Config) {
			cfg.RAG.ConsolidationSimilarityThreshold = 0.75
		})

		result, err := svc.findMergeCandidates(userID)
		assert.NoError(t, err)
		assert.Empty(t, result)
	})
}

func TestVerifyMerge(t *testing.T) {
	t.Run("should merge", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		// Mock merger agent that returns should_merge: true
		mockMerger := new(agenttesting.MockAgent)
		mockMerger.On("Type").Return(string(agent.TypeMerger)).Maybe()
		mockMerger.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
			Structured: &merger.Result{
				ShouldMerge: true,
				NewSummary:  "Combined summary",
			},
		}, nil)

		svc := newTestRAGServiceWithSetup(t, mockStore, mockClient, func(svc *Service, memSvc *memory.Service) {
			svc.SetMergerAgent(mockMerger)
		})

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

		// Mock merger agent that returns should_merge: false
		mockMerger := new(agenttesting.MockAgent)
		mockMerger.On("Type").Return(string(agent.TypeMerger)).Maybe()
		mockMerger.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
			Structured: &merger.Result{
				ShouldMerge: false,
			},
		}, nil)

		svc := newTestRAGServiceWithSetup(t, mockStore, mockClient, func(svc *Service, memSvc *memory.Service) {
			svc.SetMergerAgent(mockMerger)
		})

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

		// Mock merger agent that returns error
		mockMerger := new(agenttesting.MockAgent)
		mockMerger.On("Type").Return(string(agent.TypeMerger)).Maybe()
		mockMerger.On("Execute", mock.Anything, mock.Anything).Return(nil, assert.AnError)

		svc := newTestRAGServiceWithSetup(t, mockStore, mockClient, func(svc *Service, memSvc *memory.Service) {
			svc.SetMergerAgent(mockMerger)
		})

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{Summary: "Topic 1"},
			Topic2: storage.Topic{Summary: "Topic 2"},
		}

		_, _, _, err := svc.verifyMerge(context.Background(), candidate)
		assert.Error(t, err)
	})
}

func TestMergeTopics(t *testing.T) {
	t.Run("successful merge", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

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
		mockStore.On("UpdateFactsTopic", int64(123), int64(1), int64(100)).Return(nil)
		mockStore.On("UpdateFactsTopic", int64(123), int64(2), int64(100)).Return(nil)

		// Update fact history references
		mockStore.On("UpdateFactHistoryTopic", int64(1), int64(100)).Return(nil)
		mockStore.On("UpdateFactHistoryTopic", int64(2), int64(100)).Return(nil)

		// Delete old topics
		mockStore.On("DeleteTopicCascade", int64(123), int64(1)).Return(nil)
		mockStore.On("DeleteTopicCascade", int64(123), int64(2)).Return(nil)

		svc := newTestRAGService(t, mockStore, mockClient)

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 10},
			Topic2: storage.Topic{ID: 2, UserID: 123, StartMsgID: 11, EndMsgID: 20},
		}

		newTopicID, _, err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
		assert.NoError(t, err)
		assert.Equal(t, int64(100), newTopicID)
		mockStore.AssertExpectations(t)
	})

	t.Run("GetMessagesByTopicID error", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return([]storage.Message{}, assert.AnError)

		svc := newTestRAGService(t, mockStore, mockClient)

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 10},
			Topic2: storage.Topic{ID: 2, UserID: 123, StartMsgID: 11, EndMsgID: 20},
		}

		newTopicID, _, err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
		assert.Error(t, err)
		assert.Equal(t, int64(0), newTopicID)
	})

	t.Run("CreateEmbeddings error", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return([]storage.Message{
			{ID: 1, Role: "user", Content: "Hello"},
		}, nil)
		mockStore.On("GetMessagesByTopicID", mock.Anything, int64(2)).Return([]storage.Message{
			{ID: 11, Role: "user", Content: "World"},
		}, nil)
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{}, assert.AnError)

		svc := newTestRAGService(t, mockStore, mockClient)

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 10},
			Topic2: storage.Topic{ID: 2, UserID: 123, StartMsgID: 11, EndMsgID: 20},
		}

		newTopicID, _, err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
		assert.Error(t, err)
		assert.Equal(t, int64(0), newTopicID)
	})

	t.Run("no embedding returned", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return([]storage.Message{
			{ID: 1, Role: "user", Content: "Hello"},
		}, nil)
		mockStore.On("GetMessagesByTopicID", mock.Anything, int64(2)).Return([]storage.Message{
			{ID: 11, Role: "user", Content: "World"},
		}, nil)
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
			Data: []openrouter.EmbeddingObject{},
		}, nil)

		svc := newTestRAGService(t, mockStore, mockClient)

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 10},
			Topic2: storage.Topic{ID: 2, UserID: 123, StartMsgID: 11, EndMsgID: 20},
		}

		newTopicID, _, err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
		assert.Error(t, err)
		assert.Equal(t, int64(0), newTopicID)
		assert.Contains(t, err.Error(), "no embedding returned")
	})
}

func TestRunConsolidationSync(t *testing.T) {
	t.Run("no merge candidates", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		// No candidates
		mockStore.On("GetMergeCandidates", int64(123), mock.Anything).Return([]storage.MergeCandidate{}, nil)
		mockStore.On("SetTopicConsolidationChecked", int64(123), mock.Anything, true).Return(nil).Maybe()

		svc := newTestRAGService(t, mockStore, mockClient, func(cfg *config.Config) {
			cfg.RAG.SimilarityThreshold = 0.85
		})

		stats := &ProcessingStats{}
		mergedIDs := svc.runConsolidationSync(context.Background(), 123, []int64{1, 2}, stats)

		assert.Empty(t, mergedIDs)
		mockStore.AssertExpectations(t)
	})

	t.Run("findMergeCandidates error", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetMergeCandidates", int64(123), mock.Anything).Return([]storage.MergeCandidate{}, assert.AnError)
		mockStore.On("SetTopicConsolidationChecked", int64(123), mock.Anything, true).Return(nil).Maybe()

		svc := newTestRAGService(t, mockStore, mockClient, func(cfg *config.Config) {
			cfg.RAG.SimilarityThreshold = 0.85
		})

		stats := &ProcessingStats{}
		mergedIDs := svc.runConsolidationSync(context.Background(), 123, []int64{1}, stats)

		assert.Empty(t, mergedIDs)
	})

	t.Run("context cancellation during verification", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		topic1 := storage.Topic{ID: 1, UserID: 123}
		topic2 := storage.Topic{ID: 2, UserID: 123}
		candidates := []storage.MergeCandidate{{Topic1: topic1, Topic2: topic2}}

		mockStore.On("GetMergeCandidates", int64(123), mock.Anything).Return(candidates, nil)
		mockStore.On("SetTopicConsolidationChecked", int64(123), mock.Anything, true).Return(nil).Maybe()

		svc := newTestRAGService(t, mockStore, mockClient, func(cfg *config.Config) {
			cfg.RAG.SimilarityThreshold = 0.85
		})

		// Cancel context immediately
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		stats := &ProcessingStats{}
		mergedIDs := svc.runConsolidationSync(ctx, 123, []int64{1}, stats)

		assert.Empty(t, mergedIDs)
	})
}

func TestMarkOrphanTopicsChecked(t *testing.T) {
	t.Run("handles GetTopicsPendingFacts error gracefully", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		mockStore.On("GetTopicsPendingFacts", int64(123)).Return([]storage.Topic{}, assert.AnError)

		svc := newTestRAGService(t, mockStore, mockClient)
		svc.markOrphanTopicsChecked(123) // Should not panic

		mockStore.AssertExpectations(t)
	})

	t.Run("marks topics with no potential partner", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockOpenRouterClient)

		// Only one topic - automatically an orphan
		pendingTopics := []storage.Topic{
			{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 10, ConsolidationChecked: false},
		}

		mockStore.On("GetTopicsPendingFacts", int64(123)).Return(pendingTopics, nil)
		mockStore.On("SetTopicConsolidationChecked", int64(123), int64(1), true).Return(nil)

		svc := newTestRAGService(t, mockStore, mockClient)
		svc.markOrphanTopicsChecked(123)

		mockStore.AssertExpectations(t)
	})
}

// TestFindMergeCandidates_SizeLimit tests filtering by combined size.
func TestFindMergeCandidates_SizeLimit(t *testing.T) {
	userID := int64(123)
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	// Candidate exceeding size limit (max 50K)
	candidates := []storage.MergeCandidate{
		{
			Topic1: storage.Topic{ID: 1, UserID: userID, Embedding: []float32{1.0, 0.0, 0.0}, SizeChars: 30000},
			Topic2: storage.Topic{ID: 2, UserID: userID, Embedding: []float32{1.0, 0.0, 0.0}, SizeChars: 30000},
		},
	}

	mockStore.On("GetMergeCandidates", userID).Return(candidates, nil)
	mockStore.On("SetTopicConsolidationChecked", userID, int64(1), true).Return(nil).Maybe()
	mockStore.On("SetTopicConsolidationChecked", userID, int64(2), true).Return(nil).Maybe()

	svc := newTestRAGService(t, mockStore, mockClient, func(cfg *config.Config) {
		cfg.RAG.ConsolidationSimilarityThreshold = 0.75
		cfg.RAG.MaxMergedSizeChars = 50000 // 50K limit
	})

	result, err := svc.findMergeCandidates(userID)
	assert.NoError(t, err)
	assert.Empty(t, result) // Both topics filtered due to size
}

// TestFindMergeCandidates_AlreadyChecked tests topics already marked as checked.
func TestFindMergeCandidates_AlreadyChecked(t *testing.T) {
	userID := int64(123)
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	// Topic1 already checked, should still be processed
	candidates := []storage.MergeCandidate{
		{
			Topic1: storage.Topic{ID: 1, UserID: userID, Embedding: []float32{1.0, 0.0, 0.0}, ConsolidationChecked: true},
			Topic2: storage.Topic{ID: 2, UserID: userID, Embedding: []float32{1.0, 0.0, 0.0}, ConsolidationChecked: false},
		},
	}

	mockStore.On("GetMergeCandidates", userID).Return(candidates, nil)

	svc := newTestRAGService(t, mockStore, mockClient, func(cfg *config.Config) {
		cfg.RAG.ConsolidationSimilarityThreshold = 0.75
	})

	result, err := svc.findMergeCandidates(userID)
	assert.NoError(t, err)
	assert.Len(t, result, 1) // Topic2 is still a candidate
}

// TestVerifyMerge_NoMergerAgent tests error when merger agent is not configured.
func TestVerifyMerge_NoMergerAgent(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	svc := newTestRAGService(t, mockStore, mockClient)
	// Don't set merger agent

	candidate := storage.MergeCandidate{
		Topic1: storage.Topic{Summary: "Topic 1"},
		Topic2: storage.Topic{Summary: "Topic 2"},
	}

	shouldMerge, newSummary, _, err := svc.verifyMerge(context.Background(), candidate)
	assert.Error(t, err)
	assert.False(t, shouldMerge)
	assert.Empty(t, newSummary)
	assert.Contains(t, err.Error(), "merger agent not configured")
}

// TestProcessConsolidation_ShouldNotMerge tests the flow when topics should not be merged.
func TestProcessConsolidation_ShouldNotMerge(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	userID := int64(123)
	// Topics with embeddings and small enough to pass size check
	topic1 := storage.Topic{ID: 1, UserID: userID, Summary: "Topic 1", ConsolidationChecked: false, Embedding: []float32{1.0, 0.0, 0.0}, SizeChars: 1000}
	topic2 := storage.Topic{ID: 2, UserID: userID, Summary: "Topic 2", ConsolidationChecked: false, Embedding: []float32{0.95, 0.05, 0.0}, SizeChars: 1000}

	// Mock merger agent that returns should_merge: false
	mockMerger := new(agenttesting.MockAgent)
	mockMerger.On("Type").Return(string(agent.TypeMerger)).Maybe()
	mockMerger.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
		Structured: &merger.Result{
			ShouldMerge: false,
		},
	}, nil)

	// Background loop calls GetUnprocessedMessages
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil).Maybe()
	// Return merge candidates
	mockStore.On("GetMergeCandidates", userID).Return([]storage.MergeCandidate{
		{Topic1: topic1, Topic2: topic2},
	}, nil)
	// Expect SetTopicConsolidationChecked to be called for topic1 (when shouldMerge=false)
	mockStore.On("SetTopicConsolidationChecked", userID, int64(1), true).Return(nil)
	// Return empty pending topics for orphan check
	mockStore.On("GetTopicsPendingFacts", userID).Return([]storage.Topic{}, nil)

	svc := newTestRAGServiceWithSetup(t, mockStore, mockClient, func(svc *Service, memSvc *memory.Service) {
		svc.SetMergerAgent(mockMerger)
		// Set AllowedUserIDs so processConsolidation actually runs
		svc.cfg.Bot.AllowedUserIDs = []int64{userID}
	})

	svc.processConsolidation(context.Background())

	mockStore.AssertExpectations(t)
}

// TestProcessConsolidation_SetTopicCheckedError tests error handling when SetTopicConsolidationChecked fails.
func TestProcessConsolidation_SetTopicCheckedError(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	userID := int64(123)
	// Topics with embeddings and small enough to pass size check
	topic1 := storage.Topic{ID: 1, UserID: userID, Summary: "Topic 1", ConsolidationChecked: false, Embedding: []float32{1.0, 0.0, 0.0}, SizeChars: 1000}
	topic2 := storage.Topic{ID: 2, UserID: userID, Summary: "Topic 2", ConsolidationChecked: false, Embedding: []float32{0.95, 0.05, 0.0}, SizeChars: 1000}

	// Mock merger agent that returns should_merge: false
	mockMerger := new(agenttesting.MockAgent)
	mockMerger.On("Type").Return(string(agent.TypeMerger)).Maybe()
	mockMerger.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
		Structured: &merger.Result{
			ShouldMerge: false,
		},
	}, nil)

	// Background loop calls GetUnprocessedMessages
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil).Maybe()
	// Return merge candidates
	mockStore.On("GetMergeCandidates", userID).Return([]storage.MergeCandidate{
		{Topic1: topic1, Topic2: topic2},
	}, nil)
	// Return error for SetTopicConsolidationChecked
	mockStore.On("SetTopicConsolidationChecked", userID, int64(1), true).Return(assert.AnError)
	// Return empty pending topics for orphan check
	mockStore.On("GetTopicsPendingFacts", userID).Return([]storage.Topic{}, nil)

	svc := newTestRAGServiceWithSetup(t, mockStore, mockClient, func(svc *Service, memSvc *memory.Service) {
		svc.SetMergerAgent(mockMerger)
		// Set AllowedUserIDs so processConsolidation actually runs
		svc.cfg.Bot.AllowedUserIDs = []int64{userID}
	})

	// Should not panic, just log error
	svc.processConsolidation(context.Background())

	mockStore.AssertExpectations(t)
}

// TestProcessConsolidation_VerifyMergeError tests error handling when verifyMerge fails.
func TestProcessConsolidation_VerifyMergeError(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	userID := int64(123)
	// Topics with embeddings and small enough to pass size check
	topic1 := storage.Topic{ID: 1, UserID: userID, Summary: "Topic 1", ConsolidationChecked: false, Embedding: []float32{1.0, 0.0, 0.0}, SizeChars: 1000}
	topic2 := storage.Topic{ID: 2, UserID: userID, Summary: "Topic 2", ConsolidationChecked: false, Embedding: []float32{0.95, 0.05, 0.0}, SizeChars: 1000}

	// Mock merger agent that returns error
	mockMerger := new(agenttesting.MockAgent)
	mockMerger.On("Type").Return(string(agent.TypeMerger)).Maybe()
	mockMerger.On("Execute", mock.Anything, mock.Anything).Return(nil, assert.AnError)

	// Background loop calls GetUnprocessedMessages
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil).Maybe()
	// Return merge candidates
	mockStore.On("GetMergeCandidates", userID).Return([]storage.MergeCandidate{
		{Topic1: topic1, Topic2: topic2},
	}, nil)
	// Return empty pending topics for orphan check
	mockStore.On("GetTopicsPendingFacts", userID).Return([]storage.Topic{}, nil)

	svc := newTestRAGServiceWithSetup(t, mockStore, mockClient, func(svc *Service, memSvc *memory.Service) {
		svc.SetMergerAgent(mockMerger)
		// Set AllowedUserIDs so processConsolidation actually runs
		svc.cfg.Bot.AllowedUserIDs = []int64{userID}
	})

	// Should not panic, just log error and continue
	svc.processConsolidation(context.Background())

	mockStore.AssertExpectations(t)
}

// TestMarkOrphanTopicsChecked_HasPartner tests that a topic with a potential partner is not marked as checked.
func TestMarkOrphanTopicsChecked_HasPartner(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	userID := int64(123)

	// Three topics - first two are close (have partners), third is alone
	pendingTopics := []storage.Topic{
		{ID: 1, UserID: userID, StartMsgID: 1, EndMsgID: 10, ConsolidationChecked: false},
		{ID: 2, UserID: userID, StartMsgID: 15, EndMsgID: 25, ConsolidationChecked: false},   // Gap = 15-10 = 5, < 50
		{ID: 3, UserID: userID, StartMsgID: 100, EndMsgID: 110, ConsolidationChecked: false}, // Far from 2
	}

	mockStore.On("GetTopicsPendingFacts", userID).Return(pendingTopics, nil)
	// Topic 1 has partner (Topic 2)
	// Topic 2 has partner (Topic 1)
	// Topic 3 is orphan (no unchecked topic after it)
	mockStore.On("SetTopicConsolidationChecked", userID, int64(3), true).Return(nil)

	svc := newTestRAGService(t, mockStore, mockClient)
	svc.markOrphanTopicsChecked(userID)

	mockStore.AssertExpectations(t)
}

// TestMarkOrphanTopicsChecked_NoPartner tests that a topic without a potential partner is marked as checked.
func TestMarkOrphanTopicsChecked_NoPartner(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	userID := int64(123)

	// Two topics far apart (gap > DefaultMergeGapThreshold)
	pendingTopics := []storage.Topic{
		{ID: 1, UserID: userID, StartMsgID: 1, EndMsgID: 10, ConsolidationChecked: false},
		{ID: 2, UserID: userID, StartMsgID: 100, EndMsgID: 110, ConsolidationChecked: false}, // Gap = 100-10 = 90, > 50
	}

	mockStore.On("GetTopicsPendingFacts", userID).Return(pendingTopics, nil)
	// Topic 2 should be marked (no unchecked topic after it within 50 messages)
	mockStore.On("SetTopicConsolidationChecked", userID, int64(2), true).Return(nil)

	svc := newTestRAGService(t, mockStore, mockClient)
	svc.markOrphanTopicsChecked(userID)

	mockStore.AssertExpectations(t)
}

// TestMarkOrphanTopicsChecked_LastTopicAlreadyChecked tests that already-checked topics are skipped.
func TestMarkOrphanTopicsChecked_LastTopicAlreadyChecked(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	userID := int64(123)

	// Both topics already checked
	pendingTopics := []storage.Topic{
		{ID: 1, UserID: userID, StartMsgID: 1, EndMsgID: 10, ConsolidationChecked: true},
		{ID: 2, UserID: userID, StartMsgID: 100, EndMsgID: 110, ConsolidationChecked: true},
	}

	mockStore.On("GetTopicsPendingFacts", userID).Return(pendingTopics, nil)

	svc := newTestRAGService(t, mockStore, mockClient)
	svc.markOrphanTopicsChecked(userID)

	// No SetTopicConsolidationChecked calls should be made
	mockStore.AssertNotCalled(t, "SetTopicConsolidationChecked", mock.Anything, mock.Anything, mock.Anything)
	mockStore.AssertExpectations(t)
}

// TestProcessConsolidation_ShouldMerge_Success tests successful merge flow:
// 1. verifyMerge returns shouldMerge=true
// 2. mergeTopics is called and succeeds
// 3. ReloadVectors is triggered (goroutine)
// 4. TriggerConsolidation is called
// 5. Loop breaks after merge (only one candidate processed)
func TestProcessConsolidation_ShouldMerge_Success(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	userID := int64(123)

	// Topics with embeddings and small enough to pass size check
	topic1 := storage.Topic{ID: 1, UserID: userID, Summary: "Topic 1", ConsolidationChecked: false, Embedding: []float32{1.0, 0.0, 0.0}, SizeChars: 1000, StartMsgID: 1, EndMsgID: 5}
	topic2 := storage.Topic{ID: 2, UserID: userID, Summary: "Topic 2", ConsolidationChecked: false, Embedding: []float32{1.0, 0.0, 0.0}, SizeChars: 1000, StartMsgID: 11, EndMsgID: 15}
	topic3 := storage.Topic{ID: 3, UserID: userID, Summary: "Topic 3", ConsolidationChecked: false, Embedding: []float32{1.0, 0.0, 0.0}, SizeChars: 1000, StartMsgID: 21, EndMsgID: 25}

	// Mock merger agent that returns should_merge: true
	mockMerger := new(agenttesting.MockAgent)
	mockMerger.On("Type").Return(string(agent.TypeMerger)).Maybe()
	mockMerger.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
		Structured: &merger.Result{
			ShouldMerge: true,
			NewSummary:  "Merged summary of T1 and T2",
		},
	}, nil)

	// Messages for mergeTopics
	msgs1 := []storage.Message{
		{ID: 1, Role: "user", Content: "Hello"},
		{ID: 5, Role: "assistant", Content: "Hi there"},
	}
	msgs2 := []storage.Message{
		{ID: 11, Role: "user", Content: "More"},
		{ID: 15, Role: "assistant", Content: "Content"},
	}

	// Background loop calls GetUnprocessedMessages
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil).Maybe()

	// Return merge candidates (T1+T2, T2+T3)
	mockStore.On("GetMergeCandidates", userID).Return([]storage.MergeCandidate{
		{Topic1: topic1, Topic2: topic2},
		{Topic1: topic2, Topic2: topic3},
	}, nil)

	// mergeTopics will call GetMessagesByTopicID for each topic
	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return(msgs1, nil).Once()
	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(2)).Return(msgs2, nil).Once()

	// CreateEmbeddings for new merged topic
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{
			{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
		},
	}, nil).Once()

	// AddTopicWithoutMessageUpdate for new merged topic
	mockStore.On("AddTopicWithoutMessageUpdate", mock.Anything).Return(int64(100), nil).Once()

	// UpdateMessagesTopicInRange for each old topic's messages
	mockStore.On("UpdateMessagesTopicInRange", mock.Anything, int64(123), int64(1), int64(5), int64(100)).Return(nil).Once()
	mockStore.On("UpdateMessagesTopicInRange", mock.Anything, int64(123), int64(11), int64(15), int64(100)).Return(nil).Once()

	// Update fact references
	mockStore.On("UpdateFactsTopic", int64(123), int64(1), int64(100)).Return(nil).Maybe()
	mockStore.On("UpdateFactsTopic", int64(123), int64(2), int64(100)).Return(nil).Maybe()

	// Update fact history references
	mockStore.On("UpdateFactHistoryTopic", int64(1), int64(100)).Return(nil).Maybe()
	mockStore.On("UpdateFactHistoryTopic", int64(2), int64(100)).Return(nil).Maybe()

	// Delete old topics
	mockStore.On("DeleteTopicCascade", int64(123), int64(1)).Return(nil).Once()
	mockStore.On("DeleteTopicCascade", int64(123), int64(2)).Return(nil).Once()

	// ReloadVectors will be called (for goroutine)
	mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil).Maybe()
	mockStore.On("GetAllPeople").Return([]storage.Person{}, nil).Maybe()

	// Return empty pending topics for orphan check
	mockStore.On("GetTopicsPendingFacts", userID).Return([]storage.Topic{}, nil)

	// Use NoStart to avoid background consolidation loop interference
	svc := newTestRAGServiceNoStart(t, mockStore, mockClient)
	svc.SetMergerAgent(mockMerger)
	svc.cfg.Bot.AllowedUserIDs = []int64{userID}

	svc.processConsolidation(context.Background())

	// Verify merge was performed: old topics deleted
	mockStore.AssertExpectations(t)

	// Verify that GetMessagesByTopicID was only called for T1 and T2 (not T3)
	// This confirms break happened after merge
	mockStore.AssertNumberOfCalls(t, "GetMessagesByTopicID", 2)
}

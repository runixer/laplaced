package rag

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/archivist"
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

func TestGracefulShutdown(t *testing.T) {
	// 1. Setup
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.RAG.BackfillInterval = "50ms" // Fast interval
	cfg.Bot.AllowedUserIDs = []int64{123}

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)

	// 2. Expectations
	// Start loads vectors
	mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)

	// Background loop calls processAllUsers -> processTopicChunking
	// It calls GetUnprocessedMessages
	msgs := []storage.Message{
		{ID: 1, CreatedAt: time.Now().Add(-10 * time.Hour), Content: "Msg 1"},
		{ID: 2, CreatedAt: time.Now().Add(-9 * time.Hour), Content: "Msg 2"}, // Diff 1h
		// ... we need a gap > 1h (default chunk duration)
		{ID: 3, CreatedAt: time.Now().Add(-1 * time.Hour), Content: "Msg 3"}, // Diff 8h -> Trigger chunk [1, 2]
	}
	mockStore.On("GetUnprocessedMessages", int64(123)).Return(msgs, nil)

	// Fact extraction loop calls GetTopicsPendingFacts
	topic := storage.Topic{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 3, CreatedAt: time.Now(), ConsolidationChecked: true}
	mockStore.On("GetTopicsPendingFacts", int64(123)).Return([]storage.Topic{topic}, nil)
	mockStore.On("SetTopicFactsExtracted", int64(1), true).Return(nil)

	// It calls GetMessagesByTopicID (from processFactExtraction)
	mockStore.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return(msgs, nil)

	// Consolidation loop calls GetTopics
	mockStore.On("GetTopics", int64(123)).Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetMergeCandidates", int64(123)).Return([]storage.MergeCandidate{}, nil).Maybe()

	// Mock AddRAGLog for topic extraction logging
	mockStore.On("AddRAGLog", mock.Anything).Return(nil).Maybe()

	// Embeddings for topic
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2}}},
	}, nil).Maybe()

	// AddTopic for extracted topics
	mockStore.On("AddTopic", mock.Anything).Return(int64(1), nil).Maybe()

	// Mock for LoadNewVectors
	mockStore.On("GetTopicsAfterID", mock.Anything).Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetFactsAfterID", mock.Anything).Return([]storage.Fact{}, nil).Maybe()

	// Mock GetAllUsers for background loops
	mockStore.On("GetAllUsers").Return([]storage.User{}, nil).Maybe()

	// Mock GetFacts for memory service
	mockStore.On("GetFacts", int64(123)).Return([]storage.Fact{}, nil).Maybe()

	// Mock splitter agent with delay to simulate long-running task
	mockSplitter := new(agenttesting.MockAgent)
	mockSplitter.On("Type").Return(string(agent.TypeSplitter)).Maybe()
	mockSplitter.On("Execute", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		// Sleep to simulate long running task
		time.Sleep(500 * time.Millisecond)
	}).Return(&agent.Response{
		Structured: &splitter.Result{
			Topics: []splitter.ExtractedTopic{
				{Summary: "Test topic", StartMsgID: 1, EndMsgID: 2},
			},
		},
	}, nil)

	// Mock archivist agent for memory.ProcessSession
	mockArchivist := new(agenttesting.MockAgent)
	mockArchivist.On("Type").Return(string(agent.TypeArchivist)).Maybe()
	mockArchivist.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
		Structured: &archivist.Result{
			Facts: archivist.FactsResult{
				Added:   []archivist.AddedFact{},
				Updated: []archivist.UpdatedFact{},
				Removed: []archivist.RemovedFact{},
			},
		},
	}, nil).Maybe()

	// Create translator with required template keys
	translator := testutil.TestTranslator(t)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	memSvc.SetArchivistAgent(mockArchivist)

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
	svc.SetSplitterAgent(mockSplitter)

	// Start
	ctx, cancel := context.WithCancel(context.Background())
	err := svc.Start(ctx)
	assert.NoError(t, err)

	// Wait a bit for the background loop to pick up the task and start "sleeping" in the mock
	time.Sleep(100 * time.Millisecond)

	// Now trigger shutdown
	startStop := time.Now()
	cancel()   // Cancel context (simulate SIGINT)
	svc.Stop() // Wait for completion
	duration := time.Since(startStop)

	// Assert that Stop() took at least ~400ms (500ms sleep - 100ms wait)
	assert.Greater(t, duration, 300*time.Millisecond, "Stop() should wait for pending task")

	// Verify that the splitter agent was called
	mockSplitter.AssertCalled(t, "Execute", mock.Anything, mock.Anything)
}

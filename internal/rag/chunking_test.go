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
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestProcessChunk_HallucinatedIDs(t *testing.T) {
	// Setup
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.Embedding.Model = "test-model"
	cfg.Agents.Archivist.Model = "test-model"
	cfg.Agents.Default.Model = "test-model"

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	messages := []storage.Message{
		{ID: 100, Role: "user", Content: "Msg 1", CreatedAt: time.Now()},
		{ID: 102, Role: "user", Content: "Msg 2", CreatedAt: time.Now().Add(time.Minute)},
	}

	// Expectations
	mockStore.On("GetUnprocessedMessages", userID).Return(messages, nil)

	// Mock splitter agent that returns hallucinated ID 101 (doesn't exist in messages)
	mockSplitter := new(agenttesting.MockAgent)
	mockSplitter.On("Type").Return(string(agent.TypeSplitter)).Maybe()
	mockSplitter.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
		Structured: &splitter.Result{
			Topics: []splitter.ExtractedTopic{
				{Summary: "Hallucinated Topic", StartMsgID: 101, EndMsgID: 101},
			},
		},
	}, nil)

	// Mock GetAllTopics/Facts for ReloadVectors (initial load)
	mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil).Maybe()
	// Mock incremental loading methods (called after processChunk)
	mockStore.On("GetTopicsAfterID", mock.Anything).Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetFactsAfterID", mock.Anything).Return([]storage.Fact{}, nil).Maybe()

	// Run
	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, nil, translator)
	svc.SetSplitterAgent(mockSplitter)

	_, err := svc.ForceProcessUser(context.Background(), userID)

	// Now we expect an error because of incomplete coverage
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "topic extraction incomplete")

	// Wait a bit for goroutines to avoid race conditions with mock assertions
	time.Sleep(50 * time.Millisecond)

	mockStore.AssertExpectations(t)
	mockSplitter.AssertExpectations(t)
}

func TestProcessChunk_ValidIDs(t *testing.T) {
	// Setup
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.Embedding.Model = "test-model"
	cfg.Agents.Archivist.Model = "test-model"
	cfg.Agents.Default.Model = "test-model"

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	messages := []storage.Message{
		{ID: 100, Role: "user", Content: "Msg 1", CreatedAt: time.Now()},
		{ID: 102, Role: "user", Content: "Msg 2", CreatedAt: time.Now().Add(time.Minute)},
	}

	// Expectations
	mockStore.On("GetUnprocessedMessages", userID).Return(messages, nil)

	// Mock splitter agent that returns valid topic covering 100-102
	mockSplitter := new(agenttesting.MockAgent)
	mockSplitter.On("Type").Return(string(agent.TypeSplitter)).Maybe()
	mockSplitter.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
		Structured: &splitter.Result{
			Topics: []splitter.ExtractedTopic{
				{Summary: "Valid Topic", StartMsgID: 100, EndMsgID: 102},
			},
		},
	}, nil)

	// Mock Embedding
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{
			{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
		},
	}, nil)

	// AddTopic SHOULD be called with correct IDs
	mockStore.On("AddTopic", mock.MatchedBy(func(topic storage.Topic) bool {
		return topic.StartMsgID == 100 && topic.EndMsgID == 102 && topic.Summary == "Valid Topic"
	})).Return(int64(1), nil)

	// Mock GetAllTopics/Facts for ReloadVectors (initial load - not called since we don't Start)
	// But ForceProcessUser calls processChunk which calls LoadNewVectors in goroutine.
	mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil).Maybe()
	// Mock incremental loading methods (called after processChunk)
	mockStore.On("GetTopicsAfterID", mock.Anything).Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetFactsAfterID", mock.Anything).Return([]storage.Fact{}, nil).Maybe()

	// Run
	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, nil, translator)
	svc.SetSplitterAgent(mockSplitter)

	_, err := svc.ForceProcessUser(context.Background(), userID)
	assert.NoError(t, err)

	// Wait a bit for goroutines
	time.Sleep(100 * time.Millisecond)

	mockStore.AssertExpectations(t)
	mockClient.AssertExpectations(t)
	mockSplitter.AssertExpectations(t)
}

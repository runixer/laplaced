package rag

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"

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

	mockStore := new(MockStorage)
	mockClient := new(MockClient)

	// 2. Expectations
	// Start loads vectors
	mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)

	// Background loop calls processAllUsers -> processTopicChunking
	// It calls GetUnprocessedMessages
	msgs := []storage.Message{
		{ID: 1, CreatedAt: time.Now().Add(-10 * time.Hour), Content: "Msg 1"},
		{ID: 2, CreatedAt: time.Now().Add(-9 * time.Hour), Content: "Msg 2"}, // Diff 1h
		// ... we need a gap > 5h (default chunk duration)
		{ID: 3, CreatedAt: time.Now().Add(-1 * time.Hour), Content: "Msg 3"}, // Diff 8h -> Trigger chunk [1, 2]
	}
	mockStore.On("GetUnprocessedMessages", int64(123)).Return(msgs, nil)

	// Fact extraction loop calls GetTopicsPendingFacts
	topic := storage.Topic{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 3, CreatedAt: time.Now(), ConsolidationChecked: true}
	mockStore.On("GetTopicsPendingFacts", int64(123)).Return([]storage.Topic{topic}, nil)
	mockStore.On("SetTopicFactsExtracted", int64(1), true).Return(nil)

	// It calls GetMessagesInRange (from processFactExtraction)
	mockStore.On("GetMessagesInRange", mock.Anything, int64(123), int64(1), int64(3)).Return(msgs, nil)

	// Consolidation loop calls GetTopics
	mockStore.On("GetTopics", int64(123)).Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetMergeCandidates", int64(123)).Return([]storage.MergeCandidate{}, nil).Maybe()

	// Mock AddRAGLog for topic extraction logging
	mockStore.On("AddRAGLog", mock.Anything).Return(nil).Maybe()

	// processChunk calls CreateTopic (for Noise) and CreateEmbeddings
	mockStore.On("CreateTopic", mock.Anything).Return(int64(1), nil)
	mockStore.On("UpdateMessageTopic", mock.Anything, mock.Anything).Return(nil)
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2}}},
	}, nil)

	// processChunk calls:
	// 1. extractTopics (LLM)
	// We want to simulate delay here.
	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openrouter.ChatCompletionRequest) bool {
		// Check if it's topic extraction
		// We need to check the response format map manually since it's interface{}
		if req.ResponseFormat == nil {
			return false
		}

		// Try to cast to map
		if m, ok := req.ResponseFormat.(map[string]interface{}); ok {
			if js, ok := m["json_schema"].(map[string]interface{}); ok {
				if name, ok := js["name"].(string); ok {
					return name == "topic_extraction"
				}
			}
		}
		return false
	})).Run(func(args mock.Arguments) {
		// Sleep to simulate long running task
		time.Sleep(500 * time.Millisecond)
	}).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{Role: "assistant", Content: `{"topics": []}`}},
		},
	}, nil)

	// 2. memoryService.ProcessSession (LLM)
	mockStore.On("GetFacts", int64(123)).Return([]storage.Fact{}, nil)
	mockStore.On("GetAllUsers").Return([]storage.User{}, nil).Maybe()

	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openrouter.ChatCompletionRequest) bool {
		// Check if it's memory update
		if req.ResponseFormat == nil {
			return false
		}

		// Try to cast to *openrouter.ResponseFormat (used in memory.go)
		if rf, ok := req.ResponseFormat.(*openrouter.ResponseFormat); ok {
			return rf.JSONSchema != nil && rf.JSONSchema.Name == "memory_update"
		}

		return false
	})).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{Role: "assistant", Content: `{"added": [], "updated": [], "removed": []}`}},
		},
	}, nil)

	// Create dummy translator
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("key: value"), 0644)
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

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

	// Verify that the task actually completed (mock was called)
	// We expect 2 calls: 1 for topic extraction (which slept), 1 for memory update
	mockClient.AssertNumberOfCalls(t, "CreateChatCompletion", 2)
}

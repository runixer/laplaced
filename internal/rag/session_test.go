package rag

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
)

func TestGetActiveSessions(t *testing.T) {
	parseTime := func(s string) time.Time {
		t, _ := time.Parse(time.RFC3339, s)
		return t
	}

	t.Run("returns sessions for users with unprocessed messages", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123, 456}},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockLLMClient)

		// User 123 has unprocessed messages
		user123Messages := []storage.Message{
			{ID: 1, UserID: storage.PassthroughScopeID("telegram", "123"), Role: "user", Content: "Hello", CreatedAt: parseTime("2026-01-02T10:00:00Z")},
			{ID: 2, UserID: storage.PassthroughScopeID("telegram", "123"), Role: "assistant", Content: "Hi", CreatedAt: parseTime("2026-01-02T10:01:00Z")},
			{ID: 3, UserID: storage.PassthroughScopeID("telegram", "123"), Role: "user", Content: "How are you?", CreatedAt: parseTime("2026-01-02T10:02:00Z")},
		}
		mockStore.On("GetUnprocessedMessages", storage.PassthroughScopeID("telegram", "123")).Return(user123Messages, nil)

		// User 456 has no unprocessed messages
		mockStore.On("GetUnprocessedMessages", storage.PassthroughScopeID("telegram", "456")).Return([]storage.Message{}, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc, err := NewServiceBuilder().
			WithLogger(logger).
			WithConfig(cfg).
			WithLLMClient(mockClient).
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

		sessions, err := svc.GetActiveSessions()

		assert.NoError(t, err)
		assert.Len(t, sessions, 1)
		assert.Equal(t, storage.PassthroughScopeID("telegram", "123"), sessions[0].UserID)
		assert.Equal(t, 3, sessions[0].MessageCount)
		assert.Equal(t, parseTime("2026-01-02T10:00:00Z"), sessions[0].FirstMessageTime)
		assert.Equal(t, parseTime("2026-01-02T10:02:00Z"), sessions[0].LastMessageTime)
		// "Hello" (5) + "Hi" (2) + "How are you?" (12) = 19 chars
		assert.Equal(t, 19, sessions[0].ContextSize)
	})

	t.Run("returns empty when no users have unprocessed messages", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockLLMClient)

		mockStore.On("GetUnprocessedMessages", storage.PassthroughScopeID("telegram", "123")).Return([]storage.Message{}, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc, err := NewServiceBuilder().
			WithLogger(logger).
			WithConfig(cfg).
			WithLLMClient(mockClient).
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

		sessions, err := svc.GetActiveSessions()

		assert.NoError(t, err)
		assert.Empty(t, sessions)
	})
}

func TestForceProcessUserWithProgress(t *testing.T) {
	t.Run("returns empty stats when no unprocessed messages", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockLLMClient)

		mockStore.On("GetUnprocessedMessages", storage.ScopeID("123")).Return([]storage.Message{}, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc, err := NewServiceBuilder().
			WithLogger(logger).
			WithConfig(cfg).
			WithLLMClient(mockClient).
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

		var events []ProgressEvent
		callback := func(e ProgressEvent) {
			events = append(events, e)
		}

		stats, err := svc.ForceProcessUserWithProgress(context.Background(), "123", callback)

		assert.NoError(t, err)
		assert.NotNil(t, stats)
		assert.Equal(t, 0, stats.MessagesProcessed)
		assert.Len(t, events, 1)
		assert.Equal(t, "complete", events[0].Stage)
		assert.True(t, events[0].Complete)
	})

	t.Run("handles GetUnprocessedMessages error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockLLMClient)

		mockStore.On("GetUnprocessedMessages", storage.ScopeID("123")).Return([]storage.Message{}, assert.AnError)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc, err := NewServiceBuilder().
			WithLogger(logger).
			WithConfig(cfg).
			WithLLMClient(mockClient).
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

		callback := func(e ProgressEvent) {}

		_, err = svc.ForceProcessUserWithProgress(context.Background(), "123", callback)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch unprocessed messages")
	})
}

func TestForceProcessUser(t *testing.T) {
	t.Run("returns 0 when no unprocessed messages", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockLLMClient)

		mockStore.On("GetUnprocessedMessages", storage.ScopeID("123")).Return([]storage.Message{}, nil)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc, err := NewServiceBuilder().
			WithLogger(logger).
			WithConfig(cfg).
			WithLLMClient(mockClient).
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

		count, err := svc.ForceProcessUser(context.Background(), "123")

		assert.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("handles GetUnprocessedMessages error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(testutil.MockStorage)
		mockClient := new(testutil.MockLLMClient)

		mockStore.On("GetUnprocessedMessages", storage.ScopeID("123")).Return([]storage.Message{}, assert.AnError)

		translator := testutil.TestTranslator(t)

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc, err := NewServiceBuilder().
			WithLogger(logger).
			WithConfig(cfg).
			WithLLMClient(mockClient).
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

		_, err = svc.ForceProcessUser(context.Background(), "123")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch unprocessed messages")
	})
}

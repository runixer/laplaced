package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestStatsHandler_ErrorPaths tests error paths in stats handler.
func TestStatsHandler_ErrorPaths(t *testing.T) {
	t.Run("GetStats error", func(t *testing.T) {
		server, mockStorage, _ := setupTestServer(t)
		mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
		mockStorage.On("GetStats").Return(nil, assert.AnError)

		req := testutil.NewTestRequest(t, "GET", "/ui/stats?user_id=123", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.statsHandler), req)

		assert.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

// TestStatsHandler_CacheHit tests cache hit scenario.
func TestStatsHandler_CacheHit(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)
	stats := &storage.DashboardStats{TotalTopics: 10, TotalFacts: 5}
	server.statsCache.Set(123, stats)

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetStats").Return(map[int64]storage.Stat{123: {TokensUsed: 100}}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/stats?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.statsHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestSessionsHandler_NoSessions tests empty sessions list.
func TestSessionsHandler_NoSessions(t *testing.T) {
	server, mockStorage, mockBot := setupTestServer(t)
	mockBot.On("GetActiveSessions").Return([]rag.ActiveSessionInfo{}, nil)
	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123, FirstName: "Test"}}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.sessionsHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestSessionsHandler_UsernameFallback tests username fallback.
func TestSessionsHandler_UsernameFallback(t *testing.T) {
	server, mockStorage, mockBot := setupTestServer(t)
	sessions := []rag.ActiveSessionInfo{
		{UserID: 123, MessageCount: 1, FirstMessageTime: time.Now(), LastMessageTime: time.Now(), ContextSize: 100},
	}
	mockBot.On("GetActiveSessions").Return(sessions, nil)
	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123, Username: "testuser"}}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.sessionsHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "@testuser")
}

// TestSessionsProcessSSEHandler_Validation tests input validation.
func TestSessionsProcessSSEHandler_Validation(t *testing.T) {
	server, _, _ := setupTestServer(t)

	t.Run("method not allowed", func(t *testing.T) {
		req := testutil.NewTestRequest(t, "POST", "/ui/debug/sessions/process", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.sessionsProcessSSEHandler), req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("invalid user_id", func(t *testing.T) {
		req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions/process?user_id=invalid", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.sessionsProcessSSEHandler), req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("missing user_id", func(t *testing.T) {
		req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions/process", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.sessionsProcessSSEHandler), req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})
}

// TestDebugChatHandler_Validation tests input validation.
func TestDebugChatHandler_Validation(t *testing.T) {
	t.Run("get users error", func(t *testing.T) {
		server, mockStorage, _ := setupTestServer(t)
		mockStorage.On("GetAllUsers").Return(nil, assert.AnError)

		req := testutil.NewTestRequest(t, "GET", "/ui/debug/chat?user_id=123", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.debugChatHandler), req)
		assert.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("get messages error", func(t *testing.T) {
		server, mockStorage, _ := setupTestServer(t)
		mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
		mockStorage.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{}, assert.AnError)

		req := testutil.NewTestRequest(t, "GET", "/ui/debug/chat?user_id=123", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.debugChatHandler), req)
		// Handler still renders page even with message error
		assert.Equal(t, http.StatusOK, rr.Code)
	})
}

// TestDebugChatSendHandler_Validation tests input validation.
func TestDebugChatSendHandler_Validation(t *testing.T) {
	server, _, _ := setupTestServer(t)

	t.Run("missing user_id", func(t *testing.T) {
		body := map[string]any{"message": "test", "save_to_history": true}
		req := testutil.NewTestRequest(t, "POST", "/ui/debug/chat/send", body)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.debugChatSendHandler), req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("empty message", func(t *testing.T) {
		body := map[string]any{"user_id": 123, "message": "", "save_to_history": true}
		req := testutil.NewTestRequest(t, "POST", "/ui/debug/chat/send", body)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.debugChatSendHandler), req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})
}

// TestUpdateMetrics_ErrorHandling tests nil repo handling.
func TestUpdateMetrics_ErrorHandling(t *testing.T) {
	t.Run("nil fact repo", func(t *testing.T) {
		server, _, _ := setupTestServer(t)
		server.factRepo = nil
		server.topicRepo = nil
		// Should not panic
		server.updateMetrics()
	})

	t.Run("nil bot", func(t *testing.T) {
		server, mockStorage, _ := setupTestServer(t)
		server.bot = nil
		server.maintenanceRepo = nil // Skip storage metrics
		mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
		mockStorage.On("GetFactStatsByUser", int64(123)).Return(storage.FactStats{CountByType: map[string]int{"identity": 10}}, nil)
		mockStorage.On("GetTopicsExtended", mock.Anything, 1, 0, "", "").Return(storage.TopicResult{TotalCount: 5}, nil)
		// Should not panic
		server.updateMetrics()
	})
}

// TestLoggingMiddleware_SkipLogging tests routes that skip logging.
func TestLoggingMiddleware_SkipLogging(t *testing.T) {
	server, _, _ := setupTestServer(t)

	t.Run("webhook path", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		req := testutil.NewTestRequest(t, "GET", "/telegram/test-token", nil)
		rr := testutil.ExecuteRequest(t, server.loggingMiddleware(handler), req)
		assert.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("healthz path", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		req := testutil.NewTestRequest(t, "GET", "/healthz", nil)
		rr := testutil.ExecuteRequest(t, server.loggingMiddleware(handler), req)
		assert.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("metrics path", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		req := testutil.NewTestRequest(t, "GET", "/metrics", nil)
		rr := testutil.ExecuteRequest(t, server.loggingMiddleware(handler), req)
		assert.Equal(t, http.StatusOK, rr.Code)
	})
}

// TestBasicAuthMiddleware_SkipForNonUI tests auth middleware skips non-UI routes.
func TestBasicAuthMiddleware_SkipForNonUI(t *testing.T) {
	server, _, _ := setupTestServer(t)
	server.cfg.Server.Auth.Enabled = true
	server.cfg.Server.Auth.Username = "admin"
	server.cfg.Server.Auth.Password = "secret"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	middleware := server.basicAuthMiddleware(handler)

	req := testutil.NewTestRequest(t, "GET", "/healthz", nil)
	rr := testutil.ExecuteRequest(t, middleware, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestStart_PasswordGeneration tests password generation on start.
func TestStart_PasswordGeneration(t *testing.T) {
	server, mockStorage, mockBot := setupTestServer(t)
	server.cfg.Server.Auth.Enabled = true
	server.cfg.Server.Auth.Password = ""

	// Mock updateMetrics calls
	mockBot.On("GetActiveSessions").Return([]rag.ActiveSessionInfo{}, nil)
	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetFactStatsByUser", int64(123)).Return(storage.FactStats{CountByType: map[string]int{}}, nil)
	mockStorage.On("GetTopicsExtended", mock.Anything, 1, 0, "", "").Return(storage.TopicResult{TotalCount: 0}, nil)
	mockStorage.On("GetDBSize").Return(int64(1024), nil)
	mockStorage.On("GetTableSizes").Return([]storage.TableSize{}, nil)
	mockStorage.On("CleanupFactHistory", mock.Anything).Return(int64(0), nil)
	mockStorage.On("CleanupAgentLogs", mock.Anything).Return(int64(0), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)

	// Start in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start(ctx)
	}()

	// Wait a bit for password generation
	time.Sleep(50 * time.Millisecond)

	// Stop server first to avoid race
	cancel()
	err := <-errCh
	assert.NoError(t, err)

	// Now check password (server is stopped, no concurrent access)
	assert.NotEmpty(t, server.cfg.Server.Auth.Password)
	assert.Len(t, server.cfg.Server.Auth.Password, 12)
}

// TestWebhookHandler_SecretValidation tests webhook secret validation.
func TestWebhookHandler_SecretValidation(t *testing.T) {
	server, _, _ := setupTestServer(t)
	server.cfg.Telegram.WebhookSecret = "test-secret"

	t.Run("no secret header", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		server.ctx = ctx

		body := []byte(`{"update_id":1}`)
		req := testutil.NewTestRequest(t, "POST", "/telegram/webhook", body)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		server.webhookHandler(rr, req)
		assert.Equal(t, http.StatusForbidden, rr.Code)
	})

	t.Run("wrong secret", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		server.ctx = ctx

		body := []byte(`{"update_id":1}`)
		req := testutil.NewTestRequest(t, "POST", "/telegram/webhook", body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong-secret")
		rr := httptest.NewRecorder()
		server.webhookHandler(rr, req)
		assert.Equal(t, http.StatusForbidden, rr.Code)
	})
}

// TestSplitTopicsHandler_Validation tests input validation.
func TestSplitTopicsHandler_Validation(t *testing.T) {
	server, _, _ := setupTestServer(t)

	t.Run("method not allowed", func(t *testing.T) {
		req := testutil.NewTestRequest(t, "GET", "/ui/debug/split", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.splitTopicsHandler), req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("invalid json", func(t *testing.T) {
		req := testutil.NewTestRequest(t, "POST", "/ui/debug/split", "{invalid json")
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.splitTopicsHandler), req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})
}

// TestArtifactDownloadHandler_CacheHeaders tests response headers.
func TestArtifactDownloadHandler_CacheHeaders(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)
	testDir := t.TempDir()
	testFile := filepath.Join(testDir, "test.txt")
	assert.NoError(t, os.WriteFile(testFile, []byte("test"), 0644))
	server.cfg.Artifacts.StoragePath = testDir

	artifact := &storage.Artifact{
		ID: 1, UserID: 123, FilePath: "test.txt",
		OriginalName: "test.txt", MimeType: "text/plain",
	}
	mockStorage.On("GetArtifact", int64(123), int64(1)).Return(artifact, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/artifacts/download?id=1&user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.artifactDownloadHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "attachment; filename=\"test.txt\"", rr.Header().Get("Content-Disposition"))
	assert.Equal(t, "text/plain", rr.Header().Get("Content-Type"))
}

// TestDashboardStatsCache_Expiration2 tests cache expiration with short TTL.
func TestDashboardStatsCache_Expiration2(t *testing.T) {
	cache := newDashboardStatsCache(1 * time.Millisecond)

	stats := &storage.DashboardStats{TotalTopics: 10}
	cache.Set(123, stats)

	_, ok := cache.Get(123)
	assert.True(t, ok)

	time.Sleep(10 * time.Millisecond)

	_, ok = cache.Get(123)
	assert.False(t, ok)
}

// TestFactsHandler_ErrorPaths tests error paths in facts handler.
func TestFactsHandler_ErrorPaths(t *testing.T) {
	t.Run("GetFacts error", func(t *testing.T) {
		server, mockStorage, _ := setupTestServer(t)
		mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
		mockStorage.On("GetFacts", int64(123)).Return([]storage.Fact{}, assert.AnError)

		req := testutil.NewTestRequest(t, "GET", "/ui/debug/facts?user_id=123", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.factsHandler), req)

		assert.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

// TestPeopleHandler_ErrorPaths tests error paths in people handler.
func TestPeopleHandler_ErrorPaths(t *testing.T) {
	t.Run("GetPeople error", func(t *testing.T) {
		server, mockStorage, _ := setupTestServer(t)
		mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
		mockStorage.On("GetPeopleExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(storage.PersonResult{}, assert.AnError)

		req := testutil.NewTestRequest(t, "GET", "/ui/debug/people?user_id=123", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.peopleHandler), req)

		assert.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

// TestTopicsHandler_ErrorPaths tests error paths in topics handler.
func TestTopicsHandler_ErrorPaths(t *testing.T) {
	t.Run("GetTopicsExtended error", func(t *testing.T) {
		server, mockStorage, _ := setupTestServer(t)
		mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
		mockStorage.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(storage.TopicResult{}, assert.AnError)

		req := testutil.NewTestRequest(t, "GET", "/ui/debug/topics?user_id=123", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.topicsHandler), req)

		assert.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

// TestArtifactsHandler_ErrorPaths tests error paths in artifacts handler.
func TestArtifactsHandler_ErrorPaths(t *testing.T) {
	t.Run("GetArtifacts error", func(t *testing.T) {
		server, mockStorage, _ := setupTestServer(t)
		mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
		mockStorage.On("GetArtifacts", mock.Anything, mock.Anything, mock.Anything).Return([]storage.Artifact{}, int64(0), assert.AnError)

		req := testutil.NewTestRequest(t, "GET", "/ui/debug/artifacts?user_id=123", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.artifactsHandler), req)

		assert.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

// TestAgentLogDetailHandler_ErrorPaths tests error paths in agent log detail handler.
func TestAgentLogDetailHandler_ErrorPaths(t *testing.T) {
	t.Run("GetAgentLogFull error", func(t *testing.T) {
		server, mockStorage, _ := setupTestServer(t)
		mockStorage.On("GetAgentLogFull", mock.Anything, int64(123), int64(123)).Return(nil, assert.AnError)

		req := testutil.NewTestRequest(t, "GET", "/api/agent-log/123?user_id=123", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.agentLogDetailHandler), req)

		// Handler returns 404 when log is not found
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("missing user_id", func(t *testing.T) {
		server, _, _ := setupTestServer(t)

		req := testutil.NewTestRequest(t, "GET", "/api/agent-log/123", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.agentLogDetailHandler), req)

		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})
}

// TestStatsHandler_EmptyStats tests stats handler with empty stats.
func TestStatsHandler_EmptyStats(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)
	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetStats").Return(map[int64]storage.Stat{}, nil)
	mockStorage.On("GetDashboardStats", int64(123)).Return(&storage.DashboardStats{}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/stats?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.statsHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

// setupTestServerWithRAG creates a test server with RAG service mocked.
func setupTestServerWithRAG(t *testing.T) (*Server, *testutil.MockStorage, *MockBotInterface, *MockMaintenanceService) {
	t.Helper()

	mockStorage := new(testutil.MockStorage)
	mockBot := new(MockBotInterface)
	mockAPI := new(testutil.MockBotAPI)
	mockRAG := new(MockMaintenanceService)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.RAG.ChunkInterval = "1h"
	cfg.Artifacts.StoragePath = t.TempDir()
	cfg.Bot.AllowedUserIDs = []int64{123, 456}

	mockBot.On("API").Return(mockAPI)
	mockAPI.On("GetToken").Return("test-token")

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg,
		mockStorage, mockStorage, mockStorage, mockStorage,
		mockStorage, mockStorage, mockStorage, mockStorage,
		mockBot, mockRAG)
	assert.NoError(t, err)

	server.SetAgentLogRepo(mockStorage)
	server.SetPeopleRepository(mockStorage)
	server.SetArtifactRepository(mockStorage)

	return server, mockStorage, mockBot, mockRAG
}

// TestDatabaseHealthHandler_Success tests successful database health check.
func TestDatabaseHealthHandler_Success(t *testing.T) {
	server, _, _, mockRAG := setupTestServerWithRAG(t)

	mockRAG.On("GetDatabaseHealth", mock.Anything, int64(123), 25000).
		Return(&rag.DatabaseHealth{
			OrphanedTopics:   0,
			OverlappingPairs: 0,
			LargeTopics:      0,
		}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/database/health?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databaseHealthHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	mockRAG.AssertExpectations(t)
}

// TestDatabaseHealthHandler_Error tests error handling.
func TestDatabaseHealthHandler_Error(t *testing.T) {
	server, _, _, mockRAG := setupTestServerWithRAG(t)

	mockRAG.On("GetDatabaseHealth", mock.Anything, int64(123), 25000).
		Return((*rag.DatabaseHealth)(nil), assert.AnError)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/database/health?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databaseHealthHandler), req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// TestDatabaseRepairHandler_Success tests successful database repair.
func TestDatabaseRepairHandler_Success(t *testing.T) {
	server, _, _, mockRAG := setupTestServerWithRAG(t)

	mockRAG.On("RepairDatabase", mock.Anything, int64(123), true).
		Return(&rag.RepairStats{
			OrphanedTopicsDeleted: 5,
			FactsRelinked:         3,
		}, nil)

	req := testutil.NewTestRequest(t, "POST", "/ui/debug/database/repair",
		map[string]any{"user_id": int64(123), "dry_run": true})
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databaseRepairHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"success":true`)
	mockRAG.AssertExpectations(t)
}

// TestDatabaseRepairHandler_Error tests error handling.
func TestDatabaseRepairHandler_Error(t *testing.T) {
	server, _, _, mockRAG := setupTestServerWithRAG(t)

	mockRAG.On("RepairDatabase", mock.Anything, int64(123), false).
		Return((*rag.RepairStats)(nil), assert.AnError)

	req := testutil.NewTestRequest(t, "POST", "/ui/debug/database/repair",
		map[string]any{"user_id": int64(123), "dry_run": false})
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databaseRepairHandler), req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, rr.Body.String(), `"error":`)
}

// TestDatabaseContaminationHandler_Success tests successful contamination check.
func TestDatabaseContaminationHandler_Success(t *testing.T) {
	server, _, _, mockRAG := setupTestServerWithRAG(t)

	mockRAG.On("GetContaminationInfo", mock.Anything, int64(123)).
		Return(&rag.ContaminationInfo{
			TotalContaminated:  0,
			ContaminatedTopics: []storage.ContaminatedTopic{},
		}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/database/contamination?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databaseContaminationHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	mockRAG.AssertExpectations(t)
}

// TestDatabaseContaminationFixHandler_Success tests successful contamination fix.
func TestDatabaseContaminationFixHandler_Success(t *testing.T) {
	server, _, _, mockRAG := setupTestServerWithRAG(t)

	mockRAG.On("FixContamination", mock.Anything, int64(123), true).
		Return(&rag.ContaminationFixStats{
			MessagesUnlinked:      10,
			OrphanedTopicsDeleted: 2,
		}, nil)

	req := testutil.NewTestRequest(t, "POST", "/ui/debug/database/contamination/fix",
		map[string]any{"user_id": int64(123), "dry_run": true})
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databaseContaminationFixHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"success":true`)
	mockRAG.AssertExpectations(t)
}

// TestSplitTopicsHandler_Success tests successful topic splitting.
func TestSplitTopicsHandler_Success(t *testing.T) {
	server, _, _, mockRAG := setupTestServerWithRAG(t)

	mockRAG.On("SplitLargeTopics", mock.Anything, int64(123), 25000).
		Return(&rag.SplitStats{
			TopicsProcessed:  2,
			TopicsCreated:    5,
			FactsRelinked:    3,
			PromptTokens:     1000,
			CompletionTokens: 500,
			EmbeddingTokens:  200,
		}, nil)

	req := testutil.NewTestRequest(t, "POST", "/ui/debug/split",
		map[string]any{"user_id": int64(123), "threshold_chars": 25000})
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.splitTopicsHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"success":true`)
	mockRAG.AssertExpectations(t)
}

// TestSplitTopicsHandler_Error tests error handling.
func TestSplitTopicsHandler_Error(t *testing.T) {
	server, _, _, mockRAG := setupTestServerWithRAG(t)

	mockRAG.On("SplitLargeTopics", mock.Anything, int64(123), 25000).
		Return((*rag.SplitStats)(nil), assert.AnError)

	req := testutil.NewTestRequest(t, "POST", "/ui/debug/split",
		map[string]any{"user_id": int64(123), "threshold_chars": 25000})
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.splitTopicsHandler), req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, rr.Body.String(), `"error":`)
}

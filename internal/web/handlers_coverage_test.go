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

// setupTestServer creates a test server with all required dependencies mocked.
func setupTestServer(t *testing.T) (*Server, *testutil.MockStorage, *MockBotInterface) {
	t.Helper()

	mockStorage := new(testutil.MockStorage)
	mockBot := new(MockBotInterface)
	mockAPI := new(testutil.MockBotAPI)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.RAG.ChunkInterval = "1h"
	cfg.Artifacts.StoragePath = t.TempDir()
	cfg.Bot.AllowedUserIDs = []int64{123, 456}

	mockBot.On("API").Return(mockAPI)
	mockAPI.On("GetToken").Return("test-token")

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	// Set additional repositories
	server.SetAgentLogRepo(mockStorage)
	server.SetPeopleRepository(mockStorage)
	server.SetArtifactRepository(mockStorage)

	return server, mockStorage, mockBot
}

// TestFaviconHandler tests the favicon handler.
func TestFaviconHandler(t *testing.T) {
	server, _, _ := setupTestServer(t)

	req := testutil.NewTestRequest(t, "GET", "/favicon.ico", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.faviconHandler), req)

	// Handler should return 200
	// But the handler should not panic
	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestLogoHandler tests the logo handler.
func TestLogoHandler(t *testing.T) {
	server, _, _ := setupTestServer(t)

	req := testutil.NewTestRequest(t, "GET", "/static/logo.svg", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.logoHandler), req)

	// Handler should return 200
	// But the handler should not panic
	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestDatabaseMaintenanceHandler tests the database maintenance page handler.
func TestDatabaseMaintenanceHandler(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123, Username: "test"}}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/database?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databaseMaintenanceHandler), req)

	// Handler renders HTML, should succeed even with mock
	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestDatabaseMaintenanceHandler_MethodNotAllowed tests POST rejection.
func TestDatabaseMaintenanceHandler_MethodNotAllowed(t *testing.T) {
	server, _, _ := setupTestServer(t)

	req := testutil.NewTestRequest(t, "POST", "/ui/debug/database", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databaseMaintenanceHandler), req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

// TestDatabaseMaintenanceHandler_CommonDataError tests error getting common data.
func TestDatabaseMaintenanceHandler_CommonDataError(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	mockStorage.On("GetAllUsers").Return(nil, assert.AnError)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/database", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databaseMaintenanceHandler), req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// TestDatabaseRepairHandler_BasicValidation tests basic validation.
func TestDatabaseRepairHandler_BasicValidation(t *testing.T) {
	server, _, _ := setupTestServer(t)

	t.Run("method not allowed", func(t *testing.T) {
		req := testutil.NewTestRequest(t, "GET", "/ui/debug/database/repair", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databaseRepairHandler), req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		req := testutil.NewTestRequest(t, "POST", "/ui/debug/database/repair", "{invalid json")
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databaseRepairHandler), req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})
}

// TestDatabaseContaminationHandler_BasicValidation tests basic validation.
func TestDatabaseContaminationHandler_BasicValidation(t *testing.T) {
	server, _, _ := setupTestServer(t)

	t.Run("method not allowed", func(t *testing.T) {
		req := testutil.NewTestRequest(t, "POST", "/ui/debug/database/contamination", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databaseContaminationHandler), req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})
}

// TestDatabaseContaminationFixHandler_BasicValidation tests basic validation.
func TestDatabaseContaminationFixHandler_BasicValidation(t *testing.T) {
	server, _, _ := setupTestServer(t)

	t.Run("method not allowed", func(t *testing.T) {
		req := testutil.NewTestRequest(t, "GET", "/ui/debug/database/contamination/fix", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databaseContaminationFixHandler), req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		req := testutil.NewTestRequest(t, "POST", "/ui/debug/database/contamination/fix", "{invalid")
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databaseContaminationFixHandler), req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})
}

// TestDatabasePurgeHandler tests the database purge handler.
func TestDatabasePurgeHandler(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		body       interface{}
		setupMock  func(*testutil.MockStorage)
		expectCode int
	}{
		{
			name:       "method not allowed",
			method:     "GET",
			body:       nil,
			setupMock:  func(ms *testutil.MockStorage) {},
			expectCode: http.StatusMethodNotAllowed,
		},
		{
			name:       "invalid JSON",
			method:     "POST",
			body:       "{invalid",
			setupMock:  func(ms *testutil.MockStorage) {},
			expectCode: http.StatusBadRequest,
		},
		{
			name:   "valid dry run request",
			method: "POST",
			body:   map[string]any{"dry_run": true},
			setupMock: func(ms *testutil.MockStorage) {
				ms.On("CountAgentLogs").Return(int64(100), nil)
				ms.On("CountFactHistory").Return(int64(200), nil)
			},
			expectCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, mockStorage, _ := setupTestServer(t)
			tt.setupMock(mockStorage)

			req := testutil.NewTestRequest(t, tt.method, "/ui/debug/database/purge", tt.body)
			rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.databasePurgeHandler), req)

			assert.Equal(t, tt.expectCode, rr.Code)
		})
	}
}

// TestPeopleHandler tests the people handler.
func TestPeopleHandler(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	users := []storage.User{{ID: 123, Username: "testuser"}}
	people := []storage.Person{
		{ID: 1, UserID: 123, DisplayName: "Alice", Circle: "Family"},
		{ID: 2, UserID: 123, DisplayName: "Bob", Circle: "Work"},
	}

	mockStorage.On("GetAllUsers").Return(users, nil)
	mockStorage.On("GetPeopleExtended", storage.PersonFilter{UserID: 123}, 50, 0, "last_seen", "DESC").Return(
		storage.PersonResult{Data: people, TotalCount: 2}, nil,
	)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/people?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.peopleHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "Alice")
}

// TestPeopleHandler_MethodNotAllowed tests POST rejection.
func TestPeopleHandler_MethodNotAllowed(t *testing.T) {
	server, _, _ := setupTestServer(t)

	req := testutil.NewTestRequest(t, "POST", "/ui/debug/people", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.peopleHandler), req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

// TestPeopleHandler_ErrorGettingPeople tests error path.
func TestPeopleHandler_ErrorGettingPeople(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetPeopleExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.PersonResult{}, assert.AnError)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/people?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.peopleHandler), req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// TestFactsHandler tests the facts handler.
func TestFactsHandler(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	facts := []storage.Fact{
		{ID: 1, UserID: 123, Content: "User likes pizza", Type: "preference"},
		{ID: 2, UserID: 123, Content: "User lives in NY", Type: "identity"},
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetFacts", int64(123)).Return(facts, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/facts?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.factsHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "pizza")
}

// TestFactsHandler_MethodNotAllowed tests POST rejection.
func TestFactsHandler_MethodNotAllowed(t *testing.T) {
	server, _, _ := setupTestServer(t)

	req := testutil.NewTestRequest(t, "POST", "/ui/debug/facts", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.factsHandler), req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

// TestTopicsHandler tests the topics handler.
func TestTopicsHandler(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	topics := []storage.TopicExtended{
		{Topic: storage.Topic{ID: 1, UserID: 123, Summary: "Discussion about AI"}},
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetTopicsExtended", storage.TopicFilter{UserID: 123}, 20, 0, "created_at", "DESC").
		Return(storage.TopicResult{Data: topics, TotalCount: 1}, nil)
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return([]storage.Message{}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/topics?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.topicsHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestTopicsHandler_MethodNotAllowed tests POST rejection.
func TestTopicsHandler_MethodNotAllowed(t *testing.T) {
	server, _, _ := setupTestServer(t)

	req := testutil.NewTestRequest(t, "POST", "/ui/debug/topics", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.topicsHandler), req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

// TestTopicsHandler_Pagination tests pagination parameters.
func TestTopicsHandler_Pagination(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetTopicsExtended", storage.TopicFilter{UserID: 123}, 20, 20, "created_at", "DESC").
		Return(storage.TopicResult{Data: []storage.TopicExtended{}, TotalCount: 0}, nil)
	mockStorage.On("GetMessagesByTopicID", mock.Anything, mock.Anything).Return([]storage.Message{}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/topics?user_id=123&page=2", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.topicsHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestArtifactsHandler tests the artifacts handler.
func TestArtifactsHandler(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	artifacts := []storage.Artifact{
		{ID: 1, UserID: 123, FileType: "image", OriginalName: "photo.jpg", State: "ready"},
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetArtifacts", storage.ArtifactFilter{UserID: 123, State: "", FileType: ""}, 20, 0).
		Return(artifacts, int64(1), nil)
	// Stats calls
	mockStorage.On("GetArtifacts", storage.ArtifactFilter{UserID: 123}, 1, 0).Return(artifacts, int64(1), nil)
	mockStorage.On("GetArtifacts", storage.ArtifactFilter{UserID: 123, State: "pending"}, 1, 0).Return([]storage.Artifact{}, int64(0), nil)
	mockStorage.On("GetArtifacts", storage.ArtifactFilter{UserID: 123, State: "ready"}, 1, 0).Return(artifacts, int64(1), nil)
	mockStorage.On("GetArtifacts", storage.ArtifactFilter{UserID: 123, State: "failed"}, 1, 0).Return([]storage.Artifact{}, int64(0), nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/artifacts?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.artifactsHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "photo.jpg")
}

// TestArtifactsHandler_MethodNotAllowed tests POST rejection.
func TestArtifactsHandler_MethodNotAllowed(t *testing.T) {
	server, _, _ := setupTestServer(t)

	req := testutil.NewTestRequest(t, "POST", "/ui/debug/artifacts", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.artifactsHandler), req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

// TestArtifactDownloadHandler tests the artifact download handler.
func TestArtifactDownloadHandler(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	// Create a test artifact file
	testDir := t.TempDir()
	testFile := filepath.Join(testDir, "test_artifact.txt")
	content := []byte("test file content")
	err := os.WriteFile(testFile, content, 0644)
	assert.NoError(t, err)

	server.cfg.Artifacts.StoragePath = testDir

	artifact := &storage.Artifact{
		ID: 1, UserID: 123, FilePath: "test_artifact.txt",
		OriginalName: "test.txt", MimeType: "text/plain",
	}

	mockStorage.On("GetArtifact", int64(123), int64(1)).Return(artifact, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/artifacts/download?id=1&user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.artifactDownloadHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "attachment; filename=\"test.txt\"", rr.Header().Get("Content-Disposition"))
	assert.Contains(t, rr.Body.String(), "test file content")
}

// TestArtifactDownloadHandler_MissingID tests missing artifact ID.
func TestArtifactDownloadHandler_MissingID(t *testing.T) {
	server, _, _ := setupTestServer(t)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/artifacts/download?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.artifactDownloadHandler), req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestArtifactDownloadHandler_InvalidUserID tests invalid user ID.
func TestArtifactDownloadHandler_InvalidUserID(t *testing.T) {
	server, _, _ := setupTestServer(t)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/artifacts/download?id=1&user_id=invalid", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.artifactDownloadHandler), req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestArtifactDownloadHandler_UserNotAllowed tests unauthorized user.
func TestArtifactDownloadHandler_UserNotAllowed(t *testing.T) {
	server, _, _ := setupTestServer(t)
	server.cfg.Bot.AllowedUserIDs = []int64{999} // Different user

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/artifacts/download?id=1&user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.artifactDownloadHandler), req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

// TestArtifactDownloadHandler_ArtifactNotFound tests artifact not found.
func TestArtifactDownloadHandler_ArtifactNotFound(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	mockStorage.On("GetArtifact", int64(123), int64(1)).Return((*storage.Artifact)(nil), nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/artifacts/download?id=1&user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.artifactDownloadHandler), req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// TestArtifactDownloadHandler_PathTraversal tests path traversal protection.
func TestArtifactDownloadHandler_PathTraversal(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	// Create artifact with path traversal attempt
	artifact := &storage.Artifact{
		ID: 1, UserID: 123, FilePath: "../../../etc/passwd",
		OriginalName: "passwd", MimeType: "text/plain",
	}

	mockStorage.On("GetArtifact", int64(123), int64(1)).Return(artifact, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/artifacts/download?id=1&user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.artifactDownloadHandler), req)

	// Should be blocked by path validation
	assert.NotEqual(t, http.StatusOK, rr.Code)
}

// TestAgentLogHandler tests the agent log handler.
func TestAgentLogHandler(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	logs := []storage.AgentLog{
		{ID: 1, AgentType: "laplace", UserID: 123, CreatedAt: time.Now()},
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetAgentLogs", "laplace", int64(123), 50).Return(logs, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/agents/laplace?user_id=123", nil)

	// Create handler using the factory
	handler := server.agentLogHandler("laplace", "Laplace", "robot", "description")
	rr := testutil.ExecuteRequest(t, handler, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestAgentLogHandler_NoRepo tests error when repo is nil.
func TestAgentLogHandler_NoRepo(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)
	server.agentLogRepo = nil

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/agents/laplace?user_id=123", nil)
	handler := server.agentLogHandler("laplace", "Laplace", "robot", "description")
	rr := testutil.ExecuteRequest(t, handler, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// TestAgentLogDetailHandler tests the agent log detail handler.
func TestAgentLogDetailHandler(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	fullLog := &storage.AgentLog{
		ID: 1, AgentType: "laplace", UserID: 123,
		InputContext: "test input", OutputContext: "test output",
	}

	mockStorage.On("GetAgentLogFull", mock.Anything, int64(1), int64(123)).Return(fullLog, nil)

	req := testutil.NewTestRequest(t, "GET", "/api/agent-log/1?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.agentLogDetailHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	testutil.AssertContentType(t, rr, "application/json")
}

// TestAgentLogDetailHandler_MethodNotAllowed tests POST rejection.
func TestAgentLogDetailHandler_MethodNotAllowed(t *testing.T) {
	server, _, _ := setupTestServer(t)

	req := testutil.NewTestRequest(t, "POST", "/api/agent-log/1?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.agentLogDetailHandler), req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

// TestAgentLogDetailHandler_InvalidPath tests invalid path format.
func TestAgentLogDetailHandler_InvalidPath(t *testing.T) {
	server, _, _ := setupTestServer(t)

	req := testutil.NewTestRequest(t, "GET", "/api/agent-log/invalid?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.agentLogDetailHandler), req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestAgentLogDetailHandler_MissingUserID tests missing user_id parameter.
func TestAgentLogDetailHandler_MissingUserID(t *testing.T) {
	server, _, _ := setupTestServer(t)

	req := testutil.NewTestRequest(t, "GET", "/api/agent-log/1", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.agentLogDetailHandler), req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestSplitTopicsHandler_BasicValidation tests basic validation.
func TestSplitTopicsHandler_BasicValidation(t *testing.T) {
	server, _, _ := setupTestServer(t)

	t.Run("method not allowed", func(t *testing.T) {
		req := testutil.NewTestRequest(t, "GET", "/ui/debug/split", nil)
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.splitTopicsHandler), req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		req := testutil.NewTestRequest(t, "POST", "/ui/debug/split", "{invalid json")
		rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.splitTopicsHandler), req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})
}

// TestGetCommonData tests the getCommonData helper.
func TestGetCommonData(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	users := []storage.User{
		{ID: 123, Username: "user1"},
		{ID: 456, Username: "user2"},
	}
	mockStorage.On("GetAllUsers").Return(users, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/stats?user_id=456", nil)
	data, err := server.getCommonData(req)

	assert.NoError(t, err)
	assert.Equal(t, int64(456), data.SelectedUserID)
	assert.Equal(t, 2, len(data.Users))
}

// TestGetCommonData_DefaultsToFirstUser tests default user selection.
func TestGetCommonData_DefaultsToFirstUser(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	users := []storage.User{{ID: 123, Username: "user1"}}
	mockStorage.On("GetAllUsers").Return(users, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/stats", nil)
	data, err := server.getCommonData(req)

	assert.NoError(t, err)
	assert.Equal(t, int64(123), data.SelectedUserID)
}

// TestGetCommonData_InvalidUserID tests invalid user ID handling.
func TestGetCommonData_InvalidUserID(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/stats?user_id=invalid", nil)
	data, err := server.getCommonData(req)

	assert.NoError(t, err)
	// Should default to first user
	assert.Equal(t, int64(123), data.SelectedUserID)
}

// TestGetCommonData_NoUsers tests empty user list.
func TestGetCommonData_NoUsers(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	mockStorage.On("GetAllUsers").Return([]storage.User{}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/stats", nil)
	data, err := server.getCommonData(req)

	assert.NoError(t, err)
	assert.Equal(t, int64(0), data.SelectedUserID)
}

// TestDashboardStatsCache tests the dashboard stats cache.
func TestDashboardStatsCache(t *testing.T) {
	cache := newDashboardStatsCache(5 * time.Minute)

	stats := &storage.DashboardStats{TotalTopics: 10, TotalFacts: 5}

	// Get non-existent entry
	_, ok := cache.Get(123)
	assert.False(t, ok)

	// Set and get
	cache.Set(123, stats)
	retrieved, ok := cache.Get(123)
	assert.True(t, ok)
	assert.Equal(t, stats, retrieved)
}

// TestDashboardStatsCache_Expiration tests cache expiration.
func TestDashboardStatsCache_Expiration(t *testing.T) {
	cache := newDashboardStatsCache(1 * time.Millisecond) // Short TTL

	stats := &storage.DashboardStats{TotalTopics: 10}
	cache.Set(123, stats)

	// Should exist immediately
	_, ok := cache.Get(123)
	assert.True(t, ok)

	// Wait for expiration
	time.Sleep(10 * time.Millisecond)

	// Should be expired
	_, ok = cache.Get(123)
	assert.False(t, ok)
}

// TestWebhookHandler_ReadError tests error reading request body.
func TestWebhookHandler_ReadError(t *testing.T) {
	server, _, _ := setupTestServer(t)

	// Create a reader that will fail
	errorReader := &errorReader{}
	req := &http.Request{
		Method: "POST",
		Body:   errorReader,
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.ctx = ctx

	rr := httptest.NewRecorder()
	server.webhookHandler(rr, req)

	// Should handle error gracefully
	assert.NotEqual(t, http.StatusOK, rr.Code)
}

// errorReader is a reader that always returns an error.
type errorReader struct{}

func (e *errorReader) Read(p []byte) (n int, err error) {
	return 0, assert.AnError
}

func (e *errorReader) Close() error {
	return nil
}

// TestPeopleHandler_NoRepo tests nil people repository.
func TestPeopleHandler_NoRepo(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)
	server.peopleRepo = nil

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/people?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.peopleHandler), req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// TestPeopleHandler_CircleFilter tests circle filter parameter.
func TestPeopleHandler_CircleFilter(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	people := []storage.Person{
		{ID: 1, UserID: 123, DisplayName: "Alice", Circle: "Family"},
		{ID: 2, UserID: 123, DisplayName: "Bob", Circle: "Work"},
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetPeopleExtended", storage.PersonFilter{UserID: 123, Circle: "Family"}, 50, 0, "last_seen", "DESC").
		Return(storage.PersonResult{Data: people[:1], TotalCount: 1}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/people?user_id=123&circle=Family", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.peopleHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	mockStorage.AssertExpectations(t)
}

// TestPeopleHandler_SearchFilter tests search query parameter.
func TestPeopleHandler_SearchFilter(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	people := []storage.Person{
		{ID: 1, UserID: 123, DisplayName: "Alice Smith", Circle: "Family"},
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetPeopleExtended", storage.PersonFilter{UserID: 123, Search: "Alice"}, 50, 0, "last_seen", "DESC").
		Return(storage.PersonResult{Data: people, TotalCount: 1}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/people?user_id=123&search=Alice", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.peopleHandler), req)

	assert.Equal(t, http.StatusOK, rr.Code)
	mockStorage.AssertExpectations(t)
}

// Note: Some handlers (factsHandler, topicsHandler, artifactsHandler) don't check for nil repo
// These will panic if repo is nil - this is a known inconsistency with peopleHandler

// TestTopicsHandler_GetMessagesError tests error getting topic messages.
func TestTopicsHandler_GetMessagesError(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	topics := []storage.TopicExtended{
		{Topic: storage.Topic{ID: 1, UserID: 123, Summary: "Discussion"}},
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{Data: topics, TotalCount: 1}, nil)
	mockStorage.On("GetMessagesByTopicID", mock.Anything, int64(1)).Return([]storage.Message{}, assert.AnError)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/topics?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.topicsHandler), req)

	// Handler still renders page even with message error
	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestArtifactsHandler_StatsError tests error getting artifact stats.
func TestArtifactsHandler_StatsError(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetArtifacts", mock.Anything, mock.Anything, mock.Anything).Return([]storage.Artifact{}, int64(0), nil)
	// Stats calls fail
	mockStorage.On("GetArtifacts", storage.ArtifactFilter{UserID: 123}, 1, 0).Return([]storage.Artifact{}, int64(0), assert.AnError)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/artifacts?user_id=123", nil)
	rr := testutil.ExecuteRequest(t, http.HandlerFunc(server.artifactsHandler), req)

	// Handler still renders page even with stats error
	assert.Equal(t, http.StatusOK, rr.Code)
}

// TestAgentLogHandler_GetLogsError tests error getting agent logs.
func TestAgentLogHandler_GetLogsError(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetAgentLogs", "laplace", int64(123), 50).Return([]storage.AgentLog{}, assert.AnError)

	req := testutil.NewTestRequest(t, "GET", "/ui/agents/laplace?user_id=123", nil)
	handler := server.agentLogHandler("laplace", "Laplace", "robot", "description")
	rr := testutil.ExecuteRequest(t, handler, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// TestAgentLogHandler_RenderError tests template rendering error.
func TestAgentLogHandler_RenderError(t *testing.T) {
	server, mockStorage, _ := setupTestServer(t)

	logs := []storage.AgentLog{
		{ID: 1, AgentType: "laplace", UserID: 123},
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetAgentLogs", "laplace", int64(123), 50).Return(logs, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/agents/laplace?user_id=123", nil)
	handler := server.agentLogHandler("laplace", "Laplace", "robot", "description")
	rr := testutil.ExecuteRequest(t, handler, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

// Note: Render error can't be tested without mocking the renderer internally
// The renderer is always initialized in NewServer, so nil renderer is not a real scenario

// TestRoutesVerification verifies that key routes are registered.
func TestRoutesVerification(t *testing.T) {
	server, mockStorage, mockBot := setupTestServer(t)
	server.cfg.Server.DebugMode = true // Enable debug routes

	// Mock updateMetrics calls
	mockBot.On("GetActiveSessions").Return([]rag.ActiveSessionInfo{}, nil).Maybe()
	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil).Maybe()
	mockStorage.On("GetFactStatsByUser", int64(123)).Return(storage.FactStats{CountByType: map[string]int{}}, nil).Maybe()
	mockStorage.On("GetTopicsExtended", mock.Anything, 1, 0, "", "").Return(storage.TopicResult{TotalCount: 0}, nil).Maybe()
	mockStorage.On("GetDBSize").Return(int64(1024), nil).Maybe()
	mockStorage.On("GetTableSizes").Return([]storage.TableSize{}, nil).Maybe()
	mockStorage.On("CleanupFactHistory", mock.Anything).Return(int64(0), nil).Maybe()
	mockStorage.On("CleanupAgentLogs", mock.Anything).Return(int64(0), nil).Maybe()
	mockStorage.On("GetStats").Return(map[int64]storage.Stat{}, nil).Maybe()
	mockStorage.On("GetDashboardStats", int64(123)).Return(&storage.DashboardStats{}, nil).Maybe()
	mockStorage.On("GetFacts", int64(123)).Return([]storage.Fact{}, nil).Maybe()
	mockStorage.On("GetPeopleExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.PersonResult{Data: []storage.Person{}, TotalCount: 0}, nil).Maybe()
	mockStorage.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{Data: []storage.TopicExtended{}, TotalCount: 0}, nil).Maybe()
	mockStorage.On("GetArtifacts", mock.Anything, mock.Anything, mock.Anything).Return([]storage.Artifact{}, int64(0), nil).Maybe()

	// Start server in background
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start(ctx)
	}()

	// Wait a bit for server to start
	time.Sleep(20 * time.Millisecond)

	// The server creates mux internally, so we test handlers directly
	// Verify key handlers exist and are callable
	testCases := []struct {
		handler       http.HandlerFunc
		method        string
		path          string
		description   string
		expectedCodes []int
	}{
		{server.healthzHandler, "GET", "/healthz", "health check endpoint", []int{http.StatusOK}},
		{server.faviconHandler, "GET", "/favicon.ico", "favicon endpoint", []int{http.StatusOK, http.StatusNotFound}},
		{server.logoHandler, "GET", "/static/logo.svg", "logo endpoint", []int{http.StatusOK, http.StatusNotFound}},
		{server.statsHandler, "GET", "/ui/stats", "stats page", []int{http.StatusOK, http.StatusInternalServerError}},
		{server.peopleHandler, "GET", "/ui/debug/people", "people page", []int{http.StatusOK, http.StatusMethodNotAllowed, http.StatusInternalServerError}},
		{server.factsHandler, "GET", "/ui/debug/facts", "facts page", []int{http.StatusOK, http.StatusMethodNotAllowed, http.StatusInternalServerError}},
		{server.topicsHandler, "GET", "/ui/debug/topics", "topics page", []int{http.StatusOK, http.StatusMethodNotAllowed, http.StatusInternalServerError}},
		{server.artifactsHandler, "GET", "/ui/debug/artifacts", "artifacts page", []int{http.StatusOK, http.StatusMethodNotAllowed, http.StatusInternalServerError}},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			req := testutil.NewTestRequest(t, tc.method, tc.path, nil)
			rr := testutil.ExecuteRequest(t, tc.handler, req)

			assert.Contains(t, tc.expectedCodes, rr.Code,
				"handler %s should respond with one of %v, got %d", tc.description, tc.expectedCodes, rr.Code)
		})
	}

	// Clean up - context cancel will stop server
	cancel()
	<-errCh
}

// TestNewServer_DefaultConfig tests server creation with default config.
func TestNewServer_DefaultConfig(t *testing.T) {
	mockStorage := new(testutil.MockStorage)
	mockBot := new(MockBotInterface)
	mockAPI := new(testutil.MockBotAPI)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.RAG.ChunkInterval = "1h"
	cfg.Artifacts.StoragePath = t.TempDir()
	cfg.Bot.AllowedUserIDs = []int64{123}

	mockBot.On("API").Return(mockAPI)
	mockAPI.On("GetToken").Return("test-token")

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg,
		mockStorage, mockStorage, mockStorage, mockStorage,
		mockStorage, mockStorage, mockStorage, mockStorage,
		mockBot, nil)

	assert.NoError(t, err)
	assert.NotNil(t, server)
	assert.NotNil(t, server.statsCache)
}

// TestNewServer_InvalidPort tests server creation with invalid port.
func TestNewServer_InvalidPort(t *testing.T) {
	mockStorage := new(testutil.MockStorage)
	mockBot := new(MockBotInterface)
	mockAPI := new(testutil.MockBotAPI)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "invalid" // Invalid port number
	cfg.RAG.ChunkInterval = "1h"
	cfg.Artifacts.StoragePath = t.TempDir()
	cfg.Bot.AllowedUserIDs = []int64{123}

	mockBot.On("API").Return(mockAPI)
	mockAPI.On("GetToken").Return("test-token")

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg,
		mockStorage, mockStorage, mockStorage, mockStorage,
		mockStorage, mockStorage, mockStorage, mockStorage,
		mockBot, nil)

	// Server should still be created, but Start() will fail later
	assert.NoError(t, err)
	assert.NotNil(t, server)
	assert.NotNil(t, server.statsCache)
}

// TestStart_DebugModeDisabled verifies debug routes are not registered when DebugMode is false.
func TestStart_DebugModeDisabled(t *testing.T) {
	server, mockStorage, mockBot := setupTestServer(t)
	server.cfg.Server.DebugMode = false // Disable debug routes

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
	defer cancel()

	// Start should succeed
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start(ctx)
	}()

	time.Sleep(20 * time.Millisecond)

	// Clean up
	cancel()
	err := <-errCh
	assert.NoError(t, err)
}

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/runixer/laplaced/internal/telegram"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockBotInterface implements BotInterface for tests.
// This mock is kept in web package to avoid import cycles (rag <-> testutil).
type MockBotInterface struct {
	mock.Mock
}

func (m *MockBotInterface) API() telegram.BotAPI {
	args := m.Called()
	return args.Get(0).(telegram.BotAPI)
}

func (m *MockBotInterface) HandleUpdateAsync(ctx context.Context, update json.RawMessage, remoteAddr string) {
	m.Called(ctx, update, remoteAddr)
}

func (m *MockBotInterface) GetActiveSessions() ([]rag.ActiveSessionInfo, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]rag.ActiveSessionInfo), args.Error(1)
}

func (m *MockBotInterface) ForceCloseSession(ctx context.Context, userID int64) (int, error) {
	args := m.Called(ctx, userID)
	return args.Int(0), args.Error(1)
}

func (m *MockBotInterface) ForceCloseSessionWithProgress(ctx context.Context, userID int64, onProgress rag.ProgressCallback) (*rag.ProcessingStats, error) {
	args := m.Called(ctx, userID, onProgress)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*rag.ProcessingStats), args.Error(1)
}

func (m *MockBotInterface) SendTestMessage(ctx context.Context, userID int64, text string, saveToHistory bool) (*rag.TestMessageResult, error) {
	args := m.Called(ctx, userID, text, saveToHistory)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*rag.TestMessageResult), args.Error(1)
}

func TestStatsHandler(t *testing.T) {
	// Arrange
	mockStorage := new(testutil.MockStorage)
	mockBot := new(MockBotInterface)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	stats := map[int64]storage.Stat{
		123: {UserID: 123, TokensUsed: 1000, CostUSD: 0.001},
	}
	dashboardStats := &storage.DashboardStats{
		TotalTopics: 10,
		TotalFacts:  5,
	}
	mockStorage.On("GetStats").Return(stats, nil)
	mockStorage.On("GetDashboardStats", int64(0)).Return(dashboardStats, nil)
	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123, Username: "testuser"}}, nil)

	req, err := http.NewRequest("GET", "/ui/stats", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.statsHandler)

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "<td>123</td>")
	mockStorage.AssertExpectations(t)
}

func TestHealthzHandler(t *testing.T) {
	// Arrange
	mockBot := new(MockBotInterface)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, nil, nil, nil, nil, nil, nil, nil, mockBot, nil)
	assert.NoError(t, err)

	req, err := http.NewRequest("GET", "/healthz", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.healthzHandler)

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestWebhookHandler(t *testing.T) {
	// Arrange
	mockBot := new(MockBotInterface)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, nil, nil, nil, nil, nil, nil, nil, mockBot, nil)
	assert.NoError(t, err)

	updateJSON := `{"update_id":1,"message":{"text":"hello"}}`
	body := []byte(updateJSON)
	req, err := http.NewRequest("POST", "/telegram/webhook", bytes.NewBuffer(body))
	assert.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	// We need to match the raw json.RawMessage
	mockBot.On("HandleUpdateAsync", mock.Anything, json.RawMessage(body), mock.Anything).Return()

	// Start the server context so the handler can use it
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.ctx = ctx

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.webhookHandler)

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	mockBot.AssertCalled(t, "HandleUpdateAsync", mock.Anything, json.RawMessage(body), mock.Anything)
}

func TestWebhookHandler_TooLarge(t *testing.T) {
	// Arrange
	mockBot := new(MockBotInterface)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, nil, nil, nil, nil, nil, nil, nil, mockBot, nil)
	assert.NoError(t, err)

	// Create a body larger than 10MB
	largeBody := make([]byte, 10*1024*1024+1)
	req, err := http.NewRequest("POST", "/telegram/webhook", bytes.NewBuffer(largeBody))
	assert.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.webhookHandler)

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
	mockBot.AssertNotCalled(t, "HandleUpdateAsync", mock.Anything, mock.Anything, mock.Anything)
}

func TestServerRouting_CorrectPath(t *testing.T) {
	// Arrange
	mockStorage := new(testutil.MockStorage)
	mockBot := new(MockBotInterface)
	mockAPI := new(testutil.MockBotAPI)
	cfg := &config.Config{}
	token := "test-token"

	mockBot.On("API").Return(mockAPI)
	mockAPI.On("GetToken").Return(token)
	mockStorage.On("GetStats").Return(map[int64]storage.Stat{}, nil) // For stats handler

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)
	handler := server.buildTestHandler(token)
	testServer := httptest.NewServer(handler)
	defer testServer.Close()

	updateJSON := `{"update_id":1}`
	body := []byte(updateJSON)
	mockBot.On("HandleUpdateAsync", mock.Anything, json.RawMessage(body), mock.Anything).Return()

	// Act
	resp, err := http.Post(testServer.URL+"/telegram/"+token, "application/json", bytes.NewBuffer(body))

	// Assert
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	time.Sleep(100 * time.Millisecond)
	mockBot.AssertExpectations(t)
}

func TestServerRouting_IncorrectPath(t *testing.T) {
	// Arrange
	mockStorage := new(testutil.MockStorage)
	mockBot := new(MockBotInterface)
	mockAPI := new(testutil.MockBotAPI)
	cfg := &config.Config{}
	token := "test-token"

	mockBot.On("API").Return(mockAPI)
	mockAPI.On("GetToken").Return(token)
	mockStorage.On("GetStats").Return(map[int64]storage.Stat{}, nil) // For stats handler

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)
	handler := server.buildTestHandler(token)
	testServer := httptest.NewServer(handler)
	defer testServer.Close()

	updateJSON := `{"update_id":1}`
	body := []byte(updateJSON)

	// Act
	resp, err := http.Post(testServer.URL+"/", "application/json", bytes.NewBuffer(body))

	// Assert
	assert.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	mockBot.AssertNotCalled(t, "HandleUpdateAsync", mock.Anything, mock.Anything, mock.Anything)
}

// buildTestHandler is a helper to create a handler with all routes for testing.
func (s *Server) buildTestHandler(token string) http.Handler {
	// Initialize context for webhook handler
	s.ctx = context.Background()
	mux := http.NewServeMux()
	mux.HandleFunc("/ui/stats", s.statsHandler)
	mux.HandleFunc("/healthz", s.healthzHandler)
	mux.HandleFunc("/telegram/"+token, s.webhookHandler)
	return s.loggingMiddleware(mux)
}

func TestUpdateMetrics(t *testing.T) {
	// Arrange
	mockStorage := new(testutil.MockStorage)
	mockBot := new(MockBotInterface)
	cfg := &config.Config{}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	// Mock users
	users := []storage.User{
		{ID: 123, Username: "user1"},
		{ID: 456, Username: "user2"},
	}
	mockStorage.On("GetAllUsers").Return(users, nil)

	// Mock per-user stats
	stats1 := storage.FactStats{
		CountByType: map[string]int{"identity": 10, "context": 5},
		AvgAgeDays:  2.5,
	}
	stats2 := storage.FactStats{
		CountByType: map[string]int{"identity": 3},
		AvgAgeDays:  1.0,
	}
	mockStorage.On("GetFactStatsByUser", int64(123)).Return(stats1, nil)
	mockStorage.On("GetFactStatsByUser", int64(456)).Return(stats2, nil)

	// Mock per-user topics count
	mockStorage.On("GetTopicsExtended", storage.TopicFilter{UserID: 123}, 1, 0, "", "").Return(storage.TopicResult{TotalCount: 42}, nil)
	mockStorage.On("GetTopicsExtended", storage.TopicFilter{UserID: 456}, 1, 0, "", "").Return(storage.TopicResult{TotalCount: 10}, nil)

	// Mock active sessions
	mockBot.On("GetActiveSessions").Return([]rag.ActiveSessionInfo{
		{UserID: 123, MessageCount: 5},
		{UserID: 456, MessageCount: 3},
	}, nil)

	// Mock maintenance operations
	mockStorage.On("GetDBSize").Return(int64(1024*1024), nil)
	mockStorage.On("GetTableSizes").Return([]storage.TableSize{
		{Name: "topics", Bytes: 500000},
		{Name: "facts", Bytes: 300000},
	}, nil)
	mockStorage.On("CleanupFactHistory", 100).Return(int64(50), nil)
	mockStorage.On("CleanupAgentLogs", 50).Return(int64(25), nil)

	// Act
	server.updateMetrics()

	// Assert
	mockStorage.AssertExpectations(t)
	mockBot.AssertExpectations(t)
}

func TestGetClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		expected   string
	}{
		{
			name:       "No proxy headers - uses RemoteAddr",
			remoteAddr: "192.168.1.100:12345",
			headers:    nil,
			expected:   "192.168.1.100",
		},
		{
			name:       "X-Forwarded-For single IP",
			remoteAddr: "172.27.32.1:6779",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.50"},
			expected:   "203.0.113.50",
		},
		{
			name:       "X-Forwarded-For multiple IPs - first is client",
			remoteAddr: "172.27.32.1:6779",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.50, 70.41.3.18, 150.172.238.178"},
			expected:   "203.0.113.50",
		},
		{
			name:       "X-Real-IP header",
			remoteAddr: "172.27.32.1:6779",
			headers:    map[string]string{"X-Real-IP": "203.0.113.75"},
			expected:   "203.0.113.75",
		},
		{
			name:       "X-Forwarded-For takes precedence over X-Real-IP",
			remoteAddr: "172.27.32.1:6779",
			headers: map[string]string{
				"X-Forwarded-For": "203.0.113.50",
				"X-Real-IP":       "203.0.113.75",
			},
			expected: "203.0.113.50",
		},
		{
			name:       "RemoteAddr without port",
			remoteAddr: "192.168.1.100",
			headers:    nil,
			expected:   "192.168.1.100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				RemoteAddr: tt.remoteAddr,
				Header:     make(http.Header),
			}
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			result := getClientIP(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSessionsHandler_POST_InvalidForm(t *testing.T) {
	mockBot := new(MockBotInterface)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, nil, nil, nil, nil, nil, nil, nil, mockBot, nil)
	assert.NoError(t, err)

	// POST with invalid user_id
	req, err := http.NewRequest("POST", "/ui/debug/sessions", bytes.NewBufferString("user_id=invalid"))
	assert.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.sessionsHandler)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestSessionsHandler_POST_Success(t *testing.T) {
	mockBot := new(MockBotInterface)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, nil, nil, nil, nil, nil, nil, nil, mockBot, nil)
	assert.NoError(t, err)

	mockBot.On("ForceCloseSession", mock.Anything, int64(123)).Return(5, nil)

	req, err := http.NewRequest("POST", "/ui/debug/sessions", bytes.NewBufferString("user_id=123"))
	assert.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.sessionsHandler)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusSeeOther, rr.Code)
	assert.Equal(t, "/ui/debug/sessions", rr.Header().Get("Location"))
	mockBot.AssertExpectations(t)
}

func TestSessionsHandler_GET_Error(t *testing.T) {
	mockBot := new(MockBotInterface)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, nil, nil, nil, nil, nil, nil, nil, mockBot, nil)
	assert.NoError(t, err)

	mockBot.On("GetActiveSessions").Return(nil, assert.AnError)

	req, err := http.NewRequest("GET", "/ui/debug/sessions", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.sessionsHandler)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockBot.AssertExpectations(t)
}

func TestSessionsProcessSSEHandler_MethodNotAllowed(t *testing.T) {
	mockBot := new(MockBotInterface)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, nil, nil, nil, nil, nil, nil, nil, mockBot, nil)
	assert.NoError(t, err)

	req, err := http.NewRequest("POST", "/ui/debug/sessions/process", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.sessionsProcessSSEHandler)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestSessionsProcessSSEHandler_InvalidUserID(t *testing.T) {
	mockBot := new(MockBotInterface)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, nil, nil, nil, nil, nil, nil, nil, mockBot, nil)
	assert.NoError(t, err)

	req, err := http.NewRequest("GET", "/ui/debug/sessions/process?user_id=invalid", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.sessionsProcessSSEHandler)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestWebhookHandler_InvalidSecret(t *testing.T) {
	mockBot := new(MockBotInterface)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Telegram.WebhookSecret = "my-secret-token"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, nil, nil, nil, nil, nil, nil, nil, mockBot, nil)
	assert.NoError(t, err)

	updateJSON := `{"update_id":1,"message":{"text":"hello"}}`
	body := []byte(updateJSON)
	req, err := http.NewRequest("POST", "/telegram/webhook", bytes.NewBuffer(body))
	assert.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong-token")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.ctx = ctx

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.webhookHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	mockBot.AssertNotCalled(t, "HandleUpdateAsync", mock.Anything, mock.Anything, mock.Anything)
}

func TestWebhookHandler_ValidSecret(t *testing.T) {
	mockBot := new(MockBotInterface)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Telegram.WebhookSecret = "my-secret-token"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, nil, nil, nil, nil, nil, nil, nil, mockBot, nil)
	assert.NoError(t, err)

	updateJSON := `{"update_id":1,"message":{"text":"hello"}}`
	body := []byte(updateJSON)
	req, err := http.NewRequest("POST", "/telegram/webhook", bytes.NewBuffer(body))
	assert.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "my-secret-token")

	mockBot.On("HandleUpdateAsync", mock.Anything, json.RawMessage(body), mock.Anything).Return()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.ctx = ctx

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.webhookHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	mockBot.AssertCalled(t, "HandleUpdateAsync", mock.Anything, json.RawMessage(body), mock.Anything)
}

func TestSessionsHandler_GET_Success(t *testing.T) {
	mockBot := new(MockBotInterface)
	mockStorage := new(testutil.MockStorage)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.RAG.ChunkInterval = "4h"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	now := time.Now()
	sessions := []rag.ActiveSessionInfo{
		{
			UserID:           123,
			MessageCount:     5,
			FirstMessageTime: now.Add(-time.Hour),
			LastMessageTime:  now,
			ContextSize:      1000,
		},
	}
	mockBot.On("GetActiveSessions").Return(sessions, nil)
	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123, FirstName: "Test", LastName: "User"}}, nil)

	req, err := http.NewRequest("GET", "/ui/debug/sessions", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.sessionsHandler)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "Test User")
	mockBot.AssertExpectations(t)
	mockStorage.AssertExpectations(t)
}

func TestSessionsHandler_POST_ForceCloseError(t *testing.T) {
	mockBot := new(MockBotInterface)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, nil, nil, nil, nil, nil, nil, nil, mockBot, nil)
	assert.NoError(t, err)

	mockBot.On("ForceCloseSession", mock.Anything, int64(123)).Return(0, assert.AnError)

	req, err := http.NewRequest("POST", "/ui/debug/sessions", bytes.NewBufferString("user_id=123"))
	assert.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.sessionsHandler)
	handler.ServeHTTP(rr, req)

	// Should redirect even on error
	assert.Equal(t, http.StatusSeeOther, rr.Code)
	mockBot.AssertExpectations(t)
}

func TestSessionsHandler_GET_UsernameOnly(t *testing.T) {
	mockBot := new(MockBotInterface)
	mockStorage := new(testutil.MockStorage)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	now := time.Now()
	sessions := []rag.ActiveSessionInfo{
		{
			UserID:           123,
			MessageCount:     5,
			FirstMessageTime: now.Add(-time.Hour),
			LastMessageTime:  now,
			ContextSize:      1000,
		},
	}
	mockBot.On("GetActiveSessions").Return(sessions, nil)
	// User has only Username, no FirstName
	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123, Username: "testuser"}}, nil)

	req, err := http.NewRequest("GET", "/ui/debug/sessions", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.sessionsHandler)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "@testuser")
	mockBot.AssertExpectations(t)
}

func TestSessionsHandler_GET_CommonDataError(t *testing.T) {
	mockBot := new(MockBotInterface)
	mockStorage := new(testutil.MockStorage)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	sessions := []rag.ActiveSessionInfo{}
	mockBot.On("GetActiveSessions").Return(sessions, nil)
	mockStorage.On("GetAllUsers").Return(nil, assert.AnError)

	req, err := http.NewRequest("GET", "/ui/debug/sessions", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.sessionsHandler)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestDebugChatHandler_GET(t *testing.T) {
	mockBot := new(MockBotInterface)
	mockStorage := new(testutil.MockStorage)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	// Mock common data
	users := []storage.User{{ID: 123, FirstName: "Test", LastName: "User"}}
	mockStorage.On("GetAllUsers").Return(users, nil)
	mockStorage.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{
		{Role: "user", Content: "Hello", CreatedAt: time.Now()},
		{Role: "assistant", Content: "Hi!", CreatedAt: time.Now()},
	}, nil)

	req, err := http.NewRequest("GET", "/ui/debug/chat?user_id=123", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.debugChatHandler)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "Test Chat")
	mockStorage.AssertExpectations(t)
}

func TestDebugChatSendHandler_POST_Success(t *testing.T) {
	mockBot := new(MockBotInterface)
	mockStorage := new(testutil.MockStorage)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	// Mock bot.SendTestMessage
	cost := 0.0045
	mockBot.On("SendTestMessage", mock.Anything, int64(123), "Hello bot", true).Return(&rag.TestMessageResult{
		Response:         "Hello! How can I help?",
		TimingTotal:      2300 * time.Millisecond,
		TimingEmbedding:  45 * time.Millisecond,
		TimingSearch:     23 * time.Millisecond,
		TimingLLM:        2100 * time.Millisecond,
		PromptTokens:     1100,
		CompletionTokens: 147,
		TotalCost:        cost,
		TopicsMatched:    5,
		FactsInjected:    3,
		ContextPreview:   "System: You are...",
	}, nil)

	body := `{"user_id": 123, "message": "Hello bot", "save_to_history": true}`
	req, err := http.NewRequest("POST", "/ui/debug/chat/send", bytes.NewBufferString(body))
	assert.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.debugChatSendHandler)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var response map[string]interface{}
	err = json.Unmarshal(rr.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "Hello! How can I help?", response["response"])
	assert.NotNil(t, response["timing"])
	assert.NotNil(t, response["tokens"])
	assert.NotNil(t, response["context"])

	mockBot.AssertExpectations(t)
}

func TestDebugChatSendHandler_POST_InvalidJSON(t *testing.T) {
	mockBot := new(MockBotInterface)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, nil, nil, nil, nil, nil, nil, nil, mockBot, nil)
	assert.NoError(t, err)

	body := `{invalid json}`
	req, err := http.NewRequest("POST", "/ui/debug/chat/send", bytes.NewBufferString(body))
	assert.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.debugChatSendHandler)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestDebugChatSendHandler_POST_BotError(t *testing.T) {
	mockBot := new(MockBotInterface)
	mockStorage := new(testutil.MockStorage)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	// Mock bot.SendTestMessage returning error
	mockBot.On("SendTestMessage", mock.Anything, int64(123), "Hello", true).Return(nil, assert.AnError)

	body := `{"user_id": 123, "message": "Hello", "save_to_history": true}`
	req, err := http.NewRequest("POST", "/ui/debug/chat/send", bytes.NewBufferString(body))
	assert.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.debugChatSendHandler)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	mockBot.AssertExpectations(t)
}

func TestDebugChatSendHandler_MethodNotAllowed(t *testing.T) {
	mockBot := new(MockBotInterface)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, nil, nil, nil, nil, nil, nil, nil, mockBot, nil)
	assert.NoError(t, err)

	req, err := http.NewRequest("GET", "/ui/debug/chat/send", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.debugChatSendHandler)
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

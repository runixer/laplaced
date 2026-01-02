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
	"github.com/runixer/laplaced/internal/telegram"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockBotAPI is a mock for the telegram.BotAPI interface
type MockBotAPI struct {
	mock.Mock
}

func (m *MockBotAPI) SendMessage(ctx context.Context, req telegram.SendMessageRequest) (*telegram.Message, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*telegram.Message), args.Error(1)
}

func (m *MockBotAPI) SetMyCommands(ctx context.Context, req telegram.SetMyCommandsRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockBotAPI) SetWebhook(ctx context.Context, req telegram.SetWebhookRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockBotAPI) SendChatAction(ctx context.Context, req telegram.SendChatActionRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockBotAPI) GetFile(ctx context.Context, req telegram.GetFileRequest) (*telegram.File, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*telegram.File), args.Error(1)
}

func (m *MockBotAPI) SetMessageReaction(ctx context.Context, req telegram.SetMessageReactionRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockBotAPI) GetToken() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockBotAPI) GetUpdates(ctx context.Context, req telegram.GetUpdatesRequest) ([]telegram.Update, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]telegram.Update), args.Error(1)
}

// MockStorage is a mock type for the Storage interface
type MockStorage struct {
	mock.Mock
}

func (m *MockStorage) AddMessageToHistory(userID int64, message storage.Message) error {
	args := m.Called(userID, message)
	return args.Error(0)
}

func (m *MockStorage) ImportMessage(userID int64, message storage.Message) error {
	args := m.Called(userID, message)
	return args.Error(0)
}

func (m *MockStorage) GetHistory(userID int64) ([]storage.Message, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) GetRecentHistory(userID int64, limit int) ([]storage.Message, error) {
	args := m.Called(userID, limit)
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) GetMessagesByIDs(ids []int64) ([]storage.Message, error) {
	args := m.Called(ids)
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) GetFactsByIDs(ids []int64) ([]storage.Fact, error) {
	args := m.Called(ids)
	return args.Get(0).([]storage.Fact), args.Error(1)
}

func (m *MockStorage) ClearHistory(userID int64) error {
	args := m.Called(userID)
	return args.Error(0)
}

func (m *MockStorage) AddStat(stat storage.Stat) error {
	args := m.Called(stat)
	return args.Error(0)
}

func (m *MockStorage) GetStats() (map[int64]storage.Stat, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[int64]storage.Stat), args.Error(1)
}

func (m *MockStorage) GetDashboardStats(userID int64) (*storage.DashboardStats, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.DashboardStats), args.Error(1)
}

func (m *MockStorage) AddRAGLog(log storage.RAGLog) error {
	args := m.Called(log)
	return args.Error(0)
}

func (m *MockStorage) GetRAGLogs(userID int64, limit int) ([]storage.RAGLog, error) {
	args := m.Called(userID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.RAGLog), args.Error(1)
}

func (m *MockStorage) AddTopic(topic storage.Topic) (int64, error) {
	args := m.Called(topic)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) CreateTopic(topic storage.Topic) (int64, error) {
	args := m.Called(topic)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) ResetUserData(userID int64) error {
	args := m.Called(userID)
	return args.Error(0)
}

func (m *MockStorage) DeleteTopic(id int64) error {
	args := m.Called(id)
	return args.Error(0)
}

func (m *MockStorage) DeleteTopicCascade(id int64) error {
	args := m.Called(id)
	return args.Error(0)
}

func (m *MockStorage) GetLastTopicEndMessageID(userID int64) (int64, error) {
	args := m.Called(userID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) GetAllTopics() ([]storage.Topic, error) {
	args := m.Called()
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopicsByIDs(ids []int64) ([]storage.Topic, error) {
	args := m.Called(ids)
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopicsAfterID(minID int64) ([]storage.Topic, error) {
	args := m.Called(minID)
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopics(userID int64) ([]storage.Topic, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopicsPendingFacts(userID int64) ([]storage.Topic, error) {
	args := m.Called(userID)
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopicsExtended(filter storage.TopicFilter, limit, offset int, sortBy, sortDir string) (storage.TopicResult, error) {
	args := m.Called(filter, limit, offset, sortBy, sortDir)
	return args.Get(0).(storage.TopicResult), args.Error(1)
}

func (m *MockStorage) UpdateMessageTopic(messageID, topicID int64) error {
	args := m.Called(messageID, topicID)
	return args.Error(0)
}

func (m *MockStorage) SetTopicFactsExtracted(topicID int64, extracted bool) error {
	args := m.Called(topicID, extracted)
	return args.Error(0)
}

func (m *MockStorage) SetTopicConsolidationChecked(topicID int64, checked bool) error {
	args := m.Called(topicID, checked)
	return args.Error(0)
}

func (m *MockStorage) GetMessagesInRange(ctx context.Context, userID int64, startID, endID int64) ([]storage.Message, error) {
	args := m.Called(ctx, userID, startID, endID)
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) GetMemoryBank(userID int64) (string, error) {
	args := m.Called(userID)
	return args.String(0), args.Error(1)
}

func (m *MockStorage) UpdateMemoryBank(userID int64, content string) error {
	args := m.Called(userID, content)
	return args.Error(0)
}

func (m *MockStorage) UpsertUser(user storage.User) error {
	args := m.Called(user)
	return args.Error(0)
}

func (m *MockStorage) GetAllUsers() ([]storage.User, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.User), args.Error(1)
}

func (m *MockStorage) UpdateFact(fact storage.Fact) error {
	args := m.Called(fact)
	return args.Error(0)
}

func (m *MockStorage) UpdateFactTopic(oldTopicID, newTopicID int64) error {
	args := m.Called(oldTopicID, newTopicID)
	return args.Error(0)
}

func (m *MockStorage) DeleteFact(userID, factID int64) error {
	args := m.Called(userID, factID)
	return args.Error(0)
}

func (m *MockStorage) AddFact(fact storage.Fact) (int64, error) {
	args := m.Called(fact)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) AddFactHistory(history storage.FactHistory) error {
	args := m.Called(history)
	return args.Error(0)
}

func (m *MockStorage) UpdateFactHistoryTopic(oldTopicID, newTopicID int64) error {
	args := m.Called(oldTopicID, newTopicID)
	return args.Error(0)
}

func (m *MockStorage) GetFactHistory(userID int64, limit int) ([]storage.FactHistory, error) {
	args := m.Called(userID, limit)
	return args.Get(0).([]storage.FactHistory), args.Error(1)
}

func (m *MockStorage) GetFactHistoryExtended(filter storage.FactHistoryFilter, limit, offset int, sortBy, sortDir string) (storage.FactHistoryResult, error) {
	args := m.Called(filter, limit, offset, sortBy, sortDir)
	return args.Get(0).(storage.FactHistoryResult), args.Error(1)
}

func (m *MockStorage) GetAllFacts() ([]storage.Fact, error) {
	args := m.Called()
	return args.Get(0).([]storage.Fact), args.Error(1)
}

func (m *MockStorage) GetFactsAfterID(minID int64) ([]storage.Fact, error) {
	args := m.Called(minID)
	return args.Get(0).([]storage.Fact), args.Error(1)
}

func (m *MockStorage) GetFactStats() (storage.FactStats, error) {
	args := m.Called()
	return args.Get(0).(storage.FactStats), args.Error(1)
}

func (m *MockStorage) GetFacts(userID int64) ([]storage.Fact, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return []storage.Fact{}, args.Error(1)
	}
	return args.Get(0).([]storage.Fact), args.Error(1)
}

func (m *MockStorage) GetUnprocessedMessages(userID int64) ([]storage.Message, error) {
	args := m.Called(userID)
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) GetTopicExtractionLogs(limit, offset int) ([]storage.RAGLog, int, error) {
	args := m.Called(limit, offset)
	return args.Get(0).([]storage.RAGLog), args.Int(1), args.Error(2)
}

func (m *MockStorage) GetMergeCandidates(userID int64) ([]storage.MergeCandidate, error) {
	args := m.Called(userID)
	return args.Get(0).([]storage.MergeCandidate), args.Error(1)
}

// MockBot is a mock type for the Bot interface
type MockBot struct {
	mock.Mock
}

func (m *MockBot) API() telegram.BotAPI {
	args := m.Called()
	return args.Get(0).(telegram.BotAPI)
}

func (m *MockBot) HandleUpdateAsync(ctx context.Context, update json.RawMessage, remoteAddr string) {
	m.Called(ctx, update, remoteAddr)
}

func (m *MockBot) GetActiveSessions() ([]rag.ActiveSessionInfo, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]rag.ActiveSessionInfo), args.Error(1)
}

func (m *MockBot) ForceCloseSession(ctx context.Context, userID int64) (int, error) {
	args := m.Called(ctx, userID)
	return args.Int(0), args.Error(1)
}

func (m *MockBot) ForceCloseSessionWithProgress(ctx context.Context, userID int64, onProgress rag.ProgressCallback) (*rag.ProcessingStats, error) {
	args := m.Called(ctx, userID, onProgress)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*rag.ProcessingStats), args.Error(1)
}

func (m *MockBot) SendTestMessage(ctx context.Context, userID int64, text string, saveToHistory bool) (*rag.TestMessageResult, error) {
	args := m.Called(ctx, userID, text, saveToHistory)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*rag.TestMessageResult), args.Error(1)
}

func TestStatsHandler(t *testing.T) {
	// Arrange
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

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
	mockBot := new(MockBot)
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
	mockBot := new(MockBot)
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
	mockBot := new(MockBot)
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
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)
	mockAPI := new(MockBotAPI)
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
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)
	mockAPI := new(MockBotAPI)
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

func TestFactsHistoryHandler(t *testing.T) {
	// Arrange
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true // Enable debug routes
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	topicID := int64(123)
	history := []storage.FactHistory{
		{
			ID: 1, FactID: 1, UserID: 1, Action: "add", NewContent: "test", TopicID: &topicID,
		},
		{
			ID: 2, FactID: 2, UserID: 1, Action: "update", OldContent: "old", NewContent: "new", TopicID: nil,
		},
	}
	result := storage.FactHistoryResult{
		Data:       history,
		TotalCount: 2,
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 1, Username: "user1"}}, nil)
	mockStorage.On("GetFactHistoryExtended", mock.Anything, 50, 0, "", "DESC").Return(result, nil)

	req, err := http.NewRequest("GET", "/ui/facts/history", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.factsHistoryHandler)

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "#123") // Check if TopicID link is rendered
	mockStorage.AssertExpectations(t)
}

func TestTopicsHandler(t *testing.T) {
	// Arrange
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)
	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	topics := []storage.Topic{
		{ID: 1, UserID: 123, Summary: "Topic 1", StartMsgID: 1, EndMsgID: 2, CreatedAt: time.Now()},
	}
	messages := []storage.Message{
		{ID: 1, UserID: 123, Role: "user", Content: "Hello", TopicID: &topics[0].ID},
		{ID: 2, UserID: 123, Role: "assistant", Content: "Hi", TopicID: &topics[0].ID},
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123, Username: "testuser"}}, nil)

	result := storage.TopicResult{
		Data: []storage.TopicExtended{
			{Topic: topics[0], MessageCount: 2, FactsCount: 0},
		},
		TotalCount: 1,
	}
	mockStorage.On("GetTopicsExtended", mock.Anything, 20, 0, mock.Anything, "DESC").Return(result, nil)

	mockStorage.On("GetMessagesInRange", mock.Anything, int64(123), int64(1), int64(2)).Return(messages, nil)

	req, err := http.NewRequest("GET", "/ui/topics", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.topicsHandler)

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "Topic 1")
	assert.Contains(t, rr.Body.String(), "Hello")
	// Check if the accordion ID is rendered correctly (this was the bug)
	// The bug was data-bs-parent="#accordion-{{$.ID}}" failing.
	// If it renders, it means the template execution succeeded.
	// We can also check if the correct ID is in the output.
	assert.Contains(t, rr.Body.String(), `data-bs-parent="#accordion-1"`)

	mockStorage.AssertExpectations(t)
}

func TestUpdateMetrics(t *testing.T) {
	// Arrange
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)
	cfg := &config.Config{}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	stats := storage.FactStats{
		CountByType: map[string]int{
			"identity": 10,
			"context":  5,
		},
		AvgAgeDays: 2.5,
	}

	mockStorage.On("GetFactStats").Return(stats, nil)
	mockBot.On("GetActiveSessions").Return([]rag.ActiveSessionInfo{
		{UserID: 1, MessageCount: 5},
		{UserID: 2, MessageCount: 3},
	}, nil)

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
	mockBot := new(MockBot)
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
	mockBot := new(MockBot)
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
	mockBot := new(MockBot)
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
	mockBot := new(MockBot)
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
	mockBot := new(MockBot)
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
	mockBot := new(MockBot)
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
	mockBot := new(MockBot)
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
	mockBot := new(MockBot)
	mockStorage := new(MockStorage)
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
	mockBot := new(MockBot)
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
	mockBot := new(MockBot)
	mockStorage := new(MockStorage)
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
	mockBot := new(MockBot)
	mockStorage := new(MockStorage)
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
	mockBot := new(MockBot)
	mockStorage := new(MockStorage)
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
	mockBot := new(MockBot)
	mockStorage := new(MockStorage)
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
	mockBot := new(MockBot)
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
	mockBot := new(MockBot)
	mockStorage := new(MockStorage)
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
	mockBot := new(MockBot)
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

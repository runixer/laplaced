package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/storage"

	"github.com/stretchr/testify/assert"
)

func TestInspectorHandler_NilParsedResults(t *testing.T) {
	// Arrange
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true // Enable debug mode

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	// Mock data
	// Return a log with empty RetrievalResults, which causes ParsedResults to be nil
	logs := []storage.RAGLog{
		{
			UserID:           123,
			RetrievalResults: "", // Empty results
		},
	}
	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123, Username: "testuser"}}, nil)
	mockStorage.On("GetRAGLogs", int64(0), 50).Return(logs, nil)

	req, err := http.NewRequest("GET", "/ui/inspector", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.inspectorHandler)

	// Act
	handler.ServeHTTP(rr, req)

	// Assert
	assert.Equal(t, http.StatusOK, rr.Code)
	// Check if the page title is present
	assert.Contains(t, rr.Body.String(), "Context Inspector")
}

func TestDebugRerankerHandler_GET(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123, Username: "testuser"}}, nil)
	mockStorage.On("GetRerankerLogs", int64(0), 20).Return([]storage.RerankerLog{}, nil)

	req, err := http.NewRequest("GET", "/ui/debug/reranker", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.debugRerankerHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "Reranker Debug")
}

func TestTopicDebugHandler(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	// ContextUsed contains JSON with message IDs, LLMResponse contains topics
	logs := []storage.RAGLog{
		{
			ID:          1,
			UserID:      123,
			ContextUsed: `[{"id": 10, "content": "test"}]`,
			LLMResponse: `{"topics": [{"summary": "Test topic"}]}`,
		},
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetTopicExtractionLogs", 20, 0).Return(logs, 1, nil)
	mockStorage.On("GetMessagesByIDs", []int64{int64(10)}).Return([]storage.Message{{ID: 10, CreatedAt: time.Now()}}, nil)

	req, err := http.NewRequest("GET", "/ui/debug/topics", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.topicDebugHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "Topic Extraction")
}

func TestTopicDebugHandler_Pagination(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	mockStorage.On("GetAllUsers").Return([]storage.User{}, nil)
	mockStorage.On("GetTopicExtractionLogs", 20, 20).Return([]storage.RAGLog{}, 100, nil)

	req, err := http.NewRequest("GET", "/ui/debug/topics?page=2", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.topicDebugHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestFactsHandler(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	facts := []storage.Fact{
		{ID: 1, UserID: 123, Content: "Test fact 1", Importance: 80, CreatedAt: time.Now()},
		{ID: 2, UserID: 123, Content: "Test fact 2", Importance: 90, CreatedAt: time.Now()},
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetAllFacts").Return(facts, nil)

	req, err := http.NewRequest("GET", "/ui/facts", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.factsHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "Test fact 1")
	assert.Contains(t, rr.Body.String(), "Test fact 2")
}

func TestFactsHandler_WithUserID(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	facts := []storage.Fact{
		{ID: 1, UserID: 123, Content: "User specific fact", Importance: 80, CreatedAt: time.Now()},
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetFacts", int64(123)).Return(facts, nil)

	req, err := http.NewRequest("GET", "/ui/facts?user_id=123", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.factsHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "User specific fact")
}

func TestFactsHandler_SortByImportance(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	now := time.Now()
	facts := []storage.Fact{
		{ID: 1, Content: "Low importance", Importance: 10, CreatedAt: now},
		{ID: 2, Content: "High importance", Importance: 90, CreatedAt: now.Add(-time.Hour)},
		{ID: 3, Content: "Medium importance", Importance: 50, CreatedAt: now},
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{}, nil)
	mockStorage.On("GetAllFacts").Return(facts, nil)

	req, err := http.NewRequest("GET", "/ui/facts", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.factsHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	body := rr.Body.String()
	// High importance should appear before low importance
	highPos := strings.Index(body, "High importance")
	lowPos := strings.Index(body, "Low importance")
	assert.True(t, highPos < lowPos, "High importance should appear before low importance")
}

func TestInspectorHandler_WithUserID(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	logs := []storage.RAGLog{
		{
			UserID:        456,
			OriginalQuery: "user query",
		},
	}
	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 456, Username: "specificuser"}}, nil)
	mockStorage.On("GetRAGLogs", int64(456), 50).Return(logs, nil)

	req, err := http.NewRequest("GET", "/ui/inspector?user_id=456", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.inspectorHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "Context Inspector")
}

func TestTopicsHandler_WithFilters(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	hasFacts := true
	isConsolidated := false

	result := storage.TopicResult{
		Data: []storage.TopicExtended{
			{Topic: storage.Topic{ID: 1, UserID: 123, Summary: "Test topic with facts"}},
		},
		TotalCount: 1,
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetTopicsExtended", storage.TopicFilter{
		UserID:         int64(123),
		Search:         "",
		HasFacts:       &hasFacts,
		IsConsolidated: &isConsolidated,
		TopicID:        (*int64)(nil),
	}, 20, 0, "", "DESC").Return(result, nil)
	mockStorage.On("GetMessagesByTopicID", context.Background(), int64(1)).Return([]storage.Message{}, nil)

	req, err := http.NewRequest("GET", "/ui/topics?user_id=123&has_facts=true&merged=false", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.topicsHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "Test topic with facts")
}

func TestTopicsHandler_Pagination(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	result := storage.TopicResult{
		Data:       []storage.TopicExtended{},
		TotalCount: 100,
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{}, nil)
	mockStorage.On("GetTopicsExtended", storage.TopicFilter{
		UserID:         int64(0),
		Search:         "",
		HasFacts:       (*bool)(nil),
		IsConsolidated: (*bool)(nil),
		TopicID:        (*int64)(nil),
	}, 20, 40, "created_at", "ASC").Return(result, nil)

	req, err := http.NewRequest("GET", "/ui/topics?page=3&sort=created_at&dir=ASC", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.topicsHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestTopicsHandler_NegativePage(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	result := storage.TopicResult{
		Data:       []storage.TopicExtended{},
		TotalCount: 0,
	}

	// Negative page should be treated as page 1 (offset 0)
	mockStorage.On("GetAllUsers").Return([]storage.User{}, nil)
	mockStorage.On("GetTopicsExtended", storage.TopicFilter{
		UserID:         int64(0),
		Search:         "",
		HasFacts:       (*bool)(nil),
		IsConsolidated: (*bool)(nil),
		TopicID:        (*int64)(nil),
	}, 20, 0, "", "DESC").Return(result, nil)

	req, err := http.NewRequest("GET", "/ui/topics?page=-5", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.topicsHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestTopicDebugHandler_NegativePage(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	// Negative page should be treated as page 1 (offset 0)
	mockStorage.On("GetAllUsers").Return([]storage.User{}, nil)
	mockStorage.On("GetTopicExtractionLogs", 20, 0).Return([]storage.RAGLog{}, 0, nil)

	req, err := http.NewRequest("GET", "/ui/debug/topics?page=-10", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.topicDebugHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestFactsHistoryHandler_WithFilters(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	result := storage.FactHistoryResult{
		Data: []storage.FactHistory{
			{ID: 1, UserID: 123, Action: "add", NewContent: "Test fact"},
		},
		TotalCount: 1,
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetFactHistoryExtended", storage.FactHistoryFilter{
		UserID:   int64(123),
		Action:   "add",
		Category: "profile",
		Search:   "test",
	}, 50, 0, "created_at", "ASC").Return(result, nil)

	req, err := http.NewRequest("GET", "/ui/facts/history?user_id=123&action=add&category=profile&search=test&sort=created_at&dir=ASC", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.factsHistoryHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "Test fact")
}

func TestFactsHistoryHandler_Pagination(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	result := storage.FactHistoryResult{
		Data:       []storage.FactHistory{},
		TotalCount: 200,
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{}, nil)
	mockStorage.On("GetFactHistoryExtended", storage.FactHistoryFilter{
		UserID:   int64(0),
		Action:   "",
		Category: "",
		Search:   "",
	}, 50, 100, "", "DESC").Return(result, nil)

	req, err := http.NewRequest("GET", "/ui/facts/history?page=3", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.factsHistoryHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestTopicsHandler_WithSearch(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	topicID := int64(42)
	result := storage.TopicResult{
		Data: []storage.TopicExtended{
			{Topic: storage.Topic{ID: 42, UserID: 123, Summary: "Specific topic"}},
		},
		TotalCount: 1,
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}}, nil)
	mockStorage.On("GetTopicsExtended", storage.TopicFilter{
		UserID:         int64(0),
		Search:         "search term",
		HasFacts:       (*bool)(nil),
		IsConsolidated: (*bool)(nil),
		TopicID:        &topicID,
	}, 20, 0, "", "DESC").Return(result, nil)
	mockStorage.On("GetMessagesByTopicID", context.Background(), int64(42)).Return([]storage.Message{}, nil)

	req, err := http.NewRequest("GET", "/ui/topics?q=search+term&topic_id=42", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.topicsHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "Specific topic")
}

func TestStatsHandler_MultiUser(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	stats := map[int64]storage.Stat{
		123: {UserID: 123, TokensUsed: 1000, CostUSD: 0.05},
		456: {UserID: 456, TokensUsed: 2000, CostUSD: 0.10},
	}
	dashboardStats := &storage.DashboardStats{
		TotalMessages: 100,
		TotalTopics:   10,
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}, {ID: 456}}, nil)
	mockStorage.On("GetStats").Return(stats, nil)
	mockStorage.On("GetDashboardStats", int64(0)).Return(dashboardStats, nil)

	req, err := http.NewRequest("GET", "/ui/stats", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.statsHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "Stats")
}

func TestStatsHandler_WithUserFilter(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	stats := map[int64]storage.Stat{
		123: {UserID: 123, TokensUsed: 1000, CostUSD: 0.05},
		456: {UserID: 456, TokensUsed: 2000, CostUSD: 0.10},
	}
	dashboardStats := &storage.DashboardStats{
		TotalMessages: 50,
		TotalTopics:   5,
	}

	mockStorage.On("GetAllUsers").Return([]storage.User{{ID: 123}, {ID: 456}}, nil)
	mockStorage.On("GetStats").Return(stats, nil)
	mockStorage.On("GetDashboardStats", int64(123)).Return(dashboardStats, nil)

	req, err := http.NewRequest("GET", "/ui/stats?user_id=123", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.statsHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestFactsHandler_Error(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	mockStorage.On("GetAllUsers").Return([]storage.User{}, nil)
	mockStorage.On("GetAllFacts").Return([]storage.Fact{}, assert.AnError)

	req, err := http.NewRequest("GET", "/ui/facts", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.factsHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestInspectorHandler_Error(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	mockStorage.On("GetAllUsers").Return([]storage.User{}, nil)
	mockStorage.On("GetRAGLogs", int64(0), 50).Return([]storage.RAGLog{}, assert.AnError)

	req, err := http.NewRequest("GET", "/ui/inspector", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.inspectorHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestTopicsHandler_Error(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	mockStorage.On("GetAllUsers").Return([]storage.User{}, nil)
	mockStorage.On("GetTopicsExtended", storage.TopicFilter{
		UserID:         int64(0),
		Search:         "",
		HasFacts:       (*bool)(nil),
		IsConsolidated: (*bool)(nil),
		TopicID:        (*int64)(nil),
	}, 20, 0, "", "DESC").Return(storage.TopicResult{}, assert.AnError)

	req, err := http.NewRequest("GET", "/ui/topics", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.topicsHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestStatsHandler_Error(t *testing.T) {
	mockStorage := new(MockStorage)
	mockBot := new(MockBot)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.Server.DebugMode = true

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
	assert.NoError(t, err)

	mockStorage.On("GetAllUsers").Return([]storage.User{}, nil)
	mockStorage.On("GetStats").Return(map[int64]storage.Stat{}, assert.AnError)

	req, err := http.NewRequest("GET", "/ui/stats", nil)
	assert.NoError(t, err)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.statsHandler)

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

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
	server, err := NewServer(logger, cfg, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockStorage, mockBot, nil)
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

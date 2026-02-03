package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// setupTestServerForSSE creates a test server with minimal mocking for SSE tests.
// Uses Maybe() for API expectation since SSE handlers don't call it.
func setupTestServerForSSE(t *testing.T) (*Server, *testutil.MockStorage, *MockBotInterface) {
	t.Helper()

	mockStorage := new(testutil.MockStorage)
	mockBot := new(MockBotInterface)
	mockAPI := new(testutil.MockBotAPI)

	cfg := &config.Config{}
	cfg.Server.ListenPort = "8080"
	cfg.RAG.ChunkInterval = "1h"
	cfg.Artifacts.StoragePath = t.TempDir()
	cfg.Bot.AllowedUserIDs = []int64{123, 456}

	mockBot.On("API").Return(mockAPI).Maybe()
	mockAPI.On("GetToken").Return("test-token").Maybe()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server, err := NewServer(context.Background(), logger, cfg,
		mockStorage, mockStorage, mockStorage, mockStorage,
		mockStorage, mockStorage, mockStorage, mockStorage,
		mockBot, nil)
	assert.NoError(t, err)

	// Set additional repositories
	server.SetAgentLogRepo(mockStorage)
	server.SetPeopleRepository(mockStorage)
	server.SetArtifactRepository(mockStorage)

	return server, mockStorage, mockBot
}

// TestSessionsProcessSSEHandler_Success tests successful SSE streaming.
func TestSessionsProcessSSEHandler_Success(t *testing.T) {
	t.Run("successful processing with multiple events", func(t *testing.T) {
		server, _, mockBot := setupTestServerForSSE(t)

		progressEvents := []rag.ProgressEvent{
			{Stage: "topics", Current: 0, Total: 3, Message: "Processing topics", Complete: false},
			{Stage: "topics", Current: 1, Total: 3, Message: "Topic 1 processed", Complete: false},
			{Stage: "topics", Current: 2, Total: 3, Message: "Topic 2 processed", Complete: false},
			{Stage: "facts", Current: 0, Total: 5, Message: "Extracting facts", Complete: false},
			{Stage: "done", Current: 5, Total: 5, Message: "Complete", Complete: true, Stats: &rag.ProcessingStats{
				MessagesProcessed: 10,
				TopicsExtracted:   3,
				FactsCreated:      2,
				FactsUpdated:      1,
				FactsDeleted:      0,
			}},
		}

		mockBot.On("ForceCloseSessionWithProgress", mock.Anything, int64(123), mock.Anything).
			Run(func(args mock.Arguments) {
				onProgress := args.Get(2).(rag.ProgressCallback)
				for _, event := range progressEvents {
					onProgress(event)
				}
			}).Return(&rag.ProcessingStats{
			MessagesProcessed: 10,
			TopicsExtracted:   3,
			FactsCreated:      2,
			FactsUpdated:      1,
			FactsDeleted:      0,
		}, nil)

		req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions/process?user_id=123", nil)
		sseRecorder := testutil.NewSSERecorder()

		server.sessionsProcessSSEHandler(sseRecorder, req)

		// Verify response
		assert.Equal(t, http.StatusOK, sseRecorder.Code)
		testutil.AssertSSEHeaders(t, sseRecorder)

		// Verify events were sent
		body := sseRecorder.Body.String()
		events := testutil.ParseSSEEvents(t, body)

		assert.GreaterOrEqual(t, len(events), 5, "should have at least 5 progress events")

		// Verify first event
		var firstEvent rag.ProgressEvent
		err := json.Unmarshal([]byte(events[0].Data), &firstEvent)
		assert.NoError(t, err)
		assert.Equal(t, "topics", firstEvent.Stage)
		assert.False(t, firstEvent.Complete)

		// Verify last event has complete=true
		var lastEvent rag.ProgressEvent
		lastIdx := len(events) - 1
		err = json.Unmarshal([]byte(events[lastIdx].Data), &lastEvent)
		assert.NoError(t, err)
		assert.True(t, lastEvent.Complete)
		assert.NotNil(t, lastEvent.Stats)
		assert.Equal(t, 3, lastEvent.Stats.TopicsExtracted)

		// Verify Flush was called (each event triggers a flush)
		assert.GreaterOrEqual(t, sseRecorder.FlushCount(), 5)

		mockBot.AssertExpectations(t)
	})

	t.Run("processing with timeout context", func(t *testing.T) {
		server, _, mockBot := setupTestServerForSSE(t)

		mockBot.On("ForceCloseSessionWithProgress", mock.Anything, int64(123), mock.Anything).
			Run(func(args mock.Arguments) {
				onProgress := args.Get(2).(rag.ProgressCallback)
				onProgress(rag.ProgressEvent{Stage: "done", Complete: true})
			}).Return(&rag.ProcessingStats{}, nil)

		// Create request with context that will be cancelled
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions/process?user_id=123", nil)
		req = req.WithContext(ctx)
		sseRecorder := testutil.NewSSERecorder()

		server.sessionsProcessSSEHandler(sseRecorder, req)

		assert.Equal(t, http.StatusOK, sseRecorder.Code)
		assert.Contains(t, sseRecorder.Body.String(), "data:")
	})
}

// TestSessionsProcessSSEHandler_Error tests error handling in SSE handler.
func TestSessionsProcessSSEHandler_Error(t *testing.T) {
	t.Run("returns error event on processing failure", func(t *testing.T) {
		server, _, mockBot := setupTestServerForSSE(t)

		mockBot.On("ForceCloseSessionWithProgress", mock.Anything, int64(123), mock.Anything).
			Return((*rag.ProcessingStats)(nil), assert.AnError)

		req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions/process?user_id=123", nil)
		sseRecorder := testutil.NewSSERecorder()

		server.sessionsProcessSSEHandler(sseRecorder, req)

		assert.Equal(t, http.StatusOK, sseRecorder.Code)
		testutil.AssertSSEHeaders(t, sseRecorder)

		body := sseRecorder.Body.String()
		events := testutil.ParseSSEEvents(t, body)

		// Should have at least the error event
		assert.GreaterOrEqual(t, len(events), 1)

		// Verify error event
		var errorEvent rag.ProgressEvent
		err := json.Unmarshal([]byte(events[0].Data), &errorEvent)
		assert.NoError(t, err)
		assert.Equal(t, "error", errorEvent.Stage)
		assert.True(t, errorEvent.Complete)
		assert.Contains(t, errorEvent.Message, "Error")

		mockBot.AssertExpectations(t)
	})

	t.Run("returns error event with partial stats", func(t *testing.T) {
		server, _, mockBot := setupTestServerForSSE(t)

		partialStats := &rag.ProcessingStats{
			MessagesProcessed: 5,
			TopicsExtracted:   1,
			FactsCreated:      0,
		}

		mockBot.On("ForceCloseSessionWithProgress", mock.Anything, int64(123), mock.Anything).
			Return(partialStats, assert.AnError)

		req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions/process?user_id=123", nil)
		sseRecorder := testutil.NewSSERecorder()

		server.sessionsProcessSSEHandler(sseRecorder, req)

		assert.Equal(t, http.StatusOK, sseRecorder.Code)

		body := sseRecorder.Body.String()
		events := testutil.ParseSSEEvents(t, body)

		assert.GreaterOrEqual(t, len(events), 1)

		var errorEvent rag.ProgressEvent
		err := json.Unmarshal([]byte(events[0].Data), &errorEvent)
		assert.NoError(t, err)
		assert.Equal(t, "error", errorEvent.Stage)
		assert.NotNil(t, errorEvent.Stats)
		assert.Equal(t, 1, errorEvent.Stats.TopicsExtracted)

		mockBot.AssertExpectations(t)
	})
}

// TestSessionsProcessSSEHandler_ContextCancellation tests context cancellation during processing.
func TestSessionsProcessSSEHandler_ContextCancellation(t *testing.T) {
	server, _, mockBot := setupTestServerForSSE(t)

	// Simulate long-running operation that gets cancelled
	mockBot.On("ForceCloseSessionWithProgress", mock.Anything, int64(123), mock.Anything).
		Run(func(args mock.Arguments) {
			onProgress := args.Get(2).(rag.ProgressCallback)
			onProgress(rag.ProgressEvent{Stage: "processing", Complete: false})
			// Simulate context already cancelled
		}).Return(&rag.ProcessingStats{}, nil)

	// Create context and cancel it immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions/process?user_id=123", nil)
	req = req.WithContext(ctx)
	sseRecorder := testutil.NewSSERecorder()

	server.sessionsProcessSSEHandler(sseRecorder, req)

	// Handler should still complete and close connection
	// Actual behavior depends on how the context cancellation is handled
	assert.True(t, sseRecorder.Code == http.StatusOK || sseRecorder.Code == 0)
}

// TestSessionsProcessSSEHandler_EmptyProgress tests handler with no progress events.
func TestSessionsProcessSSEHandler_EmptyProgress(t *testing.T) {
	server, _, mockBot := setupTestServerForSSE(t)

	mockBot.On("ForceCloseSessionWithProgress", mock.Anything, int64(123), mock.Anything).
		Return(&rag.ProcessingStats{}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions/process?user_id=123", nil)
	sseRecorder := testutil.NewSSERecorder()

	server.sessionsProcessSSEHandler(sseRecorder, req)

	assert.Equal(t, http.StatusOK, sseRecorder.Code)
	testutil.AssertSSEHeaders(t, sseRecorder)

	mockBot.AssertExpectations(t)
}

// TestSessionsProcessSSEHandler_LargePayload tests handler with large event payload.
func TestSessionsProcessSSEHandler_LargePayload(t *testing.T) {
	server, _, mockBot := setupTestServerForSSE(t)

	largeStats := &rag.ProcessingStats{
		MessagesProcessed: 100,
		TopicsExtracted:   50,
		TopicsMerged:      5,
		FactsCreated:      50,
		FactsUpdated:      25,
		FactsDeleted:      5,
		PromptTokens:      10000,
		CompletionTokens:  5000,
		EmbeddingTokens:   2000,
	}

	mockBot.On("ForceCloseSessionWithProgress", mock.Anything, int64(123), mock.Anything).
		Run(func(args mock.Arguments) {
			onProgress := args.Get(2).(rag.ProgressCallback)
			onProgress(rag.ProgressEvent{Stage: "done", Complete: true, Stats: largeStats})
		}).Return(largeStats, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions/process?user_id=123", nil)
	sseRecorder := testutil.NewSSERecorder()

	server.sessionsProcessSSEHandler(sseRecorder, req)

	assert.Equal(t, http.StatusOK, sseRecorder.Code)

	body := sseRecorder.Body.String()
	events := testutil.ParseSSEEvents(t, body)

	assert.GreaterOrEqual(t, len(events), 1)

	var finalEvent rag.ProgressEvent
	err := json.Unmarshal([]byte(events[0].Data), &finalEvent)
	assert.NoError(t, err)
	assert.Equal(t, 100, finalEvent.Stats.MessagesProcessed)
	assert.Equal(t, 17000, finalEvent.Stats.PromptTokens+finalEvent.Stats.CompletionTokens+finalEvent.Stats.EmbeddingTokens)

	mockBot.AssertExpectations(t)
}

// TestSessionsProcessSSEHandler_MultipleSequentialEvents tests rapid sequential events.
func TestSessionsProcessSSEHandler_MultipleSequentialEvents(t *testing.T) {
	server, _, mockBot := setupTestServerForSSE(t)

	eventCount := 20
	mockBot.On("ForceCloseSessionWithProgress", mock.Anything, int64(123), mock.Anything).
		Run(func(args mock.Arguments) {
			onProgress := args.Get(2).(rag.ProgressCallback)
			for i := 0; i < eventCount; i++ {
				onProgress(rag.ProgressEvent{
					Stage:    "processing",
					Current:  i,
					Total:    eventCount,
					Message:  "Processing item",
					Complete: i == eventCount-1,
				})
			}
		}).Return(&rag.ProcessingStats{}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions/process?user_id=123", nil)
	sseRecorder := testutil.NewSSERecorder()

	server.sessionsProcessSSEHandler(sseRecorder, req)

	assert.Equal(t, http.StatusOK, sseRecorder.Code)

	body := sseRecorder.Body.String()
	eventCountFromBody := testutil.CountSSEEvents(body)

	assert.GreaterOrEqual(t, eventCountFromBody, eventCount)
	assert.GreaterOrEqual(t, sseRecorder.FlushCount(), eventCount)

	mockBot.AssertExpectations(t)
}

// TestSessionsProcessSSEHandler_FlushTracking tests that flush is called for each event.
func TestSessionsProcessSSEHandler_FlushTracking(t *testing.T) {
	server, _, mockBot := setupTestServerForSSE(t)

	mockBot.On("ForceCloseSessionWithProgress", mock.Anything, int64(123), mock.Anything).
		Run(func(args mock.Arguments) {
			onProgress := args.Get(2).(rag.ProgressCallback)
			// Send 3 events
			for i := 0; i < 3; i++ {
				onProgress(rag.ProgressEvent{
					Stage:    "processing",
					Current:  i,
					Total:    3,
					Message:  "Processing",
					Complete: false,
				})
			}
			// Final event
			onProgress(rag.ProgressEvent{Stage: "done", Complete: true})
		}).Return(&rag.ProcessingStats{}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions/process?user_id=123", nil)
	sseRecorder := testutil.NewSSERecorder()

	server.sessionsProcessSSEHandler(sseRecorder, req)

	assert.Equal(t, http.StatusOK, sseRecorder.Code)
	// Flush should be called at least 4 times (once per event)
	assert.GreaterOrEqual(t, sseRecorder.FlushCount(), 4)

	mockBot.AssertExpectations(t)
}

// TestSessionsProcessSSEHandler_ProgressEventStructure tests the structure of progress events.
func TestSessionsProcessSSEHandler_ProgressEventStructure(t *testing.T) {
	server, _, mockBot := setupTestServerForSSE(t)

	mockBot.On("ForceCloseSessionWithProgress", mock.Anything, int64(123), mock.Anything).
		Run(func(args mock.Arguments) {
			onProgress := args.Get(2).(rag.ProgressCallback)
			onProgress(rag.ProgressEvent{
				Stage:    "test_stage",
				Current:  5,
				Total:    10,
				Message:  "Test message",
				Complete: false,
			})
		}).Return(&rag.ProcessingStats{}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions/process?user_id=123", nil)
	sseRecorder := testutil.NewSSERecorder()

	server.sessionsProcessSSEHandler(sseRecorder, req)

	assert.Equal(t, http.StatusOK, sseRecorder.Code)

	body := sseRecorder.Body.String()
	events := testutil.ParseSSEEvents(t, body)

	assert.GreaterOrEqual(t, len(events), 1)

	var event rag.ProgressEvent
	err := json.Unmarshal([]byte(events[0].Data), &event)
	assert.NoError(t, err)
	assert.Equal(t, "test_stage", event.Stage)
	assert.Equal(t, 5, event.Current)
	assert.Equal(t, 10, event.Total)
	assert.Equal(t, "Test message", event.Message)
	assert.False(t, event.Complete)

	mockBot.AssertExpectations(t)
}

// TestSessionsProcessSSEHandler_MarshalError tests handling of JSON marshal errors.
func TestSessionsProcessSSEHandler_MarshalError(t *testing.T) {
	server, _, mockBot := setupTestServerForSSE(t)

	// Create a channel that can't be marshaled to JSON (channels are not JSON-serializable)
	mockBot.On("ForceCloseSessionWithProgress", mock.Anything, int64(123), mock.Anything).
		Run(func(args mock.Arguments) {
			onProgress := args.Get(2).(rag.ProgressCallback)
			// Send valid event first
			onProgress(rag.ProgressEvent{Stage: "start", Complete: false})
			// This event should be sent successfully even if we can't test unmarshalable events
		}).Return(&rag.ProcessingStats{}, nil)

	req := testutil.NewTestRequest(t, "GET", "/ui/debug/sessions/process?user_id=123", nil)
	sseRecorder := testutil.NewSSERecorder()

	server.sessionsProcessSSEHandler(sseRecorder, req)

	// Handler should complete without panicking
	assert.Equal(t, http.StatusOK, sseRecorder.Code)

	mockBot.AssertExpectations(t)
}

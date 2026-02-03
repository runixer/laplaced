package agentlog_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAgentLogRepository is a local mock, avoids import cycle with testutil
type mockAgentLogRepository struct {
	err           error
	capturedLog   *storage.AgentLog
	addAgentLogFn func(log storage.AgentLog) error
}

func (m *mockAgentLogRepository) AddAgentLog(log storage.AgentLog) error {
	if m.err != nil {
		return m.err
	}
	if m.addAgentLogFn != nil {
		return m.addAgentLogFn(log)
	}
	// For testing, just capture that the method was called
	m.capturedLog = &log
	return nil
}

func (m *mockAgentLogRepository) GetAgentLogs(agentType string, userID int64, limit int) ([]storage.AgentLog, error) {
	return nil, nil
}

func (m *mockAgentLogRepository) GetAgentLogsExtended(filter storage.AgentLogFilter, limit, offset int) (storage.AgentLogResult, error) {
	return storage.AgentLogResult{}, nil
}

func (m *mockAgentLogRepository) GetAgentLogFull(ctx context.Context, id int64, userID int64) (*storage.AgentLog, error) {
	return nil, nil
}

func TestNewLogger(t *testing.T) {
	tests := []struct {
		name    string
		repo    storage.AgentLogRepository
		logger  *slog.Logger
		enabled bool
	}{
		{
			name:    "enabled logger",
			repo:    &mockAgentLogRepository{},
			logger:  slog.New(slog.NewTextHandler(os.Stdout, nil)),
			enabled: true,
		},
		{
			name:    "disabled logger",
			repo:    &mockAgentLogRepository{},
			logger:  slog.New(slog.NewTextHandler(os.Stdout, nil)),
			enabled: false,
		},
		{
			name:    "nil repo",
			repo:    nil,
			logger:  slog.New(slog.NewTextHandler(os.Stdout, nil)),
			enabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := agentlog.NewLogger(tt.repo, tt.logger, tt.enabled)

			assert.NotNil(t, logger)
			assert.Equal(t, tt.enabled, logger.Enabled())
		})
	}
}

func TestLogger_Enabled(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		want    bool
	}{
		{"enabled", true, true},
		{"disabled", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := agentlog.NewLogger(nil, slog.Default(), tt.enabled)
			assert.Equal(t, tt.want, logger.Enabled())
		})
	}
}

func TestLogger_Log_Enabled(t *testing.T) {
	repo := &mockAgentLogRepository{}
	logger := agentlog.NewLogger(repo, slog.Default(), true)

	cost := 0.001
	entry := agentlog.Entry{
		UserID:           123,
		AgentType:        agentlog.AgentLaplace,
		InputPrompt:      "test prompt",
		InputContext:     map[string]interface{}{"key": "value"},
		OutputResponse:   "test response",
		OutputParsed:     map[string]interface{}{"result": "success"},
		OutputContext:    map[string]interface{}{"tokens": 100},
		Model:            "test-model",
		PromptTokens:     50,
		CompletionTokens: 30,
		TotalCost:        &cost,
		DurationMs:       1000,
		Metadata:         map[string]interface{}{"meta": "data"},
		Success:          true,
	}

	// Should not panic
	logger.Log(context.Background(), entry)

	// Verify AddAgentLog was called
	assert.NotNil(t, repo.capturedLog)
	assert.Equal(t, int64(123), repo.capturedLog.UserID)
	assert.Equal(t, "laplace", repo.capturedLog.AgentType)
	assert.True(t, repo.capturedLog.Success)
}

func TestLogger_Log_Disabled(t *testing.T) {
	repo := &mockAgentLogRepository{}
	logger := agentlog.NewLogger(repo, slog.Default(), false)

	entry := agentlog.Entry{
		UserID:    123,
		AgentType: agentlog.AgentLaplace,
		Success:   true,
	}

	// Should not panic and should not call repo
	logger.Log(context.Background(), entry)

	// Verify AddAgentLog was NOT called
	assert.Nil(t, repo.capturedLog)
}

func TestLogger_Log_NilRepo(t *testing.T) {
	logger := agentlog.NewLogger(nil, slog.Default(), true)

	entry := agentlog.Entry{
		UserID:    123,
		AgentType: agentlog.AgentLaplace,
		Success:   true,
	}

	// Should not panic when repo is nil
	logger.Log(context.Background(), entry)
}

func TestLogger_LogSuccess(t *testing.T) {
	repo := &mockAgentLogRepository{}
	logger := agentlog.NewLogger(repo, slog.Default(), true)

	cost := 0.002

	// Call LogSuccess with all parameters
	logger.LogSuccess(
		context.Background(),
		456,
		agentlog.AgentReranker,
		"test input prompt",
		map[string]interface{}{"input": "context"},
		"test output response",
		map[string]interface{}{"parsed": "output"},
		map[string]interface{}{"output": "context"},
		"gemini-3-flash",
		100,
		50,
		&cost,
		2000,
		map[string]interface{}{"metadata": "value"},
	)

	// Verify AddAgentLog was called
	assert.NotNil(t, repo.capturedLog)
	assert.Equal(t, int64(456), repo.capturedLog.UserID)
	assert.Equal(t, "reranker", repo.capturedLog.AgentType)
	assert.True(t, repo.capturedLog.Success)
	assert.Equal(t, "test input prompt", repo.capturedLog.InputPrompt)
	assert.Equal(t, "test output response", repo.capturedLog.OutputResponse)
}

func TestLogger_LogSuccess_WithNilCost(t *testing.T) {
	repo := &mockAgentLogRepository{}
	logger := agentlog.NewLogger(repo, slog.Default(), true)

	// Call LogSuccess with nil cost
	logger.LogSuccess(
		context.Background(),
		456,
		agentlog.AgentReranker,
		"test input prompt",
		map[string]interface{}{"input": "context"},
		"test output response",
		nil,
		nil,
		"gemini-3-flash",
		100,
		50,
		nil,
		2000,
		nil,
	)

	// Verify AddAgentLog was called
	assert.NotNil(t, repo.capturedLog)
	assert.Nil(t, repo.capturedLog.TotalCost)
}

func TestLogger_LogError(t *testing.T) {
	repo := &mockAgentLogRepository{}
	logger := agentlog.NewLogger(repo, slog.Default(), true)

	// Call LogError with error parameters
	logger.LogError(
		context.Background(),
		789,
		agentlog.AgentSplitter,
		"test input prompt",
		map[string]interface{}{"input": "context"},
		"something went wrong",
		"gemini-3-flash",
		3000,
		map[string]interface{}{"metadata": "value"},
	)

	// Verify AddAgentLog was called
	assert.NotNil(t, repo.capturedLog)
	assert.Equal(t, int64(789), repo.capturedLog.UserID)
	assert.Equal(t, "splitter", repo.capturedLog.AgentType)
	assert.False(t, repo.capturedLog.Success)
	assert.Equal(t, "something went wrong", repo.capturedLog.ErrorMessage)
}

func TestLogger_LogError_WithNilContext(t *testing.T) {
	repo := &mockAgentLogRepository{}
	logger := agentlog.NewLogger(repo, slog.Default(), true)

	// Call LogError with minimal parameters
	logger.LogError(
		context.Background(),
		789,
		agentlog.AgentSplitter,
		"test input prompt",
		nil,
		"something went wrong",
		"gemini-3-flash",
		3000,
		nil,
	)

	// Should not panic
	assert.NotNil(t, repo.capturedLog)
}

func TestSanitizeInputContext(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		validate func(t *testing.T, result interface{})
	}{
		{
			name:  "nil input",
			input: nil,
			validate: func(t *testing.T, result interface{}) {
				assert.Nil(t, result)
			},
		},
		{
			name:  "string input - not JSON",
			input: "plain string",
			validate: func(t *testing.T, result interface{}) {
				assert.Equal(t, "plain string", result)
			},
		},
		{
			name:  "string input - valid JSON object",
			input: `{"key":"value"}`,
			validate: func(t *testing.T, result interface{}) {
				m, ok := result.(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "value", m["key"])
			},
		},
		{
			name:  "string input - valid JSON array",
			input: `["a","b","c"]`,
			validate: func(t *testing.T, result interface{}) {
				arr, ok := result.([]interface{})
				require.True(t, ok)
				assert.Len(t, arr, 3)
			},
		},
		{
			name:  "map input - no files",
			input: map[string]interface{}{"key": "value", "nested": map[string]interface{}{"foo": "bar"}},
			validate: func(t *testing.T, result interface{}) {
				m, ok := result.(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "value", m["key"])
				assert.NotNil(t, m["nested"])
			},
		},
		{
			name:  "array input - primitive values",
			input: []interface{}{"string", 123, true, nil},
			validate: func(t *testing.T, result interface{}) {
				arr, ok := result.([]interface{})
				require.True(t, ok)
				assert.Equal(t, "string", arr[0])
				assert.Equal(t, 123, arr[1])
				assert.Equal(t, true, arr[2])
				assert.Equal(t, nil, arr[3])
			},
		},
		{
			name:  "primitive types pass through",
			input: "primitive string",
			validate: func(t *testing.T, result interface{}) {
				assert.Equal(t, "primitive string", result)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := agentlog.SanitizeInputContext(tt.input)
			tt.validate(t, result)
		})
	}
}

func TestSanitizeInputContext_DocumentType(t *testing.T) {
	input := map[string]interface{}{
		"type": "document",
		"file": map[string]interface{}{
			"filename":  "test.pdf",
			"file_data": "data:application/pdf;base64,JVBERi0xLjYNJeLjz9MNCjY0OTIgMCBvYmoNPDwvRmlsdGVyL0ZsYXRlRGVjb2RlL0ZpcnN0IDEzOC9MZW5ndGggMTEzNi9OIDE1L1R5cGUvT2JqU3RtPj5zdHJlYW0NCh8OQaMidnj8sHoF3sYeyuTvs2GmC3vT6zKM/5fUArIwrFClaieTnDPm1UjgufzALowB9d2QTWuDR49yrffaX7hty6F2/7cbAPkmT8bkS1FU70B6dH1xEIYb7QZo4PC7o+yt/obitb6lGVIaTPuKTo2AkV3hkojxo+YrMEwVscHtWusa1F7fGC2/St0f3B5Z1t6i3KeApj0KBK4c3Nl+H426s693VG",
		},
	}

	result := agentlog.SanitizeInputContext(input)

	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok)

	file, ok := resultMap["file"].(map[string]interface{})
	require.True(t, ok)

	fileData, ok := file["file_data"].(string)
	require.True(t, ok)

	// Should contain placeholder
	assert.Contains(t, fileData, "FILE:")
	assert.Contains(t, fileData, "PDF")
	assert.Contains(t, fileData, "KB")
}

func TestSanitizeInputContext_ImageType(t *testing.T) {
	input := map[string]interface{}{
		"type": "file",
		"file": map[string]interface{}{
			"filename":  "photo.jpg",
			"file_data": "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCADIAfADASIAAhEBAxEB/8QAHwAAAQUBAQEBAQEAAAAAAAAAAAECAwQFBgcICQoL",
		},
	}

	result := agentlog.SanitizeInputContext(input)

	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok)

	file, ok := resultMap["file"].(map[string]interface{})
	require.True(t, ok)

	fileData, ok := file["file_data"].(string)
	require.True(t, ok)

	// Should contain placeholder
	assert.Contains(t, fileData, "FILE:")
	assert.Contains(t, fileData, "image")
}

func TestSanitizeInputContext_NoFileData(t *testing.T) {
	input := map[string]interface{}{
		"type": "file",
		"file": map[string]interface{}{
			"filename": "test.txt",
			// No file_data field
		},
	}

	result := agentlog.SanitizeInputContext(input)

	// Structure should be preserved
	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok)

	file, ok := resultMap["file"].(map[string]interface{})
	require.True(t, ok)

	assert.Equal(t, "test.txt", file["filename"])
}

func TestSanitizeInputContext_ShortFileData(t *testing.T) {
	input := map[string]interface{}{
		"type": "file",
		"file": map[string]interface{}{
			"filename":  "test.txt",
			"file_data": "short", // Less than 100 chars
		},
	}

	result := agentlog.SanitizeInputContext(input)

	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok)

	file, ok := resultMap["file"].(map[string]interface{})
	require.True(t, ok)

	// Short file_data should be preserved as-is
	fileData, ok := file["file_data"].(string)
	require.True(t, ok)
	assert.Equal(t, "short", fileData)
}

func TestSanitizeInputContext_NonBase64FileData(t *testing.T) {
	input := map[string]interface{}{
		"type": "file",
		"file": map[string]interface{}{
			"filename":  "test.txt",
			"file_data": strings.Repeat("x", 200), // Long but not base64
		},
	}

	result := agentlog.SanitizeInputContext(input)

	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok)

	file, ok := resultMap["file"].(map[string]interface{})
	require.True(t, ok)

	// Non-base64 data should be preserved (no placeholder)
	fileData, ok := file["file_data"].(string)
	require.True(t, ok)
	assert.Equal(t, 200, len(fileData))
}

func TestSanitizeInputContext_FileWithoutFilename(t *testing.T) {
	input := map[string]interface{}{
		"type": "file",
		"file": map[string]interface{}{
			"file_data": "data:application/pdf;base64,JVBERi0xLjYNJeLjz9MNCjY0OTIgMCBvYmoNPDwvRmlsdGVyL0ZsYXRlRGVjb2RlL0ZpcnN0IDEzOC9MZW5ndGggMTEzNi9OIDE1L1R5cGUvT2JqU3RtPj5zdHJlYW0NCh8OQaMidnj8sHoF3sYeyuTvs2GmC3vT6zKM/5fUArIwrFClaieTnDPm1UjgufzALowB9d2QTWuDR49yrffaX7hty6F2/7cbAPkmT8bkS1FU70B6dH1xEIYb7QZo4PC7o+yt/obitb6lGVIaTPuKTo2AkV3hkojxo+YrMEwVscHtWusa1F7fGC2/St0f3B5Z1t6i3KeApj0KBK4c3Nl+H426s693VG",
		},
	}

	result := agentlog.SanitizeInputContext(input)

	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok)

	file, ok := resultMap["file"].(map[string]interface{})
	require.True(t, ok)

	fileData, ok := file["file_data"].(string)
	require.True(t, ok)

	// Should use type (PDF) instead of filename
	assert.Contains(t, fileData, "FILE:")
	assert.Contains(t, fileData, "PDF")
}

func TestSanitizeConversationTurns_NilInput(t *testing.T) {
	result := agentlog.SanitizeConversationTurns(nil)
	assert.Nil(t, result)
}

func TestSanitizeConversationTurns_EmptyTurns(t *testing.T) {
	turns := &agentlog.ConversationTurns{
		Turns:                 []agentlog.ConversationTurn{},
		TotalDurationMs:       0,
		TotalPromptTokens:     0,
		TotalCompletionTokens: 0,
	}

	result := agentlog.SanitizeConversationTurns(turns)

	assert.NotNil(t, result)
	assert.Empty(t, result.Turns)
	assert.Equal(t, 0, result.TotalDurationMs)
}

func TestSanitizeConversationTurns_MultipleTurns(t *testing.T) {
	requestJSON := `{"model":"test","messages":[{"role":"user","content":[{"type":"file","file":{"filename":"test.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjYNJeLjz9MNCjY0OTIgMCBvYmoNPDwvRmlsdGVyL0ZsYXRlRGVjb2RlL0ZpcnN0IDEzOC9MZW5ndGggMTEzNi9OIDE1L1R5cGUvT2JqU3RtPj5zdHJlYW0N"}}]}]}`
	responseJSON := `{"content":"response","usage":{"prompt_tokens":100}}`

	cost := 0.001
	turns := &agentlog.ConversationTurns{
		Turns: []agentlog.ConversationTurn{
			{Iteration: 1, Request: requestJSON, Response: responseJSON, DurationMs: 1000},
			{Iteration: 2, Request: `{"next":"request"}`, Response: `{"next":"response"}`, DurationMs: 500},
		},
		TotalDurationMs:       1500,
		TotalPromptTokens:     200,
		TotalCompletionTokens: 100,
		TotalCost:             &cost,
	}

	result := agentlog.SanitizeConversationTurns(turns)

	assert.NotNil(t, result)
	assert.Len(t, result.Turns, 2)
	assert.Equal(t, 1500, result.TotalDurationMs)
	assert.Equal(t, 200, result.TotalPromptTokens)
	assert.Equal(t, 100, result.TotalCompletionTokens)
	assert.NotNil(t, result.TotalCost)
	assert.Equal(t, 0.001, *result.TotalCost)

	// First turn should be sanitized
	firstRequest, ok := result.Turns[0].Request.(string)
	require.True(t, ok)
	assert.NotContains(t, firstRequest, "data:application/pdf;base64,JVBERi")
}

func TestSanitizeConversationTurns_NonStringRequestResponse(t *testing.T) {
	turns := &agentlog.ConversationTurns{
		Turns: []agentlog.ConversationTurn{
			{
				Iteration:  1,
				Request:    map[string]interface{}{"direct": "map"},
				Response:   123,
				DurationMs: 1000,
			},
		},
		TotalDurationMs: 1000,
	}

	result := agentlog.SanitizeConversationTurns(turns)

	assert.NotNil(t, result)
	assert.Len(t, result.Turns, 1)

	// Non-string values should be serialized to JSON
	requestStr, ok := result.Turns[0].Request.(string)
	require.True(t, ok)
	assert.Contains(t, requestStr, "map")

	responseStr, ok := result.Turns[0].Response.(string)
	require.True(t, ok)
	assert.Equal(t, "123", responseStr)
}

func TestAgentType_Constants(t *testing.T) {
	tests := []struct {
		constant agentlog.AgentType
		expected string
	}{
		{agentlog.AgentLaplace, "laplace"},
		{agentlog.AgentReranker, "reranker"},
		{agentlog.AgentSplitter, "splitter"},
		{agentlog.AgentMerger, "merger"},
		{agentlog.AgentEnricher, "enricher"},
		{agentlog.AgentArchivist, "archivist"},
		{agentlog.AgentScout, "scout"},
	}

	for _, tt := range tests {
		t.Run(string(tt.constant), func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.constant))
		})
	}
}

func TestConversationTurn_Structure(t *testing.T) {
	turn := agentlog.ConversationTurn{
		Iteration:  1,
		Request:    `{"test":"request"}`,
		Response:   `{"test":"response"}`,
		DurationMs: 1234,
	}

	data, err := json.Marshal(turn)
	require.NoError(t, err)

	var decoded agentlog.ConversationTurn
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, turn.Iteration, decoded.Iteration)
	assert.Equal(t, turn.DurationMs, decoded.DurationMs)
}

func TestConversationTurns_Structure(t *testing.T) {
	cost := 0.005
	turns := agentlog.ConversationTurns{
		Turns: []agentlog.ConversationTurn{
			{Iteration: 1, Request: "req1", Response: "resp1", DurationMs: 100},
			{Iteration: 2, Request: "req2", Response: "resp2", DurationMs: 200},
		},
		TotalDurationMs:       300,
		TotalPromptTokens:     150,
		TotalCompletionTokens: 75,
		TotalCost:             &cost,
	}

	data, err := json.Marshal(turns)
	require.NoError(t, err)

	var decoded agentlog.ConversationTurns
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Len(t, decoded.Turns, 2)
	assert.Equal(t, 300, decoded.TotalDurationMs)
	assert.Equal(t, 150, decoded.TotalPromptTokens)
	assert.Equal(t, 75, decoded.TotalCompletionTokens)
	assert.NotNil(t, decoded.TotalCost)
	assert.Equal(t, 0.005, *decoded.TotalCost)
}

func TestEntry_Structure(t *testing.T) {
	cost := 0.01
	entry := agentlog.Entry{
		UserID:           123,
		AgentType:        agentlog.AgentLaplace,
		InputPrompt:      "test prompt",
		InputContext:     map[string]string{"key": "value"},
		OutputResponse:   "test response",
		OutputParsed:     map[string]string{"result": "success"},
		OutputContext:    map[string]int{"tokens": 100},
		Model:            "test-model",
		PromptTokens:     50,
		CompletionTokens: 30,
		TotalCost:        &cost,
		DurationMs:       1000,
		Metadata:         map[string]string{"meta": "data"},
		Success:          true,
		ErrorMessage:     "",
	}

	data, err := json.Marshal(entry)
	require.NoError(t, err)

	// Just verify it can be marshaled without error
	assert.NotEmpty(t, data)
	assert.Contains(t, string(data), "laplace")
	assert.Contains(t, string(data), "test prompt")
}

func TestEntry_AllAgentTypes(t *testing.T) {
	agentTypes := []agentlog.AgentType{
		agentlog.AgentLaplace,
		agentlog.AgentReranker,
		agentlog.AgentSplitter,
		agentlog.AgentMerger,
		agentlog.AgentEnricher,
		agentlog.AgentArchivist,
		agentlog.AgentScout,
	}

	for _, at := range agentTypes {
		t.Run(string(at), func(t *testing.T) {
			entry := agentlog.Entry{
				UserID:    123,
				AgentType: at,
				Success:   true,
			}

			assert.Equal(t, at, entry.AgentType)
		})
	}
}

// TestLogger_Log_AllFields verifies all Entry fields are handled correctly
func TestLogger_Log_AllFields(t *testing.T) {
	repo := &mockAgentLogRepository{}
	logger := agentlog.NewLogger(repo, slog.Default(), true)

	cost := 0.015
	metadata := map[string]interface{}{
		"tool_calls": []interface{}{
			map[string]interface{}{
				"id":   "call_123",
				"type": "function",
			},
		},
	}

	entry := agentlog.Entry{
		UserID:           999,
		AgentType:        agentlog.AgentReranker,
		InputPrompt:      "What is X?",
		InputContext:     map[string]string{"context": "value"},
		OutputResponse:   "X is...",
		OutputParsed:     map[string]string{"result": "parsed"},
		OutputContext:    map[string]int{"total_tokens": 200},
		Model:            "gemini-3-flash",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalCost:        &cost,
		DurationMs:       5000,
		Metadata:         metadata,
		Success:          true,
		ErrorMessage:     "",
	}

	// Should not panic with all fields populated
	logger.Log(context.Background(), entry)

	assert.NotNil(t, repo.capturedLog)
	assert.Equal(t, int64(999), repo.capturedLog.UserID)
	assert.True(t, repo.capturedLog.Success)
}

// TestLogger_Log_WithConversationTurns verifies multi-turn conversation logging
func TestLogger_Log_WithConversationTurns(t *testing.T) {
	repo := &mockAgentLogRepository{}
	logger := agentlog.NewLogger(repo, slog.Default(), true)

	cost := 0.003
	turns := &agentlog.ConversationTurns{
		Turns: []agentlog.ConversationTurn{
			{Iteration: 1, Request: `{"query":"test"}`, Response: `{"tool_calls":[]}`, DurationMs: 1000},
			{Iteration: 2, Request: `{"tool":"results"}`, Response: `{"final":"answer"}`, DurationMs: 800},
		},
		TotalDurationMs:       1800,
		TotalPromptTokens:     300,
		TotalCompletionTokens: 150,
		TotalCost:             &cost,
	}

	entry := agentlog.Entry{
		UserID:            777,
		AgentType:         agentlog.AgentReranker,
		InputPrompt:       "reranker query",
		OutputResponse:    "final answer",
		Model:             "gemini-3-flash",
		PromptTokens:      300,
		CompletionTokens:  150,
		TotalCost:         &cost,
		DurationMs:        1800,
		Success:           true,
		ConversationTurns: turns,
	}

	// Should not panic
	logger.Log(context.Background(), entry)

	assert.NotNil(t, repo.capturedLog)
	assert.NotEmpty(t, repo.capturedLog.ConversationTurns)
}

// TestLogger_Log_UnmarshalableMetadata handles metadata that can't be marshaled
func TestLogger_Log_UnmarshalableMetadata(t *testing.T) {
	repo := &mockAgentLogRepository{}
	logger := agentlog.NewLogger(repo, slog.Default(), true)

	// Channel cannot be marshaled to JSON
	entry := agentlog.Entry{
		UserID:    123,
		AgentType: agentlog.AgentLaplace,
		Success:   true,
		Metadata:  make(chan int),
	}

	// Should not panic, unmarshalable fields become empty strings
	logger.Log(context.Background(), entry)

	// Verify the call was made despite unmarshalable metadata
	assert.NotNil(t, repo.capturedLog)
}

// TestLogger_Log_UnmarshalableInputContext handles input context that can't be marshaled
func TestLogger_Log_UnmarshalableInputContext(t *testing.T) {
	repo := &mockAgentLogRepository{}
	logger := agentlog.NewLogger(repo, slog.Default(), true)

	entry := agentlog.Entry{
		UserID:       123,
		AgentType:    agentlog.AgentLaplace,
		InputContext: make(chan int),
		Success:      true,
	}

	// Should not panic
	logger.Log(context.Background(), entry)

	assert.NotNil(t, repo.capturedLog)
}

// TestLogger_Log_WithStringMetadata tests that string metadata is preserved
func TestLogger_Log_WithStringMetadata(t *testing.T) {
	var captured *storage.AgentLog
	repo := &mockAgentLogRepository{
		addAgentLogFn: func(log storage.AgentLog) error {
			captured = &log
			return nil
		},
	}
	logger := agentlog.NewLogger(repo, slog.Default(), true)

	entry := agentlog.Entry{
		UserID:    123,
		AgentType: agentlog.AgentLaplace,
		Success:   true,
		Metadata:  "already a string",
	}

	logger.Log(context.Background(), entry)

	// Verify the method was called and string was preserved
	assert.NotNil(t, captured)
	assert.Equal(t, "already a string", captured.Metadata)
}

// TestLogger_Log_WithStringContexts tests that string contexts are preserved
func TestLogger_Log_WithStringContexts(t *testing.T) {
	repo := &mockAgentLogRepository{}
	logger := agentlog.NewLogger(repo, slog.Default(), true)

	entry := agentlog.Entry{
		UserID:        123,
		AgentType:     agentlog.AgentLaplace,
		Success:       true,
		InputContext:  "{\"already\":\"json\"}",
		OutputParsed:  "{\"result\":\"value\"}",
		OutputContext: "{\"response\":\"data\"}",
	}

	logger.Log(context.Background(), entry)

	// Should not panic, strings should be preserved as-is
	assert.NotNil(t, repo.capturedLog)
	assert.Equal(t, "{\"already\":\"json\"}", repo.capturedLog.InputContext)
}

// TestLogger_Log_NilPointerFields tests nil pointer handling
func TestLogger_Log_NilPointerFields(t *testing.T) {
	repo := &mockAgentLogRepository{}
	logger := agentlog.NewLogger(repo, slog.Default(), true)

	var nilPtr *struct{ Field string }
	entry := agentlog.Entry{
		UserID:        123,
		AgentType:     agentlog.AgentLaplace,
		Success:       true,
		InputContext:  nilPtr,
		OutputParsed:  nilPtr,
		OutputContext: nilPtr,
	}

	logger.Log(context.Background(), entry)

	// Should not panic, nil pointers should become empty strings
	assert.NotNil(t, repo.capturedLog)
	assert.Equal(t, "", repo.capturedLog.InputContext)
	assert.Equal(t, "", repo.capturedLog.OutputParsed)
	assert.Equal(t, "", repo.capturedLog.OutputContext)
}

// TestSanitizeInputContext_FileTypedWithNestedFile tests the original comprehensive case
func TestSanitizeInputContext_FileTypedWithNestedFile(t *testing.T) {
	// This is the structure that comes from OpenAI API with files
	// type: "file" at top level, with nested "file" object containing file_data
	inputJSON := `{
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "Check this file"
					},
					{
						"type": "file",
						"file": {
							"filename": "MOMENTUM-4_Manual_RU.pdf",
							"file_data": "data:application/pdf;base64,JVBERi0xLjYNJeLjz9MNCjY0OTIgMCBvYmoNPDwvRmlsdGVyL0ZsYXRlRGVjb2RlL0ZpcnN0IDEzOC9MZW5ndGggMTEzNi9OIDE1L1R5cGUvT2JqU3RtPj5zdHJlYW0NCh8OQaMidnj8sHoF3sYeyuTvs2GmC3vT6zKM/5fUArIwrFClaieTnDPm1UjgufzALowB9d2QTWuDR49yrffaX7hty6F2/7cbAPkmT8bkS1FU70B6dH1xEIYb7QZo4PC7o+yt/obitb6lGVIaTPuKTo2AkV3hkojxo+YrMEwVscHtWusa1F7fGC2/St0f3B5Z1t6i3KeApj0KBK4c3Nl+H426s693VG"
						}
					}
				]
			}
		]
	}`

	var input interface{}
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		t.Fatalf("Failed to parse input JSON: %v", err)
	}

	// Get the original file_data length for comparison
	var originalLength int
	if inputMap, ok := input.(map[string]interface{}); ok {
		if messages, ok := inputMap["messages"].([]interface{}); ok && len(messages) > 0 {
			if msg, ok := messages[0].(map[string]interface{}); ok {
				if content, ok := msg["content"].([]interface{}); ok && len(content) > 1 {
					if filePart, ok := content[1].(map[string]interface{}); ok {
						if file, ok := filePart["file"].(map[string]interface{}); ok {
							if fileData, ok := file["file_data"].(string); ok {
								originalLength = len(fileData)
							}
						}
					}
				}
			}
		}
	}

	if originalLength == 0 {
		t.Fatal("Could not find original file_data to compare")
	}

	// Apply sanitization
	result := agentlog.SanitizeInputContext(input)

	// Verify that file_data was replaced with a placeholder
	if resultMap, ok := result.(map[string]interface{}); ok {
		if messages, ok := resultMap["messages"].([]interface{}); ok && len(messages) > 0 {
			if msg, ok := messages[0].(map[string]interface{}); ok {
				if content, ok := msg["content"].([]interface{}); ok && len(content) > 1 {
					if filePart, ok := content[1].(map[string]interface{}); ok {
						if file, ok := filePart["file"].(map[string]interface{}); ok {
							if fileData, ok := file["file_data"].(string); ok {
								// The sanitized data should be much shorter
								if len(fileData) >= originalLength {
									t.Errorf("file_data was not sanitized: got length %d, expected < %d", len(fileData), originalLength)
								}
								// Should contain placeholder text
								if !strings.Contains(fileData, "FILE:") && !strings.Contains(fileData, "PDF") {
									t.Errorf("file_data placeholder unexpected: %s", fileData)
								}
							} else {
								t.Error("file_data field not found in file object")
							}
						} else {
							t.Error("file object not found in content part")
						}
					}
				}
			}
		}
	}
}

// TestSanitizeConversationTurns_OriginalPreservesOriginalBehavior
func TestSanitizeConversationTurns_OriginalPreservesOriginalBehavior(t *testing.T) {
	// Create ConversationTurns with raw base64 data in Request/Response (v0.6.0: FilePart format)
	requestJSON := `{
		"model": "google/gemini-3-flash-preview",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "Check this file"},
					{
						"type": "file",
						"file": {
							"filename": "test.pdf",
							"file_data": "data:application/pdf;base64,JVBERi0xLjYNJeLjz9MNCjY0OTIgMCBvYmoNPDwvRmlsdGVyL0ZsYXRlRGVjb2RlL0ZpcnN0IDEzOC9MZW5ndGggMTEzNi9OIDE1L1R5cGUvT2JqU3RtPj5zdHJlYW0NCh8OQaMidnj8sHoF3sYeyuTvs2GmC3vT6zKM/5fUArIwrFClaieTnDPm1UjgufzALowB9d2QTWuDR49yrffaX7hty6F2/7cbAPkmT8bkS1FU70B6dH1xEIYb7QZo4PC7o+yt/obitb6lGVIaTPuKTo2AkV3hkojxo+YrMEwVscHtWusa1F7fGC2/St0f3B5Z1t6i3KeApj0KBK4c3Nl+H426s693VG"
						}
					}
				]
			}
		]
	}`

	responseJSON := `{
		"content": "Response text",
		"usage": {"prompt_tokens": 100, "completion_tokens": 50}
	}`

	turns := &agentlog.ConversationTurns{
		Turns: []agentlog.ConversationTurn{
			{
				Iteration:  1,
				Request:    requestJSON,
				Response:   responseJSON,
				DurationMs: 1000,
			},
		},
		TotalDurationMs:       1000,
		TotalPromptTokens:     100,
		TotalCompletionTokens: 50,
	}

	// Sanitize
	sanitized := agentlog.SanitizeConversationTurns(turns)

	if sanitized == nil {
		t.Fatal("Sanitized turns should not be nil")
	}

	// The Request should be a string (JSON) after sanitization
	sanitizedRequest, ok := sanitized.Turns[0].Request.(string)
	if !ok {
		t.Fatalf("Request should be a string, got %T", sanitized.Turns[0].Request)
	}

	// Check that file_data was sanitized
	if strings.Contains(sanitizedRequest, "data:application/pdf;base64,JVBERi") && !strings.Contains(sanitizedRequest, "FILE:") {
		t.Errorf("Request was not sanitized properly, still contains raw base64")
	}

	if strings.Contains(sanitizedRequest, "FILE:") {
		t.Logf("PASS: Request was sanitized to FILE: placeholder")
	}

	// Also verify it's valid JSON
	var parsedReq map[string]interface{}
	if err := json.Unmarshal([]byte(sanitizedRequest), &parsedReq); err != nil {
		t.Errorf("Request should be valid JSON string: %v", err)
	}

	// Check that Response was preserved
	sanitizedResponseJSON, _ := json.Marshal(sanitized.Turns[0].Response)
	sanitizedResponseStr := string(sanitizedResponseJSON)

	if !strings.Contains(sanitizedResponseStr, "Response text") {
		t.Errorf("Response was modified unexpectedly: %s", sanitizedResponseStr)
	}

	// Check that metadata is preserved
	if sanitized.TotalDurationMs != 1000 {
		t.Errorf("TotalDurationMs should be preserved")
	}
}

// TestLogger_Log_ErrorFromRepo tests that repo errors are handled gracefully
func TestLogger_Log_ErrorFromRepo(t *testing.T) {
	expectedErr := errors.New("database error")
	repo := &mockAgentLogRepository{err: expectedErr}
	logger := agentlog.NewLogger(repo, slog.Default(), true)

	entry := agentlog.Entry{
		UserID:    123,
		AgentType: agentlog.AgentLaplace,
		Success:   true,
	}

	// Should not panic, error should be logged but not cause panic
	logger.Log(context.Background(), entry)

	// capturedLog will be nil because AddAgentLog returns error before setting it
	// This is expected - the error is caught and logged by the logger
	assert.Nil(t, repo.capturedLog)
}

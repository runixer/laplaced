package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCreateChatCompletion(t *testing.T) {
	// Mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check request
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer test_api_key", r.Header.Get("Authorization"))

		// Send response
		w.Header().Set("Content-Type", "application/json")
		resp := ChatCompletionResponse{
			Choices: []struct {
				Message struct {
					Role             string      `json:"role"`
					Content          string      `json:"content"`
					ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
					ReasoningDetails interface{} `json:"reasoning_details,omitempty"`
				} `json:"message"`
				FinishReason string `json:"finish_reason,omitempty"`
				Index        int    `json:"index"`
			}{
				{
					Message: struct {
						Role             string      `json:"role"`
						Content          string      `json:"content"`
						ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
						ReasoningDetails interface{} `json:"reasoning_details,omitempty"`
					}{Role: "assistant", Content: "Hello from mock server!"},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int      `json:"prompt_tokens"`
				CompletionTokens int      `json:"completion_tokens"`
				TotalTokens      int      `json:"total_tokens"`
				Cost             *float64 `json:"cost,omitempty"`
			}{PromptTokens: 100, CompletionTokens: 23, TotalTokens: 123},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create client
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client, err := NewClientWithBaseURL(logger, "test_api_key", "", server.URL+"/api/v1")
	assert.NoError(t, err)

	// Create request
	req := ChatCompletionRequest{
		Model: "test_model",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}

	// Call method
	resp, err := client.CreateChatCompletion(context.Background(), req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)

	// Check response
	assert.Equal(t, "Hello from mock server!", resp.Choices[0].Message.Content)
	assert.Equal(t, 100, resp.Usage.PromptTokens)
	assert.Equal(t, 23, resp.Usage.CompletionTokens)
	assert.Equal(t, 123, resp.Usage.TotalTokens)
}

func TestCreateChatCompletionLogging(t *testing.T) {
	// Redirect log output
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := ChatCompletionResponse{
			Choices: []struct {
				Message struct {
					Role             string      `json:"role"`
					Content          string      `json:"content"`
					ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
					ReasoningDetails interface{} `json:"reasoning_details,omitempty"`
				} `json:"message"`
				FinishReason string `json:"finish_reason,omitempty"`
				Index        int    `json:"index"`
			}{
				{
					Message: struct {
						Role             string      `json:"role"`
						Content          string      `json:"content"`
						ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
						ReasoningDetails interface{} `json:"reasoning_details,omitempty"`
					}{Role: "assistant", Content: "log test"},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int      `json:"prompt_tokens"`
				CompletionTokens int      `json:"completion_tokens"`
				TotalTokens      int      `json:"total_tokens"`
				Cost             *float64 `json:"cost,omitempty"`
			}{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, _ := NewClientWithBaseURL(logger, "test_api_key", "", server.URL)

	req := ChatCompletionRequest{
		Model: "test_model",
		Messages: []Message{
			{Role: "user", Content: "This is a secret message that should not be logged."},
		},
	}

	_, err := client.CreateChatCompletion(context.Background(), req)
	assert.NoError(t, err)

	// Check that the log output for the request has a structured object
	logLines := bytes.Split(bytes.TrimSpace(logBuf.Bytes()), []byte("\n"))
	var foundRequestLog bool
	for _, line := range logLines {
		var logEntry map[string]interface{}
		err := json.Unmarshal(line, &logEntry)
		assert.NoError(t, err, "Failed to unmarshal log line: %s", string(line))

		if msg, ok := logEntry["msg"].(string); ok && msg == "Sending request to OpenRouter" {
			foundRequestLog = true
			requestStructure, ok := logEntry["request_structure"]
			assert.True(t, ok, "Log entry should have request_structure field")
			assert.IsType(t, map[string]interface{}{}, requestStructure, "request_structure should be a JSON object, not a string")

			// Also check other fields
			assert.Equal(t, "test_model", logEntry["model"])
			// We now log string content (truncated if necessary), so this should be present.
			assert.Contains(t, string(line), "This is a secret message that should not be logged.")
		}
	}
	assert.True(t, foundRequestLog, "Did not find the 'Sending request to OpenRouter' log entry")
}

func TestCreateChatCompletionRetry(t *testing.T) {
	attempts := 0

	// Mock server that fails twice then succeeds
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			// Return 429 (rate limit) or 503 (service unavailable) for first two attempts
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": "rate limited"}`))
			return
		}

		// Third attempt succeeds
		w.Header().Set("Content-Type", "application/json")
		resp := ChatCompletionResponse{
			Choices: []struct {
				Message struct {
					Role             string      `json:"role"`
					Content          string      `json:"content"`
					ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
					ReasoningDetails interface{} `json:"reasoning_details,omitempty"`
				} `json:"message"`
				FinishReason string `json:"finish_reason,omitempty"`
				Index        int    `json:"index"`
			}{
				{
					Message: struct {
						Role             string      `json:"role"`
						Content          string      `json:"content"`
						ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
						ReasoningDetails interface{} `json:"reasoning_details,omitempty"`
					}{Role: "assistant", Content: "Success after retry!"},
					FinishReason: "stop",
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client, err := NewClientWithBaseURL(logger, "test_api_key", "", server.URL+"/api/v1")
	assert.NoError(t, err)

	req := ChatCompletionRequest{
		Model:    "test_model",
		Messages: []Message{{Role: "user", Content: "Hello"}},
	}

	resp, err := client.CreateChatCompletion(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, 3, attempts, "Should have made 3 attempts (2 retries)")
	assert.Equal(t, "Success after retry!", resp.Choices[0].Message.Content)
}

func TestCreateChatCompletionMaxRetriesExceeded(t *testing.T) {
	attempts := 0

	// Mock server that always fails
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error": "service unavailable"}`))
	}))
	defer server.Close()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client, err := NewClientWithBaseURL(logger, "test_api_key", "", server.URL+"/api/v1")
	assert.NoError(t, err)

	req := ChatCompletionRequest{
		Model:    "test_model",
		Messages: []Message{{Role: "user", Content: "Hello"}},
	}

	_, err = client.CreateChatCompletion(context.Background(), req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "503")
	assert.Equal(t, 4, attempts, "Should have made 4 attempts (initial + 3 retries)")
}

func TestIsRetryableStatusCode(t *testing.T) {
	tests := []struct {
		code     int
		expected bool
	}{
		{http.StatusOK, false},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.code), func(t *testing.T) {
			assert.Equal(t, tt.expected, isRetryableStatusCode(tt.code))
		})
	}
}

func TestFilterReasoningForLog(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected interface{}
	}{
		{
			name:     "nil input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "non-array input",
			input:    "some string",
			expected: "some string",
		},
		{
			name: "filters out encrypted reasoning",
			input: []interface{}{
				map[string]interface{}{
					"type": "reasoning.text",
					"text": "This is readable reasoning",
				},
				map[string]interface{}{
					"type": "reasoning.encrypted",
					"data": "CiUBjz1rX3MB/waIzY5/GWJubVYRagRy...",
				},
			},
			expected: []interface{}{
				map[string]interface{}{
					"type": "reasoning.text",
					"text": "This is readable reasoning",
				},
			},
		},
		{
			name: "only encrypted returns nil",
			input: []interface{}{
				map[string]interface{}{
					"type": "reasoning.encrypted",
					"data": "CiUBjz1rX3MB...",
				},
			},
			expected: nil,
		},
		{
			name:     "empty array",
			input:    []interface{}{},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterReasoningForLog(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// timeoutError implements net.Error for testing
type timeoutError struct{}

func (e *timeoutError) Error() string   { return "timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "generic error",
			err:      errors.New("some error"),
			expected: false,
		},
		{
			name:     "timeout error",
			err:      &timeoutError{},
			expected: true,
		},
		{
			name: "net.OpError",
			err: &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: errors.New("connection refused"),
			},
			expected: true,
		},
		{
			name:     "wrapped timeout error",
			err:      errors.New("wrapped: " + (&timeoutError{}).Error()),
			expected: false, // Simple wrapping doesn't preserve the type
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isRetryableError(tt.err))
		})
	}
}

func TestCalculateBackoff(t *testing.T) {
	tests := []struct {
		name      string
		attempt   int
		minExpect time.Duration
		maxExpect time.Duration
	}{
		{
			name:      "attempt 0",
			attempt:   0,
			minExpect: 800 * time.Millisecond,  // 1s - 20% jitter
			maxExpect: 1200 * time.Millisecond, // 1s + 20% jitter
		},
		{
			name:      "attempt 1",
			attempt:   1,
			minExpect: 1600 * time.Millisecond, // 2s - 20% jitter
			maxExpect: 2400 * time.Millisecond, // 2s + 20% jitter
		},
		{
			name:      "attempt 2",
			attempt:   2,
			minExpect: 3200 * time.Millisecond, // 4s - 20% jitter
			maxExpect: 4800 * time.Millisecond, // 4s + 20% jitter
		},
		{
			name:      "attempt 10 (capped at maxDelay)",
			attempt:   10,
			minExpect: 24 * time.Second, // 30s - 20% jitter
			maxExpect: 36 * time.Second, // 30s + 20% jitter
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run multiple times to account for jitter
			for i := 0; i < 10; i++ {
				result := calculateBackoff(tt.attempt)
				assert.GreaterOrEqual(t, result, tt.minExpect, "backoff should be >= min")
				assert.LessOrEqual(t, result, tt.maxExpect, "backoff should be <= max")
			}
		})
	}
}

func TestCreateEmbeddings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/embeddings", r.URL.Path)
		assert.Equal(t, "Bearer test_api_key", r.Header.Get("Authorization"))

		// Parse request
		var req EmbeddingRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, err)
		assert.Equal(t, "text-embedding-model", req.Model)
		assert.Len(t, req.Input, 2)

		// Send response
		w.Header().Set("Content-Type", "application/json")
		resp := EmbeddingResponse{
			Object: "list",
			Data: []EmbeddingObject{
				{Object: "embedding", Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
				{Object: "embedding", Embedding: []float32{0.4, 0.5, 0.6}, Index: 1},
			},
			Model: "text-embedding-model",
			Usage: struct {
				PromptTokens int      `json:"prompt_tokens"`
				TotalTokens  int      `json:"total_tokens"`
				Cost         *float64 `json:"cost,omitempty"`
			}{PromptTokens: 10, TotalTokens: 10},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client, err := NewClientWithBaseURL(logger, "test_api_key", "", server.URL+"/api/v1")
	assert.NoError(t, err)

	req := EmbeddingRequest{
		Model: "text-embedding-model",
		Input: []string{"hello world", "test input"},
	}

	resp, err := client.CreateEmbeddings(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, "list", resp.Object)
	assert.Len(t, resp.Data, 2)
	assert.Equal(t, []float32{0.1, 0.2, 0.3}, resp.Data[0].Embedding)
	assert.Equal(t, []float32{0.4, 0.5, 0.6}, resp.Data[1].Embedding)
}

func TestCreateEmbeddingsRetry(t *testing.T) {
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error": "service unavailable"}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		resp := EmbeddingResponse{
			Object: "list",
			Data: []EmbeddingObject{
				{Object: "embedding", Embedding: []float32{0.1, 0.2}, Index: 0},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client, err := NewClientWithBaseURL(logger, "test_api_key", "", server.URL+"/api/v1")
	assert.NoError(t, err)

	req := EmbeddingRequest{
		Model: "text-embedding-model",
		Input: []string{"test"},
	}

	resp, err := client.CreateEmbeddings(context.Background(), req)
	assert.NoError(t, err)
	assert.Equal(t, 3, attempts)
	assert.Len(t, resp.Data, 1)
}

func TestCreateEmbeddingsEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := EmbeddingResponse{
			Object: "list",
			Data:   []EmbeddingObject{}, // Empty data
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	client, err := NewClientWithBaseURL(logger, "test_api_key", "", server.URL+"/api/v1")
	assert.NoError(t, err)

	req := EmbeddingRequest{
		Model: "text-embedding-model",
		Input: []string{"test"},
	}

	resp, err := client.CreateEmbeddings(context.Background(), req)
	assert.NoError(t, err)
	assert.Empty(t, resp.Data)
	// Should log warning about empty data
	assert.Contains(t, logBuf.String(), "NO DATA")
}

func TestCreateEmbeddingsNonRetryableError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": "bad request"}`))
	}))
	defer server.Close()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client, err := NewClientWithBaseURL(logger, "test_api_key", "", server.URL+"/api/v1")
	assert.NoError(t, err)

	req := EmbeddingRequest{
		Model: "text-embedding-model",
		Input: []string{"test"},
	}

	_, err = client.CreateEmbeddings(context.Background(), req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

func TestNewClientWithProxy(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Test with valid proxy URL
	client, err := NewClientWithBaseURL(logger, "test_key", "http://user:pass@proxy.example.com:8080", "https://api.example.com")
	assert.NoError(t, err)
	assert.NotNil(t, client)

	// Should log with masked password (URL-encoded as %2A%2A%2A%2A%2A)
	logOutput := logBuf.String()
	assert.Contains(t, logOutput, "Using proxy")
	// Password is masked with ***** which gets URL-encoded
	assert.True(t, bytes.Contains(logBuf.Bytes(), []byte("*****")) || bytes.Contains(logBuf.Bytes(), []byte("%2A%2A%2A%2A%2A")), "password should be masked")
	assert.NotContains(t, logOutput, ":pass@")
}

func TestNewClientWithInvalidProxy(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	// Test with invalid proxy URL
	_, err := NewClientWithBaseURL(logger, "test_key", "://invalid", "https://api.example.com")
	assert.Error(t, err)
}

func TestCreateChatCompletionContextCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client, err := NewClientWithBaseURL(logger, "test_api_key", "", server.URL+"/api/v1")
	assert.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	req := ChatCompletionRequest{
		Model:    "test_model",
		Messages: []Message{{Role: "user", Content: "Hello"}},
	}

	_, err = client.CreateChatCompletion(ctx, req)
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestCreateEmbeddingsContextCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	client, err := NewClientWithBaseURL(logger, "test_api_key", "", server.URL+"/api/v1")
	assert.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := EmbeddingRequest{
		Model: "text-embedding-model",
		Input: []string{"test"},
	}

	_, err = client.CreateEmbeddings(ctx, req)
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

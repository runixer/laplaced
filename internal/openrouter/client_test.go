package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

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
			}{
				{
					Message: struct {
						Role             string      `json:"role"`
						Content          string      `json:"content"`
						ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
						ReasoningDetails interface{} `json:"reasoning_details,omitempty"`
					}{Role: "assistant", Content: "Hello from mock server!"},
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
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
			}{
				{
					Message: struct {
						Role             string      `json:"role"`
						Content          string      `json:"content"`
						ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
						ReasoningDetails interface{} `json:"reasoning_details,omitempty"`
					}{Role: "assistant", Content: "log test"},
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
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
			}{
				{
					Message: struct {
						Role             string      `json:"role"`
						Content          string      `json:"content"`
						ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
						ReasoningDetails interface{} `json:"reasoning_details,omitempty"`
					}{Role: "assistant", Content: "Success after retry!"},
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

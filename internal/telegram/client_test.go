package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	t.Run("without proxy", func(t *testing.T) {
		client, err := NewClient("test-token", "")
		require.NoError(t, err)
		assert.NotNil(t, client)
		assert.Equal(t, "test-token", client.token)
		assert.Equal(t, "https://api.telegram.org/bottest-token", client.apiURL)
	})

	t.Run("with proxy", func(t *testing.T) {
		client, err := NewClient("test-token", "http://proxy.example.com:8080")
		require.NoError(t, err)
		assert.NotNil(t, client)
		assert.Equal(t, "test-token", client.token)
	})

	t.Run("with invalid proxy", func(t *testing.T) {
		client, err := NewClient("test-token", "://invalid-proxy")
		assert.Error(t, err)
		assert.Nil(t, client)
		assert.Contains(t, err.Error(), "failed to parse proxy URL")
	})
}

func TestNewExtendedClient(t *testing.T) {
	t.Run("without proxy", func(t *testing.T) {
		client, err := NewExtendedClient("test-token", "")
		require.NoError(t, err)
		assert.NotNil(t, client)
		assert.Equal(t, "test-token", client.GetToken())
	})

	t.Run("with proxy", func(t *testing.T) {
		client, err := NewExtendedClient("test-token", "http://proxy.example.com:8080")
		require.NoError(t, err)
		assert.NotNil(t, client)
		assert.Equal(t, "test-token", client.GetToken())
	})

	t.Run("with invalid proxy", func(t *testing.T) {
		client, err := NewExtendedClient("test-token", "://invalid-proxy")
		assert.Error(t, err)
		assert.Nil(t, client)
	})
}

func TestSendMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/botfake-token/sendMessage", r.URL.Path)
		var req SendMessageRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, err)
		assert.Equal(t, int64(123), req.ChatID)
		assert.Equal(t, "Hello", req.Text)

		resp := APIResponse{
			Ok:     true,
			Result: json.RawMessage(`{"message_id": 1, "chat": {"id": 123, "type": "private"}, "text": "Hello"}`),
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}

	req := SendMessageRequest{
		ChatID: 123,
		Text:   "Hello",
	}

	msg, err := client.SendMessage(context.Background(), req)
	assert.NoError(t, err)
	assert.NotNil(t, msg)
	assert.Equal(t, 1, msg.MessageID)
}

func TestSetMyCommands(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/botfake-token/setMyCommands", r.URL.Path)
		var req SetMyCommandsRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, err)
		assert.Len(t, req.Commands, 1)
		assert.Equal(t, "start", req.Commands[0].Command)

		resp := APIResponse{
			Ok: true,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}

	req := SetMyCommandsRequest{
		Commands: []BotCommand{
			{Command: "start", Description: "Start the bot"},
		},
	}

	err := client.SetMyCommands(context.Background(), req)
	assert.NoError(t, err)
}

func TestSetWebhook(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/botfake-token/setWebhook", r.URL.Path)
		var req SetWebhookRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, err)
		assert.Equal(t, "https://example.com", req.URL)

		resp := APIResponse{
			Ok: true,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}

	req := SetWebhookRequest{
		URL: "https://example.com",
	}

	err := client.SetWebhook(context.Background(), req)
	assert.NoError(t, err)
}

func TestSendChatAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/botfake-token/sendChatAction", r.URL.Path)
		var req SendChatActionRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, err)
		assert.Equal(t, int64(123), req.ChatID)
		assert.Equal(t, "typing", req.Action)

		resp := APIResponse{
			Ok: true,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}

	req := SendChatActionRequest{
		ChatID: 123,
		Action: "typing",
	}

	err := client.SendChatAction(context.Background(), req)
	assert.NoError(t, err)
}

func TestGetFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/botfake-token/getFile", r.URL.Path)
		var req GetFileRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, err)
		assert.Equal(t, "file-id", req.FileID)

		resp := APIResponse{
			Ok:     true,
			Result: json.RawMessage(`{"file_id": "file-id", "file_path": "path/to/file"}`),
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}

	req := GetFileRequest{
		FileID: "file-id",
	}

	file, err := client.GetFile(context.Background(), req)
	assert.NoError(t, err)
	assert.NotNil(t, file)
	assert.Equal(t, "path/to/file", file.FilePath)
}

func TestSetMessageReaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/botfake-token/setMessageReaction", r.URL.Path)
		var req SetMessageReactionRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, err)
		assert.Equal(t, int64(123), req.ChatID)
		assert.Equal(t, 456, req.MessageID)
		assert.Len(t, req.Reaction, 1)
		assert.Equal(t, "emoji", req.Reaction[0].Type)
		assert.Equal(t, "👍", req.Reaction[0].Emoji)

		resp := APIResponse{
			Ok: true,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}

	req := SetMessageReactionRequest{
		ChatID:    123,
		MessageID: 456,
		Reaction: []ReactionType{
			{Type: "emoji", Emoji: "👍"},
		},
	}

	err := client.SetMessageReaction(context.Background(), req)
	assert.NoError(t, err)
}

func TestGetUpdates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/botfake-token/getUpdates", r.URL.Path)
		var req GetUpdatesRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		assert.NoError(t, err)
		assert.Equal(t, 10, req.Offset)
		assert.Equal(t, 30, req.Timeout)

		resp := APIResponse{
			Ok:     true,
			Result: json.RawMessage(`[{"update_id": 10, "message": {"message_id": 1, "text": "Hello"}}]`),
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &Client{
		token:             "fake-token",
		httpClient:        server.Client(),
		longPollingClient: server.Client(),
		apiURL:            server.URL + "/botfake-token",
	}

	req := GetUpdatesRequest{
		Offset:  10,
		Timeout: 30,
	}

	updates, err := client.GetUpdates(context.Background(), req)
	assert.NoError(t, err)
	assert.Len(t, updates, 1)
	assert.Equal(t, 10, updates[0].UpdateID)
	assert.Equal(t, "Hello", updates[0].Message.Text)
}

func TestSanitizeError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		token    string
		expected string
	}{
		{
			name:     "nil error",
			err:      nil,
			token:    "secret-token",
			expected: "",
		},
		{
			name:     "empty token",
			err:      assert.AnError,
			token:    "",
			expected: assert.AnError.Error(),
		},
		{
			name:     "error with token in URL",
			err:      fmt.Errorf(`Post "https://api.telegram.org/bot123456:ABC-DEF/sendChatAction": context canceled`),
			token:    "123456:ABC-DEF",
			expected: `Post "https://api.telegram.org/bot[REDACTED]/sendChatAction": context canceled`,
		},
		{
			name:     "error without token",
			err:      fmt.Errorf("connection refused"),
			token:    "secret-token",
			expected: "connection refused",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeError(tt.err, tt.token)
			if tt.err == nil {
				assert.Nil(t, result)
			} else {
				assert.Equal(t, tt.expected, result.Error())
			}
		})
	}
}

func TestIsTimeoutError(t *testing.T) {
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
			name:     "timeout error string",
			err:      fmt.Errorf("context deadline exceeded"),
			expected: true,
		},
		{
			name:     "context canceled",
			err:      fmt.Errorf("context canceled"),
			expected: true,
		},
		{
			name:     "timeout in error message",
			err:      fmt.Errorf("dial tcp: timeout"),
			expected: true,
		},
		{
			name:     "non-timeout error",
			err:      fmt.Errorf("connection refused"),
			expected: false,
		},
		{
			name:     "network error",
			err:      fmt.Errorf("no such host"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTimeoutError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMakeRequest_RetrySuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry test in short mode")
	}
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			// First attempt fails
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Second attempt succeeds
		resp := APIResponse{Ok: true}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}

	resp, err := client.makeRequest(context.Background(), "testMethod", map[string]string{"key": "value"})
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.True(t, resp.Ok)
	assert.Equal(t, 2, attempts)
}

func TestMakeRequest_RetryExhausted(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry test in short mode")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always fail
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := &Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}

	resp, err := client.makeRequest(context.Background(), "testMethod", map[string]string{})
	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "failed to decode response")
}

func TestMakeRequest_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Wait for context cancellation
		<-r.Context().Done()
	}))
	defer server.Close()

	client := &Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	resp, err := client.makeRequest(ctx, "testMethod", map[string]string{})
	assert.Error(t, err)
	assert.Nil(t, resp)
	// Error contains context cancellation message (wrapped by fmt.Errorf)
	assert.Contains(t, err.Error(), "context canceled")
}

func TestMakeRequest_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := APIResponse{
			Ok:          false,
			Description: "Bad Request: chat not found",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}

	resp, err := client.makeRequest(context.Background(), "sendMessage", map[string]string{})
	assert.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "chat not found")
}

func TestMakeRequest_DecodeError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry test in short mode")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return invalid JSON
		_, _ = w.Write([]byte("invalid json"))
	}))
	defer server.Close()

	client := &Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}

	resp, err := client.makeRequest(context.Background(), "testMethod", map[string]string{})
	assert.Error(t, err)
	assert.Nil(t, resp)
	// After retries, should get decode error
	assert.Contains(t, err.Error(), "failed to decode response")
}

func TestMakeRequest_NetworkError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network error test in short mode")
	}
	// Use an invalid URL to trigger network error
	client := &Client{
		token:      "fake-token",
		httpClient: &http.Client{Timeout: 1 * time.Second},
		apiURL:     "http://localhost:9999/botfake-token",
	}

	_, err := client.makeRequest(context.Background(), "testMethod", map[string]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to perform request")
}

func TestSendMessage_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return API response with result as wrong type (string instead of object)
		_, _ = w.Write([]byte(`{"ok": true, "result": "not a message object"}`))
	}))
	defer server.Close()

	client := &Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}

	req := SendMessageRequest{ChatID: 123, Text: "Hello"}
	msg, err := client.SendMessage(context.Background(), req)
	assert.Error(t, err)
	assert.Nil(t, msg)
	assert.Contains(t, err.Error(), "failed to unmarshal message")
}

func TestGetFile_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return API response with result as wrong type (string instead of object)
		_, _ = w.Write([]byte(`{"ok": true, "result": "not a file object"}`))
	}))
	defer server.Close()

	client := &Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}

	req := GetFileRequest{FileID: "file-id"}
	file, err := client.GetFile(context.Background(), req)
	assert.Error(t, err)
	assert.Nil(t, file)
	assert.Contains(t, err.Error(), "failed to unmarshal file")
}

func TestGetUpdates_TimeoutError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}
	// Use invalid URL to trigger connection timeout
	client := &Client{
		token:             "fake-token",
		longPollingClient: &http.Client{Timeout: 100 * time.Millisecond},
		apiURL:            "http://localhost:9999/botfake-token",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	req := GetUpdatesRequest{Offset: 0, Timeout: 0}
	updates, err := client.GetUpdates(ctx, req)
	assert.Error(t, err)
	assert.Nil(t, updates)
}

func TestGetUpdates_DecodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return invalid JSON
		_, _ = w.Write([]byte("invalid json"))
	}))
	defer server.Close()

	client := &Client{
		token:             "fake-token",
		longPollingClient: server.Client(),
		apiURL:            server.URL + "/botfake-token",
	}

	req := GetUpdatesRequest{Offset: 0, Timeout: 30}
	updates, err := client.GetUpdates(context.Background(), req)
	assert.Error(t, err)
	assert.Nil(t, updates)
	assert.Contains(t, err.Error(), "failed to decode response")
}

func TestGetUpdates_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := APIResponse{
			Ok:          false,
			Description: "Conflict: can't use getUpdates while webhook is active",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &Client{
		token:             "fake-token",
		longPollingClient: server.Client(),
		apiURL:            server.URL + "/botfake-token",
	}

	req := GetUpdatesRequest{Offset: 0, Timeout: 30}
	updates, err := client.GetUpdates(context.Background(), req)
	assert.Error(t, err)
	assert.Nil(t, updates)
	assert.Contains(t, err.Error(), "webhook is active")
}

func TestGetUpdates_UnmarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return valid APIResponse but invalid result for updates array
		_, _ = w.Write([]byte(`{"ok": true, "result": "not an array"}`))
	}))
	defer server.Close()

	client := &Client{
		token:             "fake-token",
		longPollingClient: server.Client(),
		apiURL:            server.URL + "/botfake-token",
	}

	req := GetUpdatesRequest{Offset: 0, Timeout: 30}
	updates, err := client.GetUpdates(context.Background(), req)
	assert.Error(t, err)
	assert.Nil(t, updates)
	assert.Contains(t, err.Error(), "failed to unmarshal updates")
}

func TestGetUpdates_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := APIResponse{
			Ok:     true,
			Result: json.RawMessage(`[]`),
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &Client{
		token:             "fake-token",
		longPollingClient: server.Client(),
		apiURL:            server.URL + "/botfake-token",
	}

	req := GetUpdatesRequest{Offset: 0, Timeout: 30}
	updates, err := client.GetUpdates(context.Background(), req)
	assert.NoError(t, err)
	assert.NotNil(t, updates)
	assert.Empty(t, updates)
}

package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

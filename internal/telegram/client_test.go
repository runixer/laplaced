package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

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

func TestSendRichMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/botfake-token/sendRichMessage", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{
			"chat_id": 123,
			"message_thread_id": 7,
			"rich_message": {
				"html": "<blockquote expandable>details</blockquote>",
				"skip_entity_detection": true
			},
			"reply_parameters": {"message_id": 42}
		}`, string(body))

		resp := APIResponse{
			Ok:     true,
			Result: json.RawMessage(`{"message_id": 8, "chat": {"id": 123, "type": "private"}}`),
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer server.Close()

	threadID := 7
	msg, err := (&Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}).SendRichMessage(context.Background(), SendRichMessageRequest{
		ChatID:          123,
		MessageThreadID: &threadID,
		RichMessage: InputRichMessage{
			HTML:                "<blockquote expandable>details</blockquote>",
			SkipEntityDetection: true,
		},
		ReplyParameters: &ReplyParameters{MessageID: 42},
	})

	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, 8, msg.MessageID)
}

func TestSendRichMessage_MultipartPhoto(t *testing.T) {
	photoBytes := []byte("generated-png-bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/botfake-token/sendRichMessage", r.URL.Path)
		assert.True(t, strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data; boundary="))
		require.NoError(t, r.ParseMultipartForm(1<<20))

		require.NotNil(t, r.MultipartForm)
		assert.Len(t, r.MultipartForm.Value, 4)
		assert.Equal(t, []string{"123"}, r.MultipartForm.Value["chat_id"])
		assert.Equal(t, []string{"7"}, r.MultipartForm.Value["message_thread_id"])
		require.Len(t, r.MultipartForm.Value["rich_message"], 1)
		assert.JSONEq(t, `{
			"html": "<img src=\"tg://photo?id=hero\"/>",
			"media": [{
				"id": "hero",
				"media": {"type": "photo", "media": "attach://hero_file"}
			}],
			"skip_entity_detection": true
		}`, r.MultipartForm.Value["rich_message"][0])
		require.Len(t, r.MultipartForm.Value["reply_parameters"], 1)
		assert.JSONEq(t, `{"message_id":42}`, r.MultipartForm.Value["reply_parameters"][0])

		assert.Len(t, r.MultipartForm.File, 1)
		files := r.MultipartForm.File["hero_file"]
		require.Len(t, files, 1)
		assert.Equal(t, "hero.png", files[0].Filename)
		assert.Equal(t, "application/octet-stream", files[0].Header.Get("Content-Type"))
		file, err := files[0].Open()
		require.NoError(t, err)
		defer file.Close()
		got, err := io.ReadAll(file)
		require.NoError(t, err)
		assert.Equal(t, photoBytes, got)

		require.NoError(t, json.NewEncoder(w).Encode(APIResponse{
			Ok:     true,
			Result: json.RawMessage(`{"message_id": 88, "chat": {"id": 123, "type": "private"}}`),
		}))
	}))
	defer server.Close()

	threadID := 7
	msg, err := (&Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}).SendRichMessage(context.Background(), SendRichMessageRequest{
		ChatID:          123,
		MessageThreadID: &threadID,
		RichMessage: InputRichMessage{
			HTML: `<img src="tg://photo?id=hero"/>`,
			Media: []InputRichMessageMedia{{
				ID:    "hero",
				Media: InputRichMessagePhoto{Media: "attach://hero_file"},
			}},
			SkipEntityDetection: true,
		},
		ReplyParameters: &ReplyParameters{MessageID: 42},
		Attachments: []RichMessageAttachment{{
			ID: "hero_file", Filename: "hero.png", Data: photoBytes,
		}},
	})

	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, 88, msg.MessageID)
}

func TestSendRichMessage_MultipartRejectsInvalidAttachmentsBeforeRequest(t *testing.T) {
	validRequest := func() SendRichMessageRequest {
		return SendRichMessageRequest{
			ChatID: 123,
			RichMessage: InputRichMessage{
				HTML: `<img src="tg://photo?id=hero"/>`,
				Media: []InputRichMessageMedia{{
					ID:    "hero",
					Media: InputRichMessagePhoto{Media: "attach://hero_file"},
				}},
			},
			Attachments: []RichMessageAttachment{{
				ID: "hero_file", Filename: "hero.png", Data: []byte("png"),
			}},
		}
	}

	tests := []struct {
		name   string
		mutate func(*SendRichMessageRequest)
		want   string
	}{
		{name: "empty attachment id", mutate: func(r *SendRichMessageRequest) {
			r.Attachments[0].ID = ""
		}, want: "invalid id"},
		{name: "invalid attachment id", mutate: func(r *SendRichMessageRequest) {
			r.Attachments[0].ID = "bad id"
		}, want: "invalid id"},
		{name: "duplicate attachment id", mutate: func(r *SendRichMessageRequest) {
			r.Attachments = append(r.Attachments, r.Attachments[0])
		}, want: "duplicated"},
		{name: "empty filename", mutate: func(r *SendRichMessageRequest) {
			r.Attachments[0].Filename = " "
		}, want: "empty filename"},
		{name: "empty data", mutate: func(r *SendRichMessageRequest) {
			r.Attachments[0].Data = nil
		}, want: "empty data"},
		{name: "unreferenced attachment", mutate: func(r *SendRichMessageRequest) {
			r.Attachments[0].ID = "other_file"
		}, want: "not referenced"},
		{name: "missing all referenced uploads", mutate: func(r *SendRichMessageRequest) {
			r.Attachments = nil
		}, want: "has no uploaded file"},
		{name: "missing referenced upload", mutate: func(r *SendRichMessageRequest) {
			r.RichMessage.Media = append(r.RichMessage.Media, InputRichMessageMedia{
				ID: "second", Media: InputRichMessagePhoto{Media: "attach://second_file"},
			})
		}, want: "has no uploaded file"},
		{name: "duplicate media id", mutate: func(r *SendRichMessageRequest) {
			r.RichMessage.Media = append(r.RichMessage.Media, InputRichMessageMedia{
				ID: "hero", Media: InputRichMessagePhoto{Media: "https://example.com/other.png"},
			})
		}, want: "media id"},
		{name: "duplicate local reference", mutate: func(r *SendRichMessageRequest) {
			r.RichMessage.Media = append(r.RichMessage.Media, InputRichMessageMedia{
				ID: "second", Media: InputRichMessagePhoto{Media: "attach://hero_file"},
			})
		}, want: "referenced more than once"},
		{name: "empty photo source", mutate: func(r *SendRichMessageRequest) {
			r.RichMessage.Media[0].Media.Media = ""
		}, want: "empty photo source"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := &Client{
				httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					return nil, errors.New("must not be called")
				})},
				apiURL: "https://api.telegram.invalid/botfake-token",
			}
			req := validRequest()
			tt.mutate(&req)

			msg, err := client.SendRichMessage(context.Background(), req)

			require.Error(t, err)
			assert.Nil(t, msg)
			assert.Contains(t, err.Error(), tt.want)
			assert.Zero(t, calls)
		})
	}
}

func TestSendRichMessage_MultipartAPIRejectionIsStructured(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(APIResponse{
			Ok:          false,
			ErrorCode:   400,
			Description: "Bad Request: invalid rich media",
			Parameters:  &ResponseParameters{RetryAfter: 3},
		}))
	}))
	defer server.Close()

	client := &Client{token: "fake-token", httpClient: server.Client(), apiURL: server.URL + "/botfake-token"}
	_, err := client.SendRichMessage(context.Background(), SendRichMessageRequest{
		ChatID: 123,
		RichMessage: InputRichMessage{
			HTML: `<img src="tg://photo?id=hero"/>`,
			Media: []InputRichMessageMedia{{
				ID: "hero", Media: InputRichMessagePhoto{Media: "attach://hero_file"},
			}},
		},
		Attachments: []RichMessageAttachment{{
			ID: "hero_file", Filename: "hero.png", Data: []byte("png"),
		}},
	})

	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, 400, apiErr.Code)
	assert.Equal(t, "Bad Request: invalid rich media", apiErr.Description)
	require.NotNil(t, apiErr.Parameters)
	assert.Equal(t, 3, apiErr.Parameters.RetryAfter)
}

func TestSendRichMessage_MultipartAmbiguousFailureIsNotRetried(t *testing.T) {
	calls := 0
	client := &Client{
		token: "fake-token",
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("connection reset after request write for fake-token")
		})},
		apiURL: "https://api.telegram.invalid/botfake-token",
	}

	_, err := client.SendRichMessage(context.Background(), SendRichMessageRequest{
		ChatID: 123,
		RichMessage: InputRichMessage{
			HTML: `<img src="tg://photo?id=hero"/>`,
			Media: []InputRichMessageMedia{{
				ID: "hero", Media: InputRichMessagePhoto{Media: "attach://hero_file"},
			}},
		},
		Attachments: []RichMessageAttachment{{
			ID: "hero_file", Filename: "hero.png", Data: []byte("png"),
		}},
	})

	require.Error(t, err)
	assert.Equal(t, 1, calls)
	var apiErr *APIError
	assert.False(t, errors.As(err, &apiErr))
	assert.NotContains(t, err.Error(), "fake-token")
}

func TestSendRichMessageRequest_OmitsOptionalFields(t *testing.T) {
	body, err := json.Marshal(SendRichMessageRequest{
		ChatID:      123,
		RichMessage: InputRichMessage{HTML: "<b>Hello</b>"},
		Attachments: []RichMessageAttachment{{ID: "ignored", Filename: "ignored.png", Data: []byte("ignored")}},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"chat_id": 123,
		"rich_message": {"html": "<b>Hello</b>"}
	}`, string(body))
}

func TestSendRichMessageDraft(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/botfake-token/sendRichMessageDraft", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{
			"chat_id": 123,
			"message_thread_id": 7,
			"draft_id": 987654321,
			"rich_message": {
				"html": "<tg-thinking>Working…</tg-thinking><p>Partial</p>",
				"skip_entity_detection": true
			}
		}`, string(body))

		require.NoError(t, json.NewEncoder(w).Encode(APIResponse{
			Ok:     true,
			Result: json.RawMessage(`true`),
		}))
	}))
	defer server.Close()

	threadID := 7
	err := (&Client{
		token:      "fake-token",
		httpClient: server.Client(),
		apiURL:     server.URL + "/botfake-token",
	}).SendRichMessageDraft(context.Background(), SendRichMessageDraftRequest{
		ChatID:          123,
		MessageThreadID: &threadID,
		DraftID:         987654321,
		RichMessage: InputRichMessage{
			HTML:                "<tg-thinking>Working…</tg-thinking><p>Partial</p>",
			SkipEntityDetection: true,
		},
	})

	require.NoError(t, err)
}

func TestSendRichMessageDraftRequest_OmitsOptionalFields(t *testing.T) {
	body, err := json.Marshal(SendRichMessageDraftRequest{
		ChatID:      123,
		DraftID:     9,
		RichMessage: InputRichMessage{HTML: "<p>Partial</p>"},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"chat_id": 123,
		"draft_id": 9,
		"rich_message": {"html": "<p>Partial</p>"}
	}`, string(body))
}

func TestSendRichMessageDraft_RejectsInvalidDraftIDBeforeRequest(t *testing.T) {
	for _, draftID := range []int64{0, -1, 1 << 31} {
		calls := 0
		client := &Client{
			httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("must not be called")
			})},
			apiURL: "https://api.telegram.invalid/botfake-token",
		}

		err := client.SendRichMessageDraft(context.Background(), SendRichMessageDraftRequest{
			ChatID:      123,
			DraftID:     draftID,
			RichMessage: InputRichMessage{HTML: "<p>Partial</p>"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "positive int32 draft_id")
		assert.Zero(t, calls)
	}
}

func TestSendRichMessageDraft_RequiresTrueResult(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing result", body: `{"ok":true}`},
		{name: "null result", body: `{"ok":true,"result":null}`},
		{name: "false result", body: `{"ok":true,"result":false}`},
		{name: "string result", body: `{"ok":true,"result":"true"}`},
		{name: "object result", body: `{"ok":true,"result":{}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			err := (&Client{
				token:      "fake-token",
				httpClient: server.Client(),
				apiURL:     server.URL + "/botfake-token",
			}).SendRichMessageDraft(context.Background(), SendRichMessageDraftRequest{
				ChatID:      123,
				DraftID:     9,
				RichMessage: InputRichMessage{HTML: "<p>Partial</p>"},
			})

			require.Error(t, err)
		})
	}
}

func TestSendRichMessageDraft_TransportRetryReusesDraftID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry test in short mode")
	}

	calls := 0
	var bodies []string
	client := &Client{
		token: "fake-token",
		httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			bodies = append(bodies, string(body))
			if calls == 1 {
				return nil, errors.New("connection reset after request write")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`)),
			}, nil
		})},
		apiURL: "https://api.telegram.invalid/botfake-token",
	}

	err := client.SendRichMessageDraft(context.Background(), SendRichMessageDraftRequest{
		ChatID:      123,
		DraftID:     44,
		RichMessage: InputRichMessage{HTML: "<p>Partial</p>"},
	})

	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	require.Len(t, bodies, 2)
	assert.JSONEq(t, bodies[0], bodies[1])
	assert.JSONEq(t, `{
		"chat_id": 123,
		"draft_id": 44,
		"rich_message": {"html": "<p>Partial</p>"}
	}`, bodies[0])
}

func TestSendRichMessage_AmbiguousNetworkFailureIsNotRetried(t *testing.T) {
	calls := 0
	client := &Client{
		token: "fake-token",
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("connection reset after request write")
		})},
		apiURL: "https://api.telegram.invalid/botfake-token",
	}

	_, err := client.SendRichMessage(context.Background(), SendRichMessageRequest{
		ChatID:      123,
		RichMessage: InputRichMessage{HTML: "<p>answer</p>"},
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls)
	var apiErr *APIError
	assert.False(t, errors.As(err, &apiErr))
	assert.NotContains(t, err.Error(), "fake-token")
}

func TestSendMessage_AmbiguousNetworkFailureIsNotRetried(t *testing.T) {
	calls := 0
	client := &Client{
		token: "fake-token",
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("connection reset after request write")
		})},
		apiURL: "https://api.telegram.invalid/botfake-token",
	}

	_, err := client.SendMessage(context.Background(), SendMessageRequest{ChatID: 123, Text: "unique answer"})
	require.Error(t, err)
	assert.Equal(t, 1, calls)
	var apiErr *APIError
	assert.False(t, errors.As(err, &apiErr))
	assert.NotContains(t, err.Error(), "fake-token")
}

func TestPersistentSendRejectsMalformedSuccess(t *testing.T) {
	results := []struct {
		name string
		body string
	}{
		{name: "missing result", body: `{"ok":true}`},
		{name: "null result", body: `{"ok":true,"result":null}`},
		{name: "empty message", body: `{"ok":true,"result":{}}`},
		{name: "wrong result type", body: `{"ok":true,"result":"not a message"}`},
	}
	methods := []string{"sendMessage", "sendRichMessage"}

	for _, method := range methods {
		for _, result := range results {
			t.Run(method+"/"+result.name, func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					assert.Equal(t, "/botfake-token/"+method, r.URL.Path)
					_, _ = w.Write([]byte(result.body))
				}))
				defer server.Close()

				client := &Client{
					token:      "fake-token",
					httpClient: server.Client(),
					apiURL:     server.URL + "/botfake-token",
				}
				var msg *Message
				var err error
				if method == "sendMessage" {
					msg, err = client.SendMessage(context.Background(), SendMessageRequest{ChatID: 123, Text: "answer"})
				} else {
					msg, err = client.SendRichMessage(context.Background(), SendRichMessageRequest{
						ChatID:      123,
						RichMessage: InputRichMessage{HTML: "<p>answer</p>"},
					})
				}
				require.Error(t, err)
				assert.Nil(t, msg)
				assert.Equal(t, 1, calls)
				var apiErr *APIError
				assert.False(t, errors.As(err, &apiErr))
			})
		}
	}
}

func TestEditMessageText(t *testing.T) {
	t.Run("success returns edited message", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/botfake-token/editMessageText", r.URL.Path)
			var req EditMessageTextRequest
			err := json.NewDecoder(r.Body).Decode(&req)
			assert.NoError(t, err)
			assert.Equal(t, int64(123), req.ChatID)
			assert.Equal(t, 42, req.MessageID)
			assert.Equal(t, "<b>updated</b>", req.Text)
			assert.Equal(t, "HTML", req.ParseMode)

			resp := APIResponse{
				Ok:     true,
				Result: json.RawMessage(`{"message_id": 42, "chat": {"id": 123, "type": "private"}, "text": "updated"}`),
			}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		client := &Client{
			token:      "fake-token",
			httpClient: server.Client(),
			apiURL:     server.URL + "/botfake-token",
		}
		msg, err := client.EditMessageText(context.Background(), EditMessageTextRequest{
			ChatID:    123,
			MessageID: 42,
			Text:      "<b>updated</b>",
			ParseMode: "HTML",
		})
		assert.NoError(t, err)
		require.NotNil(t, msg)
		assert.Equal(t, 42, msg.MessageID)
	})

	t.Run("message-not-modified returns sentinel error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := APIResponse{
				Ok:          false,
				Description: "Bad Request: message is not modified: specified new message content and reply markup are exactly the same",
				ErrorCode:   400,
			}
			w.WriteHeader(http.StatusOK) // Telegram returns 200 with ok:false
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		client := &Client{
			token:      "fake-token",
			httpClient: server.Client(),
			apiURL:     server.URL + "/botfake-token",
		}
		_, err := client.EditMessageText(context.Background(), EditMessageTextRequest{
			ChatID: 1, MessageID: 1, Text: "x",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrMessageNotModified, "edit must surface ErrMessageNotModified for the not-modified case")
	})

	t.Run("other API error propagates", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := APIResponse{
				Ok:          false,
				Description: "Bad Request: chat not found",
				ErrorCode:   400,
			}
			_ = json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		client := &Client{
			token:      "fake-token",
			httpClient: server.Client(),
			apiURL:     server.URL + "/botfake-token",
		}
		_, err := client.EditMessageText(context.Background(), EditMessageTextRequest{
			ChatID: 1, MessageID: 1, Text: "x",
		})
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrMessageNotModified)
		assert.Contains(t, err.Error(), "chat not found")
	})
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
		assert.Equal(t, AllowedUpdateTypes(), req.AllowedUpdates)

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
		URL:            "https://example.com",
		AllowedUpdates: AllowedUpdateTypes(),
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
		assert.Equal(t, AllowedUpdateTypes(), req.AllowedUpdates)

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
		Offset:         10,
		Timeout:        30,
		AllowedUpdates: AllowedUpdateTypes(),
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
			ErrorCode:   429,
			Parameters:  &ResponseParameters{RetryAfter: 17},
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
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "chat not found")
	assert.NotContains(t, err.Error(), "fake-token")

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, 429, apiErr.Code)
	assert.Equal(t, "Bad Request: chat not found", apiErr.Description)
	require.NotNil(t, apiErr.Parameters)
	assert.Equal(t, 17, apiErr.Parameters.RetryAfter)
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
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to perform request")
	var apiErr *APIError
	assert.False(t, errors.As(err, &apiErr))
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

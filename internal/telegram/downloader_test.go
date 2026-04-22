package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockBotAPI is a simple mock for BotAPI interface.
type mockBotAPI struct {
	token    string
	getFile  func(ctx context.Context, req GetFileRequest) (*File, error)
	getToken func() string
}

func (m *mockBotAPI) SendMessage(ctx context.Context, req SendMessageRequest) (*Message, error) {
	return nil, nil
}

func (m *mockBotAPI) SendPhoto(ctx context.Context, req SendPhotoRequest) (*Message, error) {
	return nil, nil
}

func (m *mockBotAPI) SendDocument(ctx context.Context, req SendDocumentRequest) (*Message, error) {
	return nil, nil
}

func (m *mockBotAPI) SendMediaGroup(ctx context.Context, req SendMediaGroupRequest) ([]Message, error) {
	return nil, nil
}

func (m *mockBotAPI) SendMediaGroupDocuments(ctx context.Context, req SendMediaGroupDocumentsRequest) ([]Message, error) {
	return nil, nil
}

func (m *mockBotAPI) SetMyCommands(ctx context.Context, req SetMyCommandsRequest) error {
	return nil
}

func (m *mockBotAPI) SetWebhook(ctx context.Context, req SetWebhookRequest) error {
	return nil
}

func (m *mockBotAPI) SendChatAction(ctx context.Context, req SendChatActionRequest) error {
	return nil
}

func (m *mockBotAPI) GetFile(ctx context.Context, req GetFileRequest) (*File, error) {
	if m.getFile != nil {
		return m.getFile(ctx, req)
	}
	return &File{FilePath: "test/path/file.bin"}, nil
}

func (m *mockBotAPI) SetMessageReaction(ctx context.Context, req SetMessageReactionRequest) error {
	return nil
}

func (m *mockBotAPI) GetUpdates(ctx context.Context, req GetUpdatesRequest) ([]Update, error) {
	return nil, nil
}

func (m *mockBotAPI) GetToken() string {
	if m.getToken != nil {
		return m.getToken()
	}
	return m.token
}

func TestNewHTTPFileDownloader(t *testing.T) {
	mockAPI := &mockBotAPI{token: "test-token"}

	t.Run("without proxy", func(t *testing.T) {
		downloader, err := NewHTTPFileDownloader(mockAPI, "https://api.telegram.org", "")
		require.NoError(t, err)
		assert.NotNil(t, downloader)
		assert.Equal(t, mockAPI, downloader.api)
		assert.Equal(t, "https://api.telegram.org", downloader.fileBaseURL)
	})

	t.Run("with proxy", func(t *testing.T) {
		downloader, err := NewHTTPFileDownloader(mockAPI, "https://api.telegram.org", "http://proxy.example.com:8080")
		require.NoError(t, err)
		assert.NotNil(t, downloader)
		assert.Equal(t, mockAPI, downloader.api)
	})

	t.Run("with invalid proxy URL", func(t *testing.T) {
		downloader, err := NewHTTPFileDownloader(mockAPI, "https://api.telegram.org", "://invalid-proxy")
		assert.Error(t, err)
		assert.Nil(t, downloader)
		assert.Contains(t, err.Error(), "failed to parse proxy URL")
	})
}

func TestDownloadFile(t *testing.T) {
	testContent := []byte("test file content")

	t.Run("success", func(t *testing.T) {
		// Mock Telegram file server
		fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify URL structure
			assert.True(t, strings.Contains(r.URL.Path, "/file/"))
			assert.True(t, strings.Contains(r.URL.Path, "test/path/file.bin"))
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(testContent)
		}))
		defer fileServer.Close()

		mockAPI := &mockBotAPI{token: "test-token"}
		downloader, err := NewHTTPFileDownloader(mockAPI, fileServer.URL, "")
		require.NoError(t, err)

		content, err := downloader.DownloadFile(context.Background(), "file-id-123")
		assert.NoError(t, err)
		assert.Equal(t, testContent, content)
	})

	t.Run("GetFile API error", func(t *testing.T) {
		mockAPI := &mockBotAPI{
			token: "test-token",
			getFile: func(ctx context.Context, req GetFileRequest) (*File, error) {
				return nil, fmt.Errorf("telegram API error: not found")
			},
		}
		downloader, err := NewHTTPFileDownloader(mockAPI, "https://api.telegram.org", "")
		require.NoError(t, err)

		content, err := downloader.DownloadFile(context.Background(), "file-id-123")
		assert.Error(t, err)
		assert.Nil(t, content)
		assert.Contains(t, err.Error(), "failed to get file info")
	})

	t.Run("HTTP download error", func(t *testing.T) {
		// Server that always fails
		fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer fileServer.Close()

		mockAPI := &mockBotAPI{token: "test-token"}
		downloader, err := NewHTTPFileDownloader(mockAPI, fileServer.URL, "")
		require.NoError(t, err)

		content, err := downloader.DownloadFile(context.Background(), "file-id-123")
		assert.Error(t, err)
		assert.Nil(t, content)
		assert.Contains(t, err.Error(), "failed to download file")
	})

	t.Run("non-OK status code", func(t *testing.T) {
		// Server that returns 404
		fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer fileServer.Close()

		mockAPI := &mockBotAPI{token: "test-token"}
		downloader, err := NewHTTPFileDownloader(mockAPI, fileServer.URL, "")
		require.NoError(t, err)

		content, err := downloader.DownloadFile(context.Background(), "file-id-123")
		assert.Error(t, err)
		assert.Nil(t, content)
		assert.Contains(t, err.Error(), "status code 404")
	})

	t.Run("context cancellation", func(t *testing.T) {
		// Server that delays response
		fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer fileServer.Close()

		mockAPI := &mockBotAPI{token: "test-token"}
		downloader, err := NewHTTPFileDownloader(mockAPI, fileServer.URL, "")
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		content, err := downloader.DownloadFile(ctx, "file-id-123")
		assert.Error(t, err)
		assert.Nil(t, content)
	})

	t.Run("token sanitized in error", func(t *testing.T) {
		// Server with invalid URL that will cause client error
		fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer fileServer.Close()

		// Create downloader then close server to simulate network error
		mockAPI := &mockBotAPI{token: "secret-token-12345"}
		downloader, err := NewHTTPFileDownloader(mockAPI, fileServer.URL, "")
		require.NoError(t, err)

		// Close server to force error on next request
		fileServer.Close()

		content, err := downloader.DownloadFile(context.Background(), "file-id-123")
		assert.Error(t, err)
		assert.Nil(t, content)
		// Token should be redacted from error message
		assert.NotContains(t, err.Error(), "secret-token-12345")
	})
}

func TestDownloadFileAsBase64(t *testing.T) {
	testContent := []byte("test file content")

	t.Run("success", func(t *testing.T) {
		// Mock Telegram file server
		fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(testContent)
		}))
		defer fileServer.Close()

		mockAPI := &mockBotAPI{token: "test-token"}
		downloader, err := NewHTTPFileDownloader(mockAPI, fileServer.URL, "")
		require.NoError(t, err)

		base64Str, err := downloader.DownloadFileAsBase64(context.Background(), "file-id-123")
		assert.NoError(t, err)
		expected := "dGVzdCBmaWxlIGNvbnRlbnQ="
		assert.Equal(t, expected, base64Str)
	})

	t.Run("error propagation", func(t *testing.T) {
		mockAPI := &mockBotAPI{
			token: "test-token",
			getFile: func(ctx context.Context, req GetFileRequest) (*File, error) {
				return nil, fmt.Errorf("API error")
			},
		}
		downloader, err := NewHTTPFileDownloader(mockAPI, "https://api.telegram.org", "")
		require.NoError(t, err)

		base64Str, err := downloader.DownloadFileAsBase64(context.Background(), "file-id-123")
		assert.Error(t, err)
		assert.Empty(t, base64Str)
	})

	t.Run("empty file", func(t *testing.T) {
		// Mock Telegram file server
		fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			// No content written
		}))
		defer fileServer.Close()

		mockAPI := &mockBotAPI{token: "test-token"}
		downloader, err := NewHTTPFileDownloader(mockAPI, fileServer.URL, "")
		require.NoError(t, err)

		base64Str, err := downloader.DownloadFileAsBase64(context.Background(), "file-id-123")
		assert.NoError(t, err)
		assert.Equal(t, "", base64Str)
	})

	t.Run("binary content", func(t *testing.T) {
		// Test with actual binary bytes
		binaryContent := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}

		fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(binaryContent)
		}))
		defer fileServer.Close()

		mockAPI := &mockBotAPI{token: "test-token"}
		downloader, err := NewHTTPFileDownloader(mockAPI, fileServer.URL, "")
		require.NoError(t, err)

		base64Str, err := downloader.DownloadFileAsBase64(context.Background(), "file-id-123")
		assert.NoError(t, err)
		assert.Equal(t, "AAEC//79", base64Str)
	})
}

// Test mockBotAPI_GetToken verifies the mock works correctly.
func TestMockBotAPI_GetToken(t *testing.T) {
	mockAPI := &mockBotAPI{token: "test-token-123"}
	assert.Equal(t, "test-token-123", mockAPI.GetToken())

	mockAPI.getToken = func() string {
		return "custom-token"
	}
	assert.Equal(t, "custom-token", mockAPI.GetToken())
}

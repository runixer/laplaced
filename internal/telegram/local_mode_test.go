package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient_APIBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"file_id": "f", "file_path": "/data/x"}})
	}))
	defer srv.Close()

	tests := []struct {
		name string
		opts []ClientOption
	}{
		{"self-hosted base URL", []ClientOption{WithAPIBaseURL(srv.URL)}},
		{"trailing slash is trimmed", []ClientOption{WithAPIBaseURL(srv.URL + "/")}},
		{"local mode routes getFile through the long-transfer client", []ClientOption{WithAPIBaseURL(srv.URL), WithLocalMode(true)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewClient("123:abc", "", tt.opts...)
			require.NoError(t, err)
			f, err := c.GetFile(context.Background(), GetFileRequest{FileID: "f"})
			require.NoError(t, err)
			assert.Equal(t, "/bot123:abc/getFile", gotPath)
			assert.Equal(t, "/data/x", f.FilePath)
		})
	}
}

func TestNewClient_LocalModeTimeouts(t *testing.T) {
	public, err := NewClient("t", "")
	require.NoError(t, err)
	local, err := NewClient("t", "", WithAPIBaseURL("http://bot-api:8081"), WithLocalMode(true))
	require.NoError(t, err)

	assert.Equal(t, "https://api.telegram.org/bott", public.apiURL)
	assert.Equal(t, "http://bot-api:8081/bott", local.apiURL)
	assert.Equal(t, publicUploadTimeout, public.uploadClient.Timeout)
	assert.Equal(t, localTransferTimeout, local.uploadClient.Timeout,
		"a 2000 MB upload through a local server must not hit the public 3m budget")
}

func TestDownloadFile_LocalPath(t *testing.T) {
	root := t.TempDir()
	tokenDir := filepath.Join(root, "123:abc", "voice")
	require.NoError(t, os.MkdirAll(tokenDir, 0o755))
	inside := filepath.Join(tokenDir, "file_1.oga")
	require.NoError(t, os.WriteFile(inside, []byte("voice bytes"), 0o600))
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(tokenDir, "link.oga")))

	tests := []struct {
		name     string
		path     string
		size     int64
		opts     []DownloaderOption
		want     string
		errMatch string
		tooLarge bool
	}{
		{name: "reads a file under the server directory", path: inside, opts: []DownloaderOption{WithLocalFilesDir(root)}, want: "voice bytes"},
		{name: "rejects an absolute path when local mode is off", path: inside, errMatch: "local_mode is off"},
		{name: "rejects a path outside the server directory", path: outside, opts: []DownloaderOption{WithLocalFilesDir(root)}, errMatch: "outside"},
		{name: "rejects dot-dot traversal", path: filepath.Join(root, "..", filepath.Base(outsideDir), "secret.txt"), opts: []DownloaderOption{WithLocalFilesDir(root)}, errMatch: "outside"},
		{name: "rejects a symlink escaping the directory", path: filepath.Join(tokenDir, "link.oga"), opts: []DownloaderOption{WithLocalFilesDir(root)}, errMatch: "open local bot API file"},
		{name: "enforces the byte cap on the real size", path: inside, opts: []DownloaderOption{WithLocalFilesDir(root), WithMaxDownloadBytes(4)}, tooLarge: true},
		{name: "enforces the byte cap on the declared size before reading", path: inside, size: 1 << 30, opts: []DownloaderOption{WithLocalFilesDir(root), WithMaxDownloadBytes(100 << 20)}, tooLarge: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &mockBotAPI{token: "123:abc", getFile: func(context.Context, GetFileRequest) (*File, error) {
				return &File{FileID: "f", FilePath: tt.path, FileSize: tt.size}, nil
			}}
			d, err := NewHTTPFileDownloader(api, "http://unused.invalid", "", tt.opts...)
			require.NoError(t, err)
			got, err := d.DownloadFile(context.Background(), "f")
			switch {
			case tt.tooLarge:
				require.ErrorIs(t, err, ErrFileDownloadTooLarge)
			case tt.errMatch != "":
				require.Error(t, err)
				assert.True(t, strings.Contains(err.Error(), tt.errMatch), "error %q should mention %q", err, tt.errMatch)
			default:
				require.NoError(t, err)
				assert.Equal(t, tt.want, string(got))
			}
		})
	}
}

func TestDownloadFileWithLimit_RaisedCap(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "big.bin")
	data := make([]byte, 30<<20) // above the public 20 MB limit
	require.NoError(t, os.WriteFile(path, data, 0o600))
	api := &mockBotAPI{token: "t", getFile: func(context.Context, GetFileRequest) (*File, error) {
		return &File{FilePath: path, FileSize: int64(len(data))}, nil
	}}

	capped, err := NewHTTPFileDownloader(api, "http://unused.invalid", "", WithLocalFilesDir(root))
	require.NoError(t, err)
	_, err = capped.DownloadFileWithLimit(context.Background(), "f", 1<<40)
	require.ErrorIs(t, err, ErrFileDownloadTooLarge, "the default cap still bounds a caller asking for more")

	raised, err := NewHTTPFileDownloader(api, "http://unused.invalid", "", WithLocalFilesDir(root), WithMaxDownloadBytes(100<<20))
	require.NoError(t, err)
	got, err := raised.DownloadFileWithLimit(context.Background(), "f", 1<<40)
	require.NoError(t, err)
	assert.Len(t, got, len(data))
}

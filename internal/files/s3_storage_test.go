package files

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// NewS3Storage validates the required connection fields without touching the
// network. The live PutObject/GetObject/DeleteObject round-trip is covered by
// the deployment smoke test against a real bucket.
func TestNewS3Storage_Validation(t *testing.T) {
	tests := []struct {
		name    string
		opts    S3Options
		wantErr string
	}{
		{
			name:    "missing bucket",
			opts:    S3Options{Endpoint: "https://storage.yandexcloud.net", Region: "ru-central1"},
			wantErr: "bucket is required",
		},
		{
			name:    "missing endpoint",
			opts:    S3Options{Bucket: "laplaced-dev", Region: "ru-central1"},
			wantErr: "endpoint is required",
		},
		{
			name: "valid",
			opts: S3Options{
				Endpoint:  "https://storage.yandexcloud.net",
				Region:    "ru-central1",
				Bucket:    "laplaced-dev",
				AccessKey: "ak",
				SecretKey: "sk",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewS3Storage(context.Background(), tt.opts, testLogger())
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, s)
			assert.Equal(t, tt.opts.Bucket, s.bucket)
		})
	}
}

// Both backends must satisfy the Storage interface (also asserted at compile
// time via the package-level var _ Storage assertions).
func TestStorageInterfaceSatisfied(t *testing.T) {
	var _ Storage = (*FileStorage)(nil)
	var _ Storage = (*S3Storage)(nil)
}

// Requests must use path-style addressing: the bucket goes into the URL path,
// not into the host name. Self-hosted S3 gateways have no per-bucket DNS names,
// so a virtual-hosted request would fail to resolve before reaching the server.
func TestS3Storage_PathStyleAddressing(t *testing.T) {
	var (
		mu    sync.Mutex
		hosts []string
		paths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.Host)
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, "payload")
		}
	}))
	defer srv.Close()

	// A host name, not an IP: for IP endpoints the SDK falls back to path-style
	// on its own, which would let this test pass without the fix.
	endpoint := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	s, err := NewS3Storage(context.Background(), S3Options{
		Endpoint:  endpoint,
		Region:    "ru-central1",
		Bucket:    "laplaced-dev",
		AccessKey: "ak",
		SecretKey: "sk",
	}, testLogger())
	require.NoError(t, err)

	ctx := context.Background()
	saved, err := s.SaveFile(ctx, "42", strings.NewReader("payload"), "note.txt")
	require.NoError(t, err)
	data, err := s.ReadFile(ctx, saved.Path)
	require.NoError(t, err)
	assert.Equal(t, "payload", string(data))

	assert.True(t, s.client.Options().UsePathStyle)

	u, err := url.Parse(endpoint)
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, paths, 2)
	for i := range paths {
		assert.Equal(t, u.Host, hosts[i], "bucket must not be prepended to the host")
		assert.Equal(t, "/laplaced-dev/"+saved.Path, paths[i])
	}
}

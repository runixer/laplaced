package files

import (
	"context"
	"io"
	"log/slog"
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

package fetch

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestNew_BackendSelection(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.FetcherConfig
		wantErr  string
		wantType any
	}{
		{
			name:     "firecrawl with key",
			cfg:      config.FetcherConfig{Backend: "firecrawl", Firecrawl: config.FirecrawlConfig{APIKey: "fc-test"}},
			wantType: &firecrawlFetcher{},
		},
		{
			name:    "firecrawl without key",
			cfg:     config.FetcherConfig{Backend: "firecrawl"},
			wantErr: "requires firecrawl.api_key",
		},
		{
			name:     "raw",
			cfg:      config.FetcherConfig{Backend: "raw"},
			wantType: &rawFetcher{},
		},
		{
			name:    "unknown backend",
			cfg:     config.FetcherConfig{Backend: "mcp"},
			wantErr: "unknown fetcher backend",
		},
		{
			name:    "empty backend",
			cfg:     config.FetcherConfig{},
			wantErr: "unknown fetcher backend",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := New(&tt.cfg, testutil.TestLogger())
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
			assert.IsType(t, tt.wantType, f)
		})
	}
}

func TestError_Error(t *testing.T) {
	withStatus := &Error{Kind: KindBlocked, StatusCode: 403, Msg: "captcha"}
	assert.Equal(t, "fetch failed (blocked, HTTP 403): captcha", withStatus.Error())

	noStatus := &Error{Kind: KindTimeout, Msg: "deadline"}
	assert.Equal(t, "fetch failed (timeout): deadline", noStatus.Error())
}

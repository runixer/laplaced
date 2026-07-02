package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/testutil"
)

// newFirecrawlTest builds a firecrawl fetcher pointed at a test server.
func newFirecrawlTest(t *testing.T, handler http.HandlerFunc) (*firecrawlFetcher, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := config.FetcherConfig{
		Backend:   "firecrawl",
		Firecrawl: config.FirecrawlConfig{BaseURL: srv.URL, APIKey: "fc-test"},
	}
	return newFirecrawlFetcher(&cfg, testutil.TestLogger()), srv
}

func fcSuccessBody(markdown, title, finalURL string, statusCode int) string {
	body := map[string]any{
		"success": true,
		"data": map[string]any{
			"markdown": markdown,
			"metadata": map[string]any{
				"title":      title,
				"url":        finalURL,
				"statusCode": statusCode,
			},
		},
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func TestFirecrawlFetch_Success(t *testing.T) {
	var gotAuth, gotBody string
	f, _ := newFirecrawlTest(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotBody, _ = req["url"].(string)
		assert.Equal(t, "/v2/scrape", r.URL.Path)
		fmt.Fprint(w, fcSuccessBody("# Docs\n\ncontent", "API Docs", "https://docs.example.com/final", 200))
	})

	res, err := f.Fetch(context.Background(), "https://docs.example.com/page")
	require.NoError(t, err)
	assert.Equal(t, "Bearer fc-test", gotAuth)
	assert.Equal(t, "https://docs.example.com/page", gotBody)
	assert.Equal(t, "# Docs\n\ncontent", res.Content)
	assert.Equal(t, "API Docs", res.Title)
	assert.Equal(t, "https://docs.example.com/final", res.FinalURL)
	assert.Equal(t, 200, res.StatusCode)
}

func TestFirecrawlFetch_FinalURLFallsBackToRequested(t *testing.T) {
	f, _ := newFirecrawlTest(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, fcSuccessBody("content", "", "", 200))
	})

	res, err := f.Fetch(context.Background(), "https://example.com/x")
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/x", res.FinalURL)
}

func TestFirecrawlFetch_ServiceStatusMapping(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantKind ErrorKind
	}{
		{"400 invalid url", http.StatusBadRequest, KindInvalidURL},
		{"401 bad key", http.StatusUnauthorized, KindConfig},
		{"402 out of credits", http.StatusPaymentRequired, KindQuota},
		{"403 policy refusal", http.StatusForbidden, KindRefused},
		{"429 rate limited", http.StatusTooManyRequests, KindRateLimited},
		{"500 upstream", http.StatusInternalServerError, KindUpstream},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _ := newFirecrawlTest(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprintf(w, `{"success":false,"error":"service says no"}`)
			})

			_, err := f.Fetch(context.Background(), "https://example.com")
			var fErr *Error
			require.ErrorAs(t, err, &fErr)
			assert.Equal(t, tt.wantKind, fErr.Kind)
			assert.Equal(t, tt.status, fErr.StatusCode)
			assert.Equal(t, "service says no", fErr.Msg)
		})
	}
}

func TestFirecrawlFetch_TargetPageFailures(t *testing.T) {
	tests := []struct {
		name         string
		targetStatus int
		wantKind     ErrorKind
	}{
		{"target 404", 404, KindNotFound},
		{"target 410", 410, KindNotFound},
		{"target 403 captcha", 403, KindBlocked},
		{"target 999 anti-bot", 999, KindBlocked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _ := newFirecrawlTest(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, fcSuccessBody("", "", "https://final.example.com/resolved", tt.targetStatus))
			})

			_, err := f.Fetch(context.Background(), "https://short.link/abc")
			var fErr *Error
			require.ErrorAs(t, err, &fErr)
			assert.Equal(t, tt.wantKind, fErr.Kind)
			assert.Equal(t, tt.targetStatus, fErr.StatusCode)
			// The resolved URL survives failure — "this shortlink points to X".
			assert.Equal(t, "https://final.example.com/resolved", fErr.FinalURL)
		})
	}
}

func TestFirecrawlFetch_TargetErrorStatusWithContent_IsSuccess(t *testing.T) {
	// A page can return 404 yet carry useful content (soft-404 docs). Only an
	// empty scrape counts as failure.
	f, _ := newFirecrawlTest(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, fcSuccessBody("actual content", "T", "https://e.com", 404))
	})

	res, err := f.Fetch(context.Background(), "https://e.com")
	require.NoError(t, err)
	assert.Equal(t, "actual content", res.Content)
}

func TestFirecrawlFetch_SuccessFalse(t *testing.T) {
	f, _ := newFirecrawlTest(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":false,"error":"scrape engine exploded"}`)
	})

	_, err := f.Fetch(context.Background(), "https://example.com")
	var fErr *Error
	require.ErrorAs(t, err, &fErr)
	assert.Equal(t, KindUpstream, fErr.Kind)
	assert.Equal(t, "scrape engine exploded", fErr.Msg)
}

func TestFirecrawlFetch_MalformedJSON(t *testing.T) {
	f, _ := newFirecrawlTest(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `not json at all`)
	})

	_, err := f.Fetch(context.Background(), "https://example.com")
	var fErr *Error
	require.ErrorAs(t, err, &fErr)
	assert.Equal(t, KindUpstream, fErr.Kind)
	assert.Contains(t, fErr.Msg, "malformed scrape response")
}

func TestFirecrawlFetch_Timeout(t *testing.T) {
	f, _ := newFirecrawlTest(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		fmt.Fprint(w, fcSuccessBody("late", "", "", 200))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := f.Fetch(ctx, "https://example.com")
	var fErr *Error
	require.ErrorAs(t, err, &fErr)
	assert.Equal(t, KindTimeout, fErr.Kind)
}

func TestClassifyTransportError_NonTimeout(t *testing.T) {
	fErr := classifyTransportError(errors.New("connection refused"))
	assert.Equal(t, KindUpstream, fErr.Kind)
}

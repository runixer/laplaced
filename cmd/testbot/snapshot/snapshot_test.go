package snapshot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractServiceVersion(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{
			name: "present",
			json: `{"batches":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"laplaced"}},{"key":"service.version","value":{"stringValue":"feat/otel-migration-2aa4245"}}]}}]}`,
			want: "feat/otel-migration-2aa4245",
		},
		{
			name: "absent",
			json: `{"batches":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"laplaced"}}]}}]}`,
			want: "",
		},
		{
			name: "malformed JSON returns empty, not panic",
			json: `not json`,
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, ExtractServiceVersion([]byte(c.json)))
		})
	}
}

func TestManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := &Manifest{
		Version:     ManifestVersion,
		Agent:       "reranker",
		SpanName:    "reranker.Execute",
		TempoURL:    "https://tempo.example",
		Filter:      `{ name = "reranker.Execute" }`,
		WindowStart: time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC),
		WindowEnd:   time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
		CapturedAt:  time.Date(2026, 4, 25, 12, 0, 5, 0, time.UTC),
		TraceCount:  1,
		DBPath:      "laplaced.db",
		Traces: []TraceManifestEntry{
			{TraceID: "abc", File: "traces/abc.json", DurationMs: 12345, StartTimeUnix: 1777148712, ServiceVersion: "v1"},
		},
	}

	path := filepath.Join(dir, "manifest.yaml")
	require.NoError(t, WriteManifest(path, want))

	got, err := ReadManifest(path)
	require.NoError(t, err)
	assert.Equal(t, want.Agent, got.Agent)
	assert.Equal(t, want.Filter, got.Filter)
	assert.Equal(t, want.WindowStart.UTC(), got.WindowStart.UTC())
	assert.Equal(t, want.TraceCount, got.TraceCount)
	require.Len(t, got.Traces, 1)
	assert.Equal(t, want.Traces[0], got.Traces[0])
}

func TestTempoClient_Search(t *testing.T) {
	var capturedURL *url.URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"traces":[{"traceID":"abc","durationMs":1234,"startTimeUnixNano":"1777148712000000000"},{"traceID":"def","durationMs":5678}]}`))
	}))
	defer srv.Close()

	c := NewTempoClient(srv.URL)
	traces, err := c.Search(context.Background(), `{ name = "reranker.Execute" }`, 1000, 2000, 50)
	require.NoError(t, err)
	require.Len(t, traces, 2)
	assert.Equal(t, "abc", traces[0].TraceID)
	assert.Equal(t, int64(1234), traces[0].DurationMs)
	assert.Equal(t, int64(1777148712), traces[0].StartTimeUnix())
	assert.Equal(t, int64(0), traces[1].StartTimeUnix(), "missing nano string maps to 0")

	require.NotNil(t, capturedURL)
	assert.Equal(t, "/api/search", capturedURL.Path)
	q := capturedURL.Query()
	assert.Equal(t, `{ name = "reranker.Execute" }`, q.Get("q"))
	assert.Equal(t, "1000", q.Get("start"))
	assert.Equal(t, "2000", q.Get("end"))
	assert.Equal(t, "50", q.Get("limit"))
}

func TestTempoClient_Search_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("range exceeds 168h"))
	}))
	defer srv.Close()

	c := NewTempoClient(srv.URL)
	_, err := c.Search(context.Background(), `{}`, 0, 1, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "168h")
}

func TestTempoClient_FetchTrace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/traces/abc123", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"batches":[]}`))
	}))
	defer srv.Close()

	c := NewTempoClient(srv.URL)
	body, err := c.FetchTrace(context.Background(), "abc123")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(body), `{"batches"`))
}

func TestWriteTrace_AndCopyDB(t *testing.T) {
	dir := t.TempDir()
	rel, err := WriteTrace(dir, "tid_1", []byte(`{"a":1}`))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("traces", "tid_1.json"), rel)
	body, err := os.ReadFile(filepath.Join(dir, rel))
	require.NoError(t, err)
	assert.Equal(t, `{"a":1}`, string(body))

	srcPath := filepath.Join(dir, "src.db")
	require.NoError(t, os.WriteFile(srcPath, []byte("sqlite-bytes"), 0o644))
	dbRel, err := CopyDB(dir, srcPath)
	require.NoError(t, err)
	assert.Equal(t, "laplaced.db", dbRel)
	copied, err := os.ReadFile(filepath.Join(dir, dbRel))
	require.NoError(t, err)
	assert.Equal(t, "sqlite-bytes", string(copied))
}

func TestCopyDB_EmptySource_NoOp(t *testing.T) {
	dir := t.TempDir()
	rel, err := CopyDB(dir, "")
	require.NoError(t, err)
	assert.Equal(t, "", rel)
	_, statErr := os.Stat(filepath.Join(dir, "laplaced.db"))
	assert.True(t, os.IsNotExist(statErr), "no DB file when source is empty")
}

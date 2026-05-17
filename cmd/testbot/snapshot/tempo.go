// Package snapshot captures Tempo traces and a database snapshot to disk
// for later offline replay and evaluation. The package is agent-agnostic:
// callers pick the TraceQL filter, the package mechanically fetches traces
// and writes them out. Per-agent extraction (which event holds the prompt,
// which attribute holds the cost) is left to consumers.
package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TempoClient is a thin HTTP client over Tempo's search and trace APIs.
// No auth — this is dev tooling against a LAN-only Tempo (Traefik whitelist).
type TempoClient struct {
	BaseURL string
	HTTP    *http.Client
}

// NewTempoClient constructs a client with sensible defaults.
func NewTempoClient(baseURL string) *TempoClient {
	return &TempoClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// TraceMeta is one entry from /api/search.
type TraceMeta struct {
	TraceID           string `json:"traceID"`
	RootServiceName   string `json:"rootServiceName"`
	RootTraceName     string `json:"rootTraceName"`
	StartTimeUnixNano string `json:"startTimeUnixNano"`
	DurationMs        int64  `json:"durationMs"`
}

// StartTimeUnix converts the nano-precision string to unix seconds.
// Tempo returns startTimeUnixNano as a string because it overflows int64
// in some JSON parsers; we only need second precision for manifest.
func (m TraceMeta) StartTimeUnix() int64 {
	if m.StartTimeUnixNano == "" {
		return 0
	}
	n, err := strconv.ParseInt(m.StartTimeUnixNano, 10, 64)
	if err != nil {
		return 0
	}
	return n / 1_000_000_000
}

type searchResponse struct {
	Traces []TraceMeta `json:"traces"`
}

// Search runs a TraceQL query over [start, end] and returns up to limit traces.
// start and end are unix seconds. Tempo enforces a 168h max window.
func (c *TempoClient) Search(ctx context.Context, query string, start, end int64, limit int) ([]TraceMeta, error) {
	u, err := url.Parse(c.BaseURL + "/api/search")
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("start", strconv.FormatInt(start, 10))
	q.Set("end", strconv.FormatInt(end, 10))
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build search request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tempo search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read search body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tempo search %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var sr searchResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, fmt.Errorf("decode search response: %w (body: %s)", err, truncate(string(body), 200))
	}
	return sr.Traces, nil
}

// FetchTrace returns the full trace JSON as raw bytes. We persist it as-is
// so consumers can use any JSON tooling without re-marshalling drift.
func (c *TempoClient) FetchTrace(ctx context.Context, traceID string) ([]byte, error) {
	u := c.BaseURL + "/api/traces/" + url.PathEscape(traceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build trace request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tempo fetch %s: %w", traceID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read trace body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tempo fetch %s: %d %s", traceID, resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

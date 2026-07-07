package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/textutil"
)

// firecrawlFetcher scrapes pages via the Firecrawl REST API
// (POST {base}/v2/scrape). Firecrawl renders JavaScript, so it reads
// doc sites and SPAs a plain HTTP GET cannot. 1 credit per page.
type firecrawlFetcher struct {
	client  *http.Client
	baseURL string
	apiKey  string
	logger  *slog.Logger
}

func newFirecrawlFetcher(cfg *config.FetcherConfig, logger *slog.Logger) *firecrawlFetcher {
	return &firecrawlFetcher{
		client:  &http.Client{Timeout: cfg.GetTimeout()},
		baseURL: cfg.Firecrawl.GetBaseURL(),
		apiKey:  cfg.Firecrawl.APIKey,
		logger:  logger,
	}
}

// fcResponse mirrors the /v2/scrape response shape (fields we use).
type fcResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Data    struct {
		Markdown string `json:"markdown"`
		Metadata struct {
			Title      string `json:"title"`
			SourceURL  string `json:"sourceURL"`
			URL        string `json:"url"` // final URL after redirects
			StatusCode int    `json:"statusCode"`
			Error      string `json:"error"`
		} `json:"metadata"`
	} `json:"data"`
}

func (f *firecrawlFetcher) Fetch(ctx context.Context, targetURL string) (*Result, error) {
	body, err := json.Marshal(map[string]any{
		"url":     targetURL,
		"formats": []string{"markdown"},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal scrape request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.baseURL+"/v2/scrape", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to build scrape request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.apiKey)

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, classifyTransportError(err)
	}

	var parsed fcResponse
	parseErr := json.Unmarshal(respBody, &parsed)

	if resp.StatusCode != http.StatusOK {
		msg := parsed.Error
		if msg == "" {
			// Rune-safe: the error body may be non-ASCII, and this message
			// reaches the model via the tool result.
			msg = textutil.TruncateRunes(string(respBody), 200, "...")
		}
		return nil, &Error{Kind: classifyServiceStatus(resp.StatusCode), StatusCode: resp.StatusCode, Msg: msg}
	}
	if parseErr != nil {
		return nil, &Error{Kind: KindUpstream, Msg: fmt.Sprintf("malformed scrape response: %v", parseErr)}
	}
	if !parsed.Success {
		return nil, &Error{Kind: KindUpstream, Msg: parsed.Error}
	}

	meta := parsed.Data.Metadata
	finalURL := meta.URL
	if finalURL == "" {
		finalURL = targetURL
	}

	// success:true with a failing target status and no content: the scrape
	// worked but the page itself said no. 404/410 → dead link; other 4xx →
	// walled off (captcha/anti-bot); 5xx → the origin is having an outage,
	// which is transient — classifying it as "blocked" would make the bot
	// tell the user the site bans bots and forbid the one retry that would
	// likely succeed.
	if meta.StatusCode >= 400 && parsed.Data.Markdown == "" {
		kind := KindBlocked
		switch {
		case meta.StatusCode == http.StatusNotFound || meta.StatusCode == http.StatusGone:
			kind = KindNotFound
		case meta.StatusCode >= 500 && meta.StatusCode < 600:
			// Strictly 5xx: out-of-range statuses like 999 are anti-bot
			// walls (LinkedIn), not outages.
			kind = KindUpstream
		}
		msg := meta.Error
		if msg == "" {
			msg = fmt.Sprintf("target page returned HTTP %d", meta.StatusCode)
		}
		return nil, &Error{Kind: kind, StatusCode: meta.StatusCode, FinalURL: finalURL, Msg: msg}
	}

	return &Result{
		Content:    parsed.Data.Markdown,
		Title:      meta.Title,
		FinalURL:   finalURL,
		StatusCode: meta.StatusCode,
	}, nil
}

// classifyServiceStatus maps Firecrawl API (not target page) HTTP statuses.
func classifyServiceStatus(status int) ErrorKind {
	switch status {
	case http.StatusBadRequest:
		return KindInvalidURL
	case http.StatusUnauthorized:
		return KindConfig
	case http.StatusPaymentRequired:
		return KindQuota
	case http.StatusForbidden:
		// Firecrawl refuses whole sites by policy with 403 (e.g. Reddit).
		return KindRefused
	case http.StatusTooManyRequests:
		return KindRateLimited
	default:
		return KindUpstream
	}
}

// classifyTransportError distinguishes deadline expiry from other network
// failures so the tool layer can say "the page took too long" honestly.
func classifyTransportError(err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: KindTimeout, Msg: err.Error()}
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Error{Kind: KindTimeout, Msg: err.Error()}
	}
	return &Error{Kind: KindUpstream, Msg: err.Error()}
}

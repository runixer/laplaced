// Package fetch retrieves web pages for the read_url tool. It exposes a
// single Fetcher interface with backends selected by config: "firecrawl"
// (hosted scrape API with JS rendering) and "raw" (plain HTTP + text
// extraction). Failures are classified into ErrorKind so the tool layer can
// give the model an honest, actionable message instead of a generic error.
package fetch

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/runixer/laplaced/internal/config"
)

// Result is a successfully fetched page. Content is the full extracted
// markdown/plain text — truncation is a presentation concern of the tool
// layer, not the fetcher.
type Result struct {
	Content    string
	Title      string // "" when unknown
	FinalURL   string // after redirects; equals the requested URL if none
	StatusCode int    // target page HTTP status when known, 0 otherwise
}

// ErrorKind classifies fetch failures. The tool layer maps each kind to a
// model-facing message that steers away from retry/search spirals.
type ErrorKind string

const (
	KindInvalidURL  ErrorKind = "invalid_url"  // malformed or non-http(s) URL
	KindRefused     ErrorKind = "refused"      // service refuses the site by policy (e.g. Reddit)
	KindBlocked     ErrorKind = "blocked"      // captcha / anti-bot wall (e.g. marketplaces)
	KindNotFound    ErrorKind = "not_found"    // target 404/410
	KindTimeout     ErrorKind = "timeout"      // deadline exceeded
	KindRateLimited ErrorKind = "rate_limited" // 429 from the service
	KindQuota       ErrorKind = "quota"        // fetch service out of credits
	KindConfig      ErrorKind = "config"       // bad/missing API key, unwired backend
	KindUpstream    ErrorKind = "upstream"     // 5xx, malformed response, network error
)

// Error is a classified fetch failure. FinalURL carries the best-known
// resolved URL even on failure: a shortlink redirect often resolves before
// the target page turns out to be bot-protected, and "this link points to X"
// is still a useful answer.
type Error struct {
	Kind       ErrorKind
	StatusCode int    // HTTP status that triggered the failure (service or target), 0 if none
	FinalURL   string // best-known final URL, "" if unknown
	Msg        string
}

func (e *Error) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("fetch failed (%s, HTTP %d): %s", e.Kind, e.StatusCode, e.Msg)
	}
	return fmt.Sprintf("fetch failed (%s): %s", e.Kind, e.Msg)
}

// Fetcher retrieves a single URL. Implementations classify failures as *Error
// where possible; any other error means a programmer/transport fault.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (*Result, error)
}

// New builds the Fetcher selected by cfg.Backend. It fails on an unknown
// backend or a firecrawl backend without an API key, so the caller can leave
// the read_url tool unwired (and log why) instead of failing at first use.
func New(cfg *config.FetcherConfig, logger *slog.Logger) (Fetcher, error) {
	switch cfg.Backend {
	case "firecrawl":
		if cfg.Firecrawl.APIKey == "" {
			return nil, fmt.Errorf("fetcher backend %q requires firecrawl.api_key (env LAPLACED_FIRECRAWL_API_KEY)", cfg.Backend)
		}
		return newFirecrawlFetcher(cfg, logger), nil
	case "raw":
		return newRawFetcher(cfg, logger), nil
	default:
		return nil, fmt.Errorf("unknown fetcher backend %q (want firecrawl or raw)", cfg.Backend)
	}
}

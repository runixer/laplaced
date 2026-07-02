package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/fetch"
	"github.com/runixer/laplaced/internal/llm"
)

// performReadURL executes the read_url tool: fetch one page via the configured
// fetch backend and hand its content to the LLM.
//
// Failures come back as non-error *Result content, mirroring generate_image:
// a Go error would surface as a bare "Tool execution failed: …", which the
// model reads as "try something else" and falls into exactly the search
// spiral this tool exists to prevent. Each failure kind gets an explicit
// instruction on what to do instead. Go errors remain only for programmer
// faults (missing url argument).
func (e *ToolExecutor) performReadURL(ctx context.Context, cc CallContext, args map[string]interface{}) (*Result, error) {
	if e.fetcher == nil {
		return &Result{Content: "READ FAILED: page reading is not configured on this bot. " +
			"Do NOT retry and do NOT hunt for the page content via internet_search — " +
			"answer from what you have and tell the user you cannot open links right now."}, nil
	}

	rawURL, _ := args["url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("read_url: url argument is required")
	}

	backend := e.cfg.Fetcher.Backend
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("fetch.backend", backend))

	startTime := time.Now()
	res, err := e.fetcher.Fetch(ctx, rawURL)
	duration := time.Since(startTime)

	if err != nil {
		content, fErr := readFailureContent(rawURL, err)
		if fErr != nil {
			span.SetAttributes(attribute.String("fetch.error_kind", string(fErr.Kind)))
		}
		e.logReadURL(ctx, cc, rawURL, backend, content, duration, agentlogFailureMeta(backend, fErr), err)

		// Even on failure the resolved URL is citable — "this shortlink
		// points to X" must survive the citation guard.
		var citations []llm.Citation
		if fErr != nil && fErr.FinalURL != "" && fErr.FinalURL != rawURL {
			citations = append(citations, llm.Citation{URL: fErr.FinalURL})
		}
		return &Result{Content: content, Citations: citations}, nil
	}

	content, truncated := truncateRunes(res.Content, e.cfg.Fetcher.GetMaxContentChars())

	var b strings.Builder
	if res.Title != "" {
		fmt.Fprintf(&b, "# %s\n", res.Title)
	}
	fmt.Fprintf(&b, "**Source:** %s\n\n---\n\n", res.FinalURL)
	b.WriteString(content)
	fmt.Fprintf(&b, "\n\n<source note=\"This is the page you read. When linking it, use ONLY this URL. Never invent or alter a URL.\">%s</source>", res.FinalURL)
	toolContent := b.String()

	// Register both URL forms with the citation guard: the user pasted one,
	// the page may canonically be the other, and the model may link either.
	citations := []llm.Citation{{URL: rawURL, Title: res.Title}}
	if res.FinalURL != rawURL {
		citations = append(citations, llm.Citation{URL: res.FinalURL, Title: res.Title})
	}
	span.SetAttributes(attribute.Int("tool.citations_count", len(citations)))

	e.logReadURL(ctx, cc, rawURL, backend, toolContent, duration, map[string]any{
		"backend":       backend,
		"final_url":     res.FinalURL,
		"http_status":   res.StatusCode,
		"content_chars": len([]rune(res.Content)),
		"truncated":     truncated,
	}, nil)

	return &Result{Content: toolContent, Citations: citations}, nil
}

// truncateRunes caps s at max runes, appending a model-facing marker when cut.
// Rune (not byte) boundaries: page content is often Russian, and a split
// multibyte rune corrupts the tail.
func truncateRunes(s string, max int) (string, bool) {
	runes := []rune(s)
	if len(runes) <= max {
		return s, false
	}
	return string(runes[:max]) + fmt.Sprintf("\n\n[... truncated: page content exceeds %d characters]", max), true
}

// readFailureContent maps a classified fetch error to a model-facing message
// that steers the model to an honest answer instead of a retry/search spiral.
func readFailureContent(rawURL string, err error) (string, *fetch.Error) {
	var fErr *fetch.Error
	if !errors.As(err, &fErr) {
		return fmt.Sprintf("READ FAILED: could not fetch %s (%v). Do NOT retry and do NOT search for the same content — tell the user honestly you could not open the page.", rawURL, err), nil
	}

	redirectNote := ""
	if fErr.FinalURL != "" && fErr.FinalURL != rawURL {
		redirectNote = fmt.Sprintf(" The link resolves to %s — you may tell the user what it points to.", fErr.FinalURL)
	}

	switch fErr.Kind {
	case fetch.KindInvalidURL:
		return fmt.Sprintf("READ FAILED: %q is not a valid http(s) URL. If the user's message contains the full correct URL, retry once with it verbatim; otherwise ask the user for the link.", rawURL), fErr
	case fetch.KindRefused:
		return fmt.Sprintf("READ FAILED: the fetch service refuses to open %s by site policy (common for Reddit and similar sites). Do NOT retry and do NOT hunt for the same page via internet_search — tell the user honestly that you cannot open this site.%s", rawURL, redirectNote), fErr
	case fetch.KindBlocked:
		return fmt.Sprintf("READ FAILED: the page at %s is protected by captcha/anti-bot (common for marketplaces). Do NOT retry and do NOT chase the content via internet_search — tell the user honestly.%s", rawURL, redirectNote), fErr
	case fetch.KindNotFound:
		return fmt.Sprintf("READ FAILED: the page at %s returned HTTP %d — the link is dead or mistyped. Say so honestly; retry only if you have a corrected URL.%s", rawURL, fErr.StatusCode, redirectNote), fErr
	case fetch.KindTimeout:
		return fmt.Sprintf("READ FAILED: %s took too long to load. Do NOT retry now — tell the user the site did not respond and answer from what you have.", rawURL), fErr
	case fetch.KindRateLimited:
		return "READ FAILED: the page-reading service is rate-limited right now. Do NOT retry in this reply — answer from what you have.", fErr
	case fetch.KindQuota:
		return "READ FAILED: the page-reading service is out of quota. Do NOT retry and do NOT substitute internet_search for reading this exact page — answer from what you have and mention you could not open the link.", fErr
	case fetch.KindConfig:
		return "READ FAILED: the page-reading backend is misconfigured (authentication error). Do NOT retry — answer from what you have and tell the user you cannot open links right now.", fErr
	default: // KindUpstream and anything new
		return fmt.Sprintf("READ FAILED: could not fetch %s (%s). You may retry ONCE if a transient error seems likely; if it fails again, tell the user honestly.%s", rawURL, fErr.Msg, redirectNote), fErr
	}
}

// agentlogFailureMeta builds the metadata map for a failed fetch.
func agentlogFailureMeta(backend string, fErr *fetch.Error) map[string]any {
	meta := map[string]any{"backend": backend}
	if fErr != nil {
		meta["error_kind"] = string(fErr.Kind)
		if fErr.FinalURL != "" {
			meta["final_url"] = fErr.FinalURL
		}
		if fErr.StatusCode != 0 {
			meta["http_status"] = fErr.StatusCode
		}
	}
	return meta
}

// logReadURL records the fetch in agent_logs (agent type "fetcher") — this
// powers the /ui/agents/fetcher debug page and the eval runner's read count.
// No token/cost fields: the fetch is not an LLM call.
func (e *ToolExecutor) logReadURL(ctx context.Context, cc CallContext, rawURL, backend, output string, duration time.Duration, meta map[string]any, err error) {
	if e.agentLogger == nil {
		return
	}
	entry := agentlog.Entry{
		UserID:         cc.UserID,
		AgentType:      agentlog.AgentFetcher,
		InputPrompt:    rawURL,
		OutputResponse: output,
		Model:          backend,
		DurationMs:     int(duration.Milliseconds()),
		Metadata:       meta,
		Success:        err == nil,
	}
	if err != nil {
		entry.ErrorMessage = err.Error()
	}
	e.agentLogger.Log(ctx, entry)
}

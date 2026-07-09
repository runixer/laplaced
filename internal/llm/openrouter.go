package llm

// OpenRouter protocol extensions.
//
// Everything in this file goes beyond the plain OpenAI-compatible API surface:
// request fields only OpenRouter understands (plugins, provider routing,
// Broadcast trace metadata) and response shapes only OpenRouter produces
// (error envelopes inside HTTP 200 bodies, error.metadata blocks). Other
// OpenAI-compatible backends (litellm, vLLM) ignore the request extensions and
// never produce the response shapes, so all of this is a harmless no-op
// outside OpenRouter.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/obs"
)

// PDFConfig selects OpenRouter's PDF parsing engine for the pdf plugin.
type PDFConfig struct {
	Engine string `json:"engine"`
}

// Plugin is an OpenRouter request plugin (e.g. "response-healing", "pdf").
type Plugin struct {
	ID  string    `json:"id"`
	PDF PDFConfig `json:"pdf,omitempty"`
}

// ProviderRouting controls OpenRouter's provider selection for a request.
// Order lists preferred providers (tried in sequence). AllowFallbacks is a
// pointer so callers can distinguish "unset" (default true on OpenRouter's
// side) from an explicit false (strict routing — fail instead of falling
// back to providers outside the order list).
type ProviderRouting struct {
	Order          []string `json:"order,omitempty"`
	AllowFallbacks *bool    `json:"allow_fallbacks,omitempty"`
}

// providerBodyError represents an error reported inside an HTTP 200 response
// body. OpenRouter returns 200 with {"error":{"message":...,"code":...}} when
// the upstream provider is unavailable; this must be treated as retryable,
// not as a silent success.
type providerBodyError struct {
	Message      string
	Code         int
	ProviderName string // from error.metadata.provider_name when OR proxies an upstream error
	Raw          string // upstream provider's raw error body, mirrored at error.metadata.raw

	// MetadataPresent distinguishes "OR sent an error.metadata{} block (even
	// if provider_name=null) → OR-gateway-level rejection on our API key"
	// from "no metadata at all → upstream provider rejected and OR just wrapped
	// the response body in HTTP 200" (the Vertex gemini-embedding-2 quota case
	// looks like the latter). The two need different ops responses, so the
	// distinction has to survive the parse boundary.
	MetadataPresent bool

	// Rate-limit signals from error.metadata.headers, parsed from string to
	// int64 once at the boundary. Zero means "absent" — for 429s, treat zero
	// as "no reset hint" rather than "now". OpenRouter populates X-RateLimit-*
	// on its own gateway throttles; some upstream providers populate Retry-After.
	RateLimitResetMs   int64
	RateLimitRemaining int64
	RetryAfterSeconds  int64
}

func (e *providerBodyError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("provider body error: %s (code %d)", e.Message, e.Code)
	}
	return fmt.Sprintf("provider body error: %s", e.Message)
}

// orErrorEnvelope is the wire shape of an OpenRouter error block. It appears
// at the body top level (provider unavailable) or inside a choice — OpenRouter
// injects mid-generation provider failures as choices[i].error ("JSON error
// injected into SSE stream") while the message content stays truncated.
type orErrorEnvelope struct {
	Message  string          `json:"message"`
	Code     json.RawMessage `json:"code"`
	Metadata *struct {
		Raw          string            `json:"raw"`
		ProviderName string            `json:"provider_name"`
		Headers      map[string]string `json:"headers"`
	} `json:"metadata"`
}

// toBodyError converts a parsed envelope into a providerBodyError.
func (env *orErrorEnvelope) toBodyError() *providerBodyError {
	bodyErr := &providerBodyError{Message: env.Message}
	if len(env.Code) > 0 {
		// Code can be number or string — try number first, then quoted string.
		if err := json.Unmarshal(env.Code, &bodyErr.Code); err != nil {
			var s string
			if err := json.Unmarshal(env.Code, &s); err == nil {
				_, _ = fmt.Sscanf(s, "%d", &bodyErr.Code)
			}
		}
	}
	if env.Metadata != nil {
		bodyErr.MetadataPresent = true
		bodyErr.ProviderName = env.Metadata.ProviderName
		bodyErr.Raw = env.Metadata.Raw
		bodyErr.RateLimitResetMs = parseInt64Header(env.Metadata.Headers, "X-RateLimit-Reset")
		bodyErr.RateLimitRemaining = parseInt64Header(env.Metadata.Headers, "X-RateLimit-Remaining")
		bodyErr.RetryAfterSeconds = parseInt64Header(env.Metadata.Headers, "Retry-After")
	}
	return bodyErr
}

// detectProviderBodyError inspects a raw response body for an error envelope:
// the top-level "error" field, or — when that is absent — a choice-level
// "choices[i].error" (mid-generation provider failure with truncated content).
// Returns non-nil if the body reports an error despite HTTP 200 status.
func detectProviderBodyError(body []byte) *providerBodyError {
	var probe struct {
		Error   *orErrorEnvelope `json:"error"`
		Choices []struct {
			Error *orErrorEnvelope `json:"error"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil
	}
	env := probe.Error
	if env == nil {
		for _, ch := range probe.Choices {
			if ch.Error != nil {
				env = ch.Error
				break
			}
		}
	}
	if env == nil {
		return nil
	}
	return env.toBodyError()
}

// parseInt64Header pulls a string value from an OR error.metadata.headers map
// and parses it as int64. Returns 0 when the key is absent or unparseable —
// callers treat zero as "no signal" rather than a literal value.
func parseInt64Header(h map[string]string, key string) int64 {
	v, ok := h[key]
	if !ok || v == "" {
		return 0
	}
	var n int64
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return 0
	}
	return n
}

// classifyUpstreamError maps a parsed OR error envelope to a stable ErrorKind
// recorded as a span attribute that TraceQL can filter on without grepping
// free-form messages. The classification is intentionally coarse —
// fine-grained provider error shapes change between deploys, but these
// buckets stay stable enough that a query like
// {span.error.upstream_kind="invalid_argument"} still matches next year.
// New shapes get KindUnknown and surface in raw_message for triage.
func classifyUpstreamError(bodyErr *providerBodyError) ErrorKind {
	if bodyErr == nil {
		return KindNone
	}
	msg := strings.ToLower(bodyErr.Message + " " + bodyErr.Raw)
	switch {
	// Most specific match first: Gemini's thought-signature body also carries
	// `"status": "INVALID_ARGUMENT"`, so the invalid_argument case would
	// shadow it — misclassifying a transient failure as permanent (never
	// retried, and the thought_signature TraceQL kind never matching).
	case strings.Contains(msg, "corrupted thought signature"):
		return KindThoughtSignature
	case strings.Contains(msg, "invalid_argument") || strings.Contains(msg, "invalid argument"):
		return KindInvalidArgument
	case strings.Contains(msg, "safety") || strings.Contains(msg, "harm_category") ||
		strings.Contains(msg, "blocked") || strings.Contains(msg, "prohibited") ||
		strings.Contains(msg, "recitation"):
		return KindSafety
	case strings.Contains(msg, "rate_limit") || strings.Contains(msg, "rate limit") || bodyErr.Code == 429:
		return KindRateLimited
	case strings.Contains(msg, "context") && strings.Contains(msg, "length"):
		return KindContextLength
	case bodyErr.Code >= 500:
		return KindUpstream5xx
	case bodyErr.Code >= 400:
		return KindUpstream4xx
	}
	return KindUnknown
}

// isNonRetryableBodyError reports whether an HTTP-200 body error is a permanent
// provider rejection that a retry cannot fix. See ErrorKind.IsRetryable.
func isNonRetryableBodyError(bodyErr *providerBodyError) bool {
	return !classifyUpstreamError(bodyErr).IsRetryable()
}

// IsSafetyBlock reports whether err (or anything it wraps) is a provider
// content-policy rejection — Gemini PROHIBITED_CONTENT / RECITATION, an OpenAI
// content_filter, etc. — i.e. the permanent KindSafety class from
// classifyUpstreamError. Callers outside this package can't inspect the
// unexported providerBodyError directly, so this is the public seam. The bot
// uses it to show a "blocked by the safety filter" message instead of the
// generic API-error fallback, and to tag the turn's span.
func IsSafetyBlock(err error) bool {
	var bodyErr *providerBodyError
	if errors.As(err, &bodyErr) {
		return classifyUpstreamError(bodyErr) == KindSafety
	}
	return false
}

// NewSafetyBlockErrorForTest builds a synthetic provider body error so tests in
// other packages (e.g. the bot's errorReplyText) can drive the IsSafetyBlock
// branch without standing up an HTTP server. providerBodyError is intentionally
// unexported; this is the sanctioned construction seam. Not used in production.
func NewSafetyBlockErrorForTest(message string, code int) error {
	return &providerBodyError{Message: message, Code: code}
}

// recordProviderError attaches a provider error envelope to a span: the raw
// body as an llm.response content event (subject to ContentEnabled gating) and
// the parsed envelope fields as structured attributes (always set when present).
// httpStatus, when non-zero, is recorded verbatim as http.response.status_code
// for filterability and is also used to bucket non-JSON 5xx bodies into an
// edge_<status> error.upstream_kind — non-JSON 5xx bodies (e.g. plain-text
// "error code: 502") fall through every JSON-envelope classifier branch and
// would otherwise leave the span without any structured error class.
// No-op on empty body and zero status.
func recordProviderError(span trace.Span, body []byte, httpStatus int) {
	if len(body) == 0 && httpStatus == 0 {
		return
	}
	if httpStatus > 0 {
		span.SetAttributes(attribute.Int("http.response.status_code", httpStatus))
	}
	if len(body) == 0 {
		return
	}
	obs.RecordContent(span, "llm.response", obs.RedactBase64Payloads(string(body)))
	bodyErr := detectProviderBodyError(body)
	if bodyErr == nil {
		// Edge classification: a 4xx/5xx response with a non-JSON body almost
		// always means OR's edge/CDN (Cloudflare-style) killed the request
		// before OR's app server saw it. Distinguishing this from a real
		// provider error matters because retrying is much less useful here
		// (everything's broken upstream of OR's routing).
		if httpStatus >= 400 && !json.Valid(body) {
			span.SetAttributes(
				attribute.String("error.upstream_kind", fmt.Sprintf("edge_%d", httpStatus)),
				attribute.String("error.upstream_message", truncateForLog(strings.TrimSpace(string(body)), 256)),
			)
		}
		return
	}
	attrs := make([]attribute.KeyValue, 0, 8)
	if bodyErr.Message != "" {
		attrs = append(attrs, attribute.String("error.upstream_message", bodyErr.Message))
	}
	if bodyErr.Code != 0 {
		attrs = append(attrs, attribute.Int("error.upstream_code", bodyErr.Code))
	}
	// upstream_provider gets a sentinel "openrouter" when OR sent a metadata
	// block but provider_name was null/empty — that's the OR-gateway throttle
	// case (chat 429 against our API key). With this, a TraceQL query like
	// {span.error.upstream_provider="openrouter" && span.error.upstream_kind="rate_limited"}
	// isolates "OR throttled us" from "Vertex/AI Studio threw a 429 and OR
	// passed it through" (provider non-empty) without parsing message strings.
	switch {
	case bodyErr.ProviderName != "":
		attrs = append(attrs, attribute.String("error.upstream_provider", bodyErr.ProviderName))
	case bodyErr.MetadataPresent:
		attrs = append(attrs, attribute.String("error.upstream_provider", "openrouter"))
	}
	if kind := classifyUpstreamError(bodyErr); kind != KindNone {
		attrs = append(attrs, attribute.String("error.upstream_kind", kind.String()))
	}
	// Rate-limit signals correlate the 429 with the next reset window. We
	// emit Remaining alongside Reset (even when Remaining=0) because "0
	// remaining" is exactly the throttled-now signal — distinguishable in
	// TraceQL from "field absent" (no signal available).
	if bodyErr.RateLimitResetMs > 0 {
		attrs = append(attrs,
			attribute.Int64("error.rate_limit.reset_unix_ms", bodyErr.RateLimitResetMs),
			attribute.Int64("error.rate_limit.remaining", bodyErr.RateLimitRemaining),
		)
	}
	if bodyErr.RetryAfterSeconds > 0 {
		attrs = append(attrs, attribute.Int64("error.retry_after_s", bodyErr.RetryAfterSeconds))
	}
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
}

// withBroadcastFields returns trc and user enriched with OpenRouter
// Broadcast linking metadata. When ctx carries a valid span context,
// trace_id and parent_span_id are filled (callers' values win). When user
// is empty and userID is non-zero, user becomes the stringified userID.
// Safe to call when Broadcast is disabled on the OR side — OR ignores
// unknown fields. Returning the values (instead of mutating pointers)
// keeps the call-site one-liner at request assembly time.
func withBroadcastFields(ctx context.Context, trc map[string]any, user string, userID string) (map[string]any, string) {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		if trc == nil {
			trc = map[string]any{}
		}
		if _, ok := trc["trace_id"]; !ok {
			trc["trace_id"] = sc.TraceID().String()
		}
		if _, ok := trc["parent_span_id"]; !ok {
			trc["parent_span_id"] = sc.SpanID().String()
		}
	}
	if user == "" && userID != "" {
		user = userID
	}
	return trc, user
}

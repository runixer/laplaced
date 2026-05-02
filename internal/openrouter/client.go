package openrouter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/jobtype"
	"github.com/runixer/laplaced/internal/obs"
)

// Retry configuration
const (
	maxRetries   = 3
	baseDelay    = 1 * time.Second
	maxDelay     = 30 * time.Second
	jitterFactor = 0.2 // 20% jitter
)

type Client interface {
	CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (ChatCompletionResponse, error)
	CreateEmbeddings(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error)
}

// FilterReasoningForLog filters out encrypted reasoning entries from reasoning_details.
// Keeps only "reasoning.text" entries which are human-readable and useful for debugging.
// This prevents massive base64 blobs from polluting logs.
func FilterReasoningForLog(details interface{}) interface{} {
	if details == nil {
		return nil
	}

	// reasoning_details is typically []interface{} of maps
	arr, ok := details.([]interface{})
	if !ok {
		return details
	}

	var filtered []interface{}
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		// Keep only reasoning.text entries, skip reasoning.encrypted
		if t, ok := m["type"].(string); ok && t == "reasoning.text" {
			filtered = append(filtered, m)
		}
	}

	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// truncateForLog truncates a string to maxLen characters for logging.
// Adds "... (truncated)" suffix if truncation occurred.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "... (truncated)"
}

type clientImpl struct {
	httpClient      *http.Client
	apiKey          string
	apiEndpoint     string
	logger          *slog.Logger
	defaultProvider *ProviderRouting
}

// openRouterBodyError represents an error reported inside an HTTP 200 response
// body. OpenRouter returns 200 with {"error":{"message":...,"code":...}} when
// the upstream provider is unavailable; this must be treated as retryable,
// not as a silent success.
type openRouterBodyError struct {
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

func (e *openRouterBodyError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("openrouter body error: %s (code %d)", e.Message, e.Code)
	}
	return fmt.Sprintf("openrouter body error: %s", e.Message)
}

// detectOpenRouterBodyError inspects a raw response body for a top-level "error"
// field. Returns non-nil if the body reports an error despite HTTP 200 status.
func detectOpenRouterBodyError(body []byte) *openRouterBodyError {
	var probe struct {
		Error *struct {
			Message  string          `json:"message"`
			Code     json.RawMessage `json:"code"`
			Metadata *struct {
				Raw          string            `json:"raw"`
				ProviderName string            `json:"provider_name"`
				Headers      map[string]string `json:"headers"`
			} `json:"metadata"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe.Error == nil {
		return nil
	}
	bodyErr := &openRouterBodyError{Message: probe.Error.Message}
	if len(probe.Error.Code) > 0 {
		// Code can be number or string — try number first, then quoted string.
		if err := json.Unmarshal(probe.Error.Code, &bodyErr.Code); err != nil {
			var s string
			if err := json.Unmarshal(probe.Error.Code, &s); err == nil {
				_, _ = fmt.Sscanf(s, "%d", &bodyErr.Code)
			}
		}
	}
	if probe.Error.Metadata != nil {
		bodyErr.MetadataPresent = true
		bodyErr.ProviderName = probe.Error.Metadata.ProviderName
		bodyErr.Raw = probe.Error.Metadata.Raw
		bodyErr.RateLimitResetMs = parseInt64Header(probe.Error.Metadata.Headers, "X-RateLimit-Reset")
		bodyErr.RateLimitRemaining = parseInt64Header(probe.Error.Metadata.Headers, "X-RateLimit-Remaining")
		bodyErr.RetryAfterSeconds = parseInt64Header(probe.Error.Metadata.Headers, "Retry-After")
	}
	return bodyErr
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

// classifyUpstreamError maps a parsed OR error envelope to a stable kind
// attribute that TraceQL can filter on without grepping free-form messages.
// The classification is intentionally coarse — fine-grained provider error
// shapes change between deploys, but these buckets stay stable enough that
// a query like {span.error.upstream_kind="invalid_argument"} still matches
// next year. New shapes get "unknown" and surface in raw_message for triage.
func classifyUpstreamError(bodyErr *openRouterBodyError) string {
	if bodyErr == nil {
		return ""
	}
	msg := strings.ToLower(bodyErr.Message + " " + bodyErr.Raw)
	switch {
	case strings.Contains(msg, "invalid_argument") || strings.Contains(msg, "invalid argument"):
		return "invalid_argument"
	case strings.Contains(msg, "safety") || strings.Contains(msg, "harm_category") || strings.Contains(msg, "blocked"):
		return "safety"
	case strings.Contains(msg, "rate_limit") || strings.Contains(msg, "rate limit") || bodyErr.Code == 429:
		return "rate_limited"
	case strings.Contains(msg, "context") && strings.Contains(msg, "length"):
		return "context_length"
	case strings.Contains(msg, "corrupted thought signature"):
		return "thought_signature"
	case bodyErr.Code >= 500:
		return "upstream_5xx"
	case bodyErr.Code >= 400:
		return "upstream_4xx"
	}
	return "unknown"
}

// countFilenameCollisions returns the number of FilePart pairs across
// req.Messages that share a FileName but carry different file_data. Zero is
// the normal case — non-zero is a canary for the same-filename multi-FilePart
// bug class. Telegram defaults photos to "photo.jpg" both for live downloads
// and for stored artifacts, so a prompt that mixes a fresh attachment with a
// reranker-loaded historical artifact can present two distinct images named
// "photo.jpg" to a multimodal model. Reasoning-mode models trip on the
// dissonance and prepend a false "I can't read this file" disclaimer.
//
// We compare file_data bodies via sha256 to avoid keying maps on multi-MB
// strings. The hash is over the entire data URL string (including the
// "data:image/jpeg;base64," prefix); two FileParts with identical bytes but
// different mime declarations still count as distinct, which is what we want.
func countFilenameCollisions(messages []Message) int {
	// filename -> set of distinct content fingerprints
	seen := map[string]map[[32]byte]struct{}{}
	for _, msg := range messages {
		parts, ok := msg.Content.([]interface{})
		if !ok {
			continue
		}
		for _, p := range parts {
			fp, ok := p.(FilePart)
			if !ok {
				continue
			}
			h := sha256.Sum256([]byte(fp.File.FileData))
			set := seen[fp.File.FileName]
			if set == nil {
				set = map[[32]byte]struct{}{}
				seen[fp.File.FileName] = set
			}
			set[h] = struct{}{}
		}
	}
	collisions := 0
	for _, hashes := range seen {
		if len(hashes) > 1 {
			collisions += len(hashes) - 1
		}
	}
	return collisions
}

// recordOpenRouterError attaches an OR error envelope to a span: the raw body
// as an llm.response content event (subject to ContentEnabled gating) and the
// parsed envelope fields as structured attributes (always set when present).
// httpStatus, when non-zero, is recorded verbatim as http.response.status_code
// for filterability and is also used to bucket non-JSON 5xx bodies into an
// edge_<status> error.upstream_kind — non-JSON 5xx bodies (e.g. plain-text
// "error code: 502") fall through every JSON-envelope classifier branch and
// would otherwise leave the span without any structured error class.
// No-op on empty body and zero status.
func recordOpenRouterError(span trace.Span, body []byte, httpStatus int) {
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
	bodyErr := detectOpenRouterBodyError(body)
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
	if kind := classifyUpstreamError(bodyErr); kind != "" {
		attrs = append(attrs, attribute.String("error.upstream_kind", kind))
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

// attemptOutcome describes the result of one HTTP attempt inside a retry loop.
// It exists so recordAttemptEvent can render a single structured event with
// duration, status, body preview, and a classified error.class — without
// scattering AddEvent calls across half a dozen retry-loop branches.
type attemptOutcome struct {
	httpStatus int                  // 0 if no response received (transport err)
	err        error                // non-nil on transport/IO failure
	bodyErr    *openRouterBodyError // OR-envelope error parsed from a 200 body
	body       []byte               // raw response body (may be nil)
}

// classifyAttemptOutcome buckets an attempt into a stable error.class string.
// Buckets are coarse on purpose: TraceQL queries like
// {span.event.error.class =~ "edge_.*"} keep working as new HTTP codes appear.
// "edge_<status>" specifically means OR's edge/CDN returned a non-JSON 4xx/5xx
// (Cloudflare-style) — strong signal that OR's routing logic never ran.
func classifyAttemptOutcome(o attemptOutcome) string {
	if o.err != nil {
		var netErr net.Error
		if errors.As(o.err, &netErr) && netErr.Timeout() {
			return "timeout"
		}
		return "network"
	}
	if o.httpStatus == 0 {
		return "unknown"
	}
	if o.httpStatus == http.StatusOK {
		if o.bodyErr != nil {
			return "body_error"
		}
		return "none"
	}
	if !json.Valid(o.body) {
		return fmt.Sprintf("edge_%d", o.httpStatus)
	}
	return fmt.Sprintf("http_%d", o.httpStatus)
}

// recordAttemptEvent emits one "attempt" span event per HTTP attempt. Cheap
// (events don't multiply spans), and lets each attempt's wall-time, status,
// and class be queried in TraceQL instead of being collapsed into a single
// llm.attempts counter on the parent span. Successful 200 attempts emit an
// event too (class=none) for consistency, but skip the body preview to keep
// traces compact.
func recordAttemptEvent(span trace.Span, idx int, start time.Time, o attemptOutcome) {
	class := classifyAttemptOutcome(o)
	attrs := []attribute.KeyValue{
		attribute.Int("attempt.idx", idx),
		attribute.Int64("attempt.duration_ms", time.Since(start).Milliseconds()),
		attribute.String("error.class", class),
	}
	if o.httpStatus > 0 {
		attrs = append(attrs, attribute.Int("http.response.status_code", o.httpStatus))
	}
	// Tiny preview helps triage edge errors ("error code: 502" et al) without
	// flipping on full content tracing. Skip on the success class to keep the
	// happy-path event lean. Redact base64 first — image-gen 200 bodies and
	// upstream provider envelopes can carry data URLs in `error.metadata.raw`.
	if class != "none" && len(o.body) > 0 {
		preview := obs.RedactBase64Payloads(string(o.body))
		if len(preview) > 256 {
			preview = preview[:256]
		}
		attrs = append(attrs, attribute.String("body_preview", preview))
	}
	if o.err != nil {
		attrs = append(attrs, attribute.String("error.message", o.err.Error()))
	}
	span.AddEvent("attempt", trace.WithAttributes(attrs...))
}

// isRetryableStatusCode returns true if the HTTP status code indicates a retryable error.
func isRetryableStatusCode(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests, // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	default:
		return false
	}
}

// isRetryableError returns true if the error is a network/timeout error that should be retried.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Check for timeout errors
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	// Check for connection errors
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// calculateBackoff returns the delay for the given attempt using exponential backoff with jitter.
func calculateBackoff(attempt int) time.Duration {
	// Limit attempt to avoid overflow (2^5 = 32 seconds is already > maxDelay)
	if attempt > 5 {
		attempt = 5
	}
	// Exponential backoff: baseDelay * 2^attempt
	delay := baseDelay * time.Duration(1<<attempt)
	if delay > maxDelay {
		delay = maxDelay
	}

	// Add jitter: ±20%
	// #nosec G404 -- retry jitter is a throughput tweak, not a security primitive
	jitter := time.Duration(float64(delay) * jitterFactor * (2*rand.Float64() - 1))
	return delay + jitter
}

// File represents a file for multimodal content (v0.6.0: unified format).
type File struct {
	FileName string `json:"filename"`
	FileData string `json:"file_data"` // data URL format: "data:mime/type;base64,..."
}

// FilePart represents a file part in multimodal messages (v0.6.0: unified format).
// This is the recommended format for all file types: images, PDFs, audio, video.
type FilePart struct {
	Type string `json:"type"` // "file"
	File File   `json:"file"`
}

// TextPart represents a text part in messages.
type TextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ImageURLPart represents an image reference for MODEL INPUT, in the
// OpenAI-compatible shape. Use this when calling image-generation or
// image-editing models (e.g. google/gemini-3.1-flash-image-preview) —
// they reject the "file" FilePart shape with "Invalid file type: image/…".
// Regular multimodal text models accept either shape, but for image models
// this is the only format the provider accepts for input images.
type ImageURLPart struct {
	Type     string        `json:"type"` // "image_url"
	ImageURL ImageURLValue `json:"image_url"`
}

type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	ExtraContent interface{} `json:"extra_content,omitempty"`
}

type Message struct {
	Role             string      `json:"role"`
	Content          interface{} `json:"content"`
	ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
	ToolCallID       string      `json:"tool_call_id,omitempty"`
	ReasoningDetails interface{} `json:"reasoning_details,omitempty"`
}

type PDFConfig struct {
	Engine string `json:"engine"`
}

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

// ReasoningConfig controls the model's internal reasoning behavior.
// For Gemini 3: effort levels "minimal", "low", "medium", "high".
// For other models: max_tokens (1024-128000) controls reasoning depth.
//
// NOTE: response-healing plugin breaks reasoning visibility when combined with json_object format.
type ReasoningConfig struct {
	Effort    string `json:"effort,omitempty"`     // Gemini 3: "minimal", "low", "medium", "high"
	MaxTokens int    `json:"max_tokens,omitempty"` // Other models: token budget for reasoning
	Exclude   bool   `json:"exclude,omitempty"`    // Suppress reasoning from response
}

// ImageConfig controls image generation output for models that support it
// (e.g. google/gemini-3.1-flash-image-preview). Only meaningful when the
// request sets Modalities to include "image".
type ImageConfig struct {
	// AspectRatio: "1:1","2:3","3:2","3:4","4:3","4:5","5:4","9:16","16:9","21:9"
	// Extended on nano banana: "1:4","4:1","1:8","8:1".
	AspectRatio string `json:"aspect_ratio,omitempty"`
	// ImageSize: "1K", "2K", "4K". OpenRouter's validator advertises "0.5K"
	// as a fourth value but Google rejects that upstream as INVALID_ARGUMENT;
	// the actual Gemini enum value "512" is in turn rejected by OR's validator.
	// Don't advertise either to the LLM — see docs/bugs/2026-04-30-nano-banana-*.
	ImageSize string `json:"image_size,omitempty"`
}

type ChatCompletionRequest struct {
	Model          string           `json:"model"`
	Messages       []Message        `json:"messages"`
	Plugins        []Plugin         `json:"plugins,omitempty"`
	Tools          []Tool           `json:"tools,omitempty"`
	ToolChoice     any              `json:"tool_choice,omitempty"`
	ResponseFormat interface{}      `json:"response_format,omitempty"`
	Reasoning      *ReasoningConfig `json:"reasoning,omitempty"`
	// Modalities enables image-output models. Use ["image","text"] for Gemini
	// image models that return both text and images. Required for image generation;
	// see TestAllImageGenerationRequestsSetModalities.
	Modalities []string `json:"modalities,omitempty"`
	// ImageConfig is model-specific image-generation configuration
	// (aspect ratio, size). Only used when Modalities includes "image".
	ImageConfig *ImageConfig `json:"image_config,omitempty"`
	// Provider overrides OpenRouter's provider routing for this request.
	// When nil, the client-level default (if any) is applied before sending.
	Provider *ProviderRouting `json:"provider,omitempty"`
	// User is the OpenAI/OpenRouter-standard end-user id, used for abuse
	// signals on the provider side and as user.id on OR-emitted Broadcast
	// spans. Auto-populated from UserID in CreateChatCompletion when empty.
	User string `json:"user,omitempty"`
	// Trace carries OpenRouter's Broadcast metadata. Keys with special
	// meaning: trace_id, parent_span_id, trace_name, span_name,
	// generation_name. CreateChatCompletion auto-fills trace_id and
	// parent_span_id from the current span context so OR-emitted spans
	// nest under our local trace when Broadcast is configured. When
	// Broadcast is disabled on the OR side, the field is ignored.
	Trace map[string]any `json:"trace,omitempty"`

	// UserID is used for metrics tracking only, not sent to API
	UserID int64 `json:"-"`
}

type JSONSchema struct {
	Name   string                 `json:"name"`
	Strict bool                   `json:"strict,omitempty"`
	Schema map[string]interface{} `json:"schema"`
}

type ResponseFormat struct {
	Type       string      `json:"type"`
	JSONSchema *JSONSchema `json:"json_schema,omitempty"`
}

type ResponseFormatJSONSchema struct {
	Type       string     `json:"type"` // "json_schema"
	JSONSchema JSONSchema `json:"json_schema"`
}

// ImageURLValue is the inner object of an ImageOutput; kept as a named type
// so struct literals can be written without repeating the anonymous shape.
type ImageURLValue struct {
	URL string `json:"url"`
}

// ImageOutput represents an image emitted by image-generation models in the
// response. The URL is a base64 data URL, e.g. "data:image/png;base64,...".
type ImageOutput struct {
	Type     string        `json:"type"` // "image_url"
	ImageURL ImageURLValue `json:"image_url"`
}

// ResponseMessage is the assistant message on a ChatCompletionResponse choice.
// Extracted as a named type so test fixtures and helpers don't have to restate
// the anonymous struct shape every time a field is added.
type ResponseMessage struct {
	Role             string        `json:"role"`
	Content          string        `json:"content"`
	ToolCalls        []ToolCall    `json:"tool_calls,omitempty"`
	Reasoning        string        `json:"reasoning,omitempty"`         // Gemini 3: raw reasoning text
	ReasoningDetails interface{}   `json:"reasoning_details,omitempty"` // Structured reasoning details
	Images           []ImageOutput `json:"images,omitempty"`            // Generated images (image-output models)
}

// ResponseChoice is one choice on a ChatCompletionResponse. Named for the same
// reason as ResponseMessage.
type ResponseChoice struct {
	Message      ResponseMessage `json:"message"`
	FinishReason string          `json:"finish_reason,omitempty"`
	Index        int             `json:"index"`
}

type ChatCompletionResponse struct {
	ID       string           `json:"id"`
	Model    string           `json:"model"`
	Provider string           `json:"provider,omitempty"` // Actual provider that served the request (e.g. "Google", "Google AI Studio")
	Choices  []ResponseChoice `json:"choices"`
	Usage    struct {
		PromptTokens     int      `json:"prompt_tokens"`
		CompletionTokens int      `json:"completion_tokens"`
		TotalTokens      int      `json:"total_tokens"`
		Cost             *float64 `json:"cost,omitempty"` // Cost in USD from OpenRouter
	} `json:"usage"`

	// DebugRequestBody contains the raw JSON request body sent to OpenRouter.
	// Not part of API response - populated by client for debugging purposes.
	DebugRequestBody string `json:"-"`

	// DebugResponseBody contains the raw JSON response body from OpenRouter.
	// Not part of API response - populated by client for debugging purposes.
	DebugResponseBody string `json:"-"`
}

type EmbeddingRequest struct {
	Model      string                 `json:"model"`
	Input      []string               `json:"input"`
	Dimensions int                    `json:"dimensions,omitempty"`
	Provider   *ProviderRouting       `json:"provider,omitempty"`
	LogMeta    map[string]interface{} `json:"-"`
	// User mirrors the OpenAI-standard end-user id. See the identical
	// field on ChatCompletionRequest for semantics.
	User string `json:"user,omitempty"`
	// Trace carries OpenRouter Broadcast metadata. See
	// ChatCompletionRequest.Trace for semantics and auto-populated keys.
	Trace map[string]any `json:"trace,omitempty"`
}

type EmbeddingObject struct {
	Object    string    `json:"object"`
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type EmbeddingResponse struct {
	Object string            `json:"object"`
	Data   []EmbeddingObject `json:"data"`
	Model  string            `json:"model"`
	Usage  struct {
		PromptTokens int      `json:"prompt_tokens"`
		TotalTokens  int      `json:"total_tokens"`
		Cost         *float64 `json:"cost,omitempty"` // Cost in USD from OpenRouter
	} `json:"usage"`
}

func NewClient(logger *slog.Logger, apiKey, proxyURL string, defaultProvider *ProviderRouting) (Client, error) {
	return NewClientWithBaseURL(logger, apiKey, proxyURL, "https://openrouter.ai/api/v1", defaultProvider)
}

func NewClientWithBaseURL(logger *slog.Logger, apiKey, proxyURL, baseURL string, defaultProvider *ProviderRouting) (Client, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConnsPerHost:   10, // Increased from default 2 to allow more concurrent requests
	}

	if proxyURL != "" {
		proxy, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxy)
	}

	clientLogger := logger.With("component", "openrouter_client")

	if proxyURL != "" {
		safeProxyURL := proxyURL
		if u, err := url.Parse(proxyURL); err == nil {
			if u.User != nil {
				u.User = url.UserPassword(u.User.Username(), "*****")
				safeProxyURL = u.String()
			}
		}
		clientLogger.Info("Using proxy for OpenRouter", "proxy_url", safeProxyURL)
	}

	return &clientImpl{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   300 * time.Second, // Global timeout for requests (5 min for large contexts)
		},
		apiKey:          apiKey,
		apiEndpoint:     baseURL,
		logger:          clientLogger,
		defaultProvider: defaultProvider,
	}, nil
}

// withBroadcastFields returns trc and user enriched with OpenRouter
// Broadcast linking metadata. When ctx carries a valid span context,
// trace_id and parent_span_id are filled (callers' values win). When user
// is empty and userID is non-zero, user becomes the stringified userID.
// Safe to call when Broadcast is disabled on the OR side — OR ignores
// unknown fields. Returning the values (instead of mutating pointers)
// keeps the call-site one-liner at request assembly time.
func withBroadcastFields(ctx context.Context, trc map[string]any, user string, userID int64) (map[string]any, string) {
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
	if user == "" && userID != 0 {
		user = strconv.FormatInt(userID, 10)
	}
	return trc, user
}

func (c *clientImpl) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (resp ChatCompletionResponse, err error) {
	startTime := time.Now()
	jt := jobtype.FromContext(ctx).String()

	// Span covers the whole call including retries. Per-attempt signals
	// (attempts, retry_delays_ms) land as attrs, not child spans, so we
	// don't multiply cardinality. Named returns let the deferred closure
	// route any terminal err through ObserveErr.
	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/openrouter").Start(
		ctx, "openrouter.CreateChatCompletion",
		trace.WithAttributes(
			attribute.String("gen_ai.system", "openrouter"),
			attribute.String("gen_ai.request.model", req.Model),
			attribute.Int64("user.id", req.UserID),
		),
	)
	var (
		attempts    int
		retryDelays []int64
		reqBody     []byte
	)
	defer func() {
		span.SetAttributes(attribute.Int("llm.attempts", attempts))
		if len(retryDelays) > 0 {
			span.SetAttributes(attribute.Int64Slice("llm.retry_delays_ms", retryDelays))
		}
		if resp.Model != "" {
			span.SetAttributes(attribute.String("gen_ai.response.model", resp.Model))
		}
		if resp.Usage.PromptTokens > 0 || resp.Usage.CompletionTokens > 0 {
			span.SetAttributes(
				attribute.Int("gen_ai.usage.input_tokens", resp.Usage.PromptTokens),
				attribute.Int("gen_ai.usage.output_tokens", resp.Usage.CompletionTokens),
			)
		}
		if resp.Usage.Cost != nil {
			span.SetAttributes(attribute.Float64("llm.cost_usd", *resp.Usage.Cost))
		}
		// Canary attribute, always set: 0 in the normal case. A non-zero count
		// flags that the assembled prompt contains FileParts that share a
		// FileName but differ in content — see countFilenameCollisions for why.
		span.SetAttributes(attribute.Int("prompt.media.filename_collisions", countFilenameCollisions(req.Messages)))
		if len(reqBody) > 0 {
			// Redact: multimodal inputs carry full base64 (1-5 MB per image,
			// 3-4 calls per turn) which trips Tempo max_bytes_per_trace if
			// left raw. Replay reconstructs FilePart bytes via the placeholder
			// hash → snapshot artifact storage. See internal/obs/content.go.
			obs.RecordContent(span, "llm.request", obs.RedactBase64Payloads(string(reqBody)))
		}
		if resp.DebugResponseBody != "" {
			// imagegen success responses carry generated image as base64 in
			// choices[].message.images[].image_url.url — redact for the same
			// trace-size reasons. Failure responses are small text, untouched.
			obs.RecordContent(span, "llm.response", obs.RedactBase64Payloads(resp.DebugResponseBody))
		}
		_ = obs.ObserveErr(span, err)
		span.End()
	}()

	// Calculate context size for logging (text content only, not images/files)
	contextChars := 0
	for _, msg := range req.Messages {
		switch content := msg.Content.(type) {
		case string:
			contextChars += len(content)
		case []interface{}:
			// Multi-modal content (text + images)
			for _, part := range content {
				if partMap, ok := part.(map[string]interface{}); ok {
					if partType, ok := partMap["type"].(string); ok && partType == "text" {
						if text, ok := partMap["text"].(string); ok {
							contextChars += len(text)
						}
					}
				}
			}
		}
	}
	// Rough estimate: ~4 chars per token for Gemini
	estimatedTokens := contextChars / 4

	if req.Provider == nil && c.defaultProvider != nil {
		req.Provider = c.defaultProvider
	}

	// Link our local trace to OR's Broadcast-emitted span, if Broadcast is
	// configured. Does nothing when ctx has no active span or when
	// Broadcast is disabled on the OR side.
	req.Trace, req.User = withBroadcastFields(ctx, req.Trace, req.User, req.UserID)

	// Log request summary (full details available in Agent Debug UI)
	c.logger.Info("Sending request to OpenRouter",
		"model", req.Model,
		"message_count", len(req.Messages),
		"tools_count", len(req.Tools),
		"context_chars", contextChars,
		"estimated_tokens", estimatedTokens,
	)

	body, err := json.Marshal(req)
	if err != nil {
		return ChatCompletionResponse{}, err
	}
	reqBody = body

	// Debug: log request body to inspect multimodal content
	if len(body) > 0 {
		// Check if file_data is present in the request (v0.6.0: unified FilePart format)
		bodyStr := string(body)
		hasFileData := strings.Contains(bodyStr, "file_data")

		c.logger.Debug("OpenRouter request body analysis",
			"body_length", len(body),
			"has_file_data", hasFileData,
			"message_count", len(req.Messages),
		)

		// Log preview for inspection
		preview := bodyStr
		if len(preview) > 5000 {
			preview = preview[:5000] + "... (truncated, total: " + fmt.Sprintf("%d", len(body)) + " bytes)"
		}
		c.logger.Debug("OpenRouter request body preview",
			"body_preview", preview,
		)
	}

	endpoint, err := url.JoinPath(c.apiEndpoint, "chat/completions")
	if err != nil {
		return ChatCompletionResponse{}, err
	}

	var responseBody []byte
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		attempts = attempt + 1
		if attempt > 0 {
			RecordLLMRetry(req.Model)
			delay := calculateBackoff(attempt - 1)
			retryDelays = append(retryDelays, delay.Milliseconds())
			c.logger.Warn("Retrying OpenRouter request",
				"attempt", attempt,
				"max_retries", maxRetries,
				"delay", delay,
				"last_error", lastErr,
			)

			select {
			case <-ctx.Done():
				return ChatCompletionResponse{}, ctx.Err()
			case <-time.After(delay):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewBuffer(body))
		if err != nil {
			return ChatCompletionResponse{}, err
		}

		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("User-Agent", "laplaced/1.0")

		attemptStart := time.Now()
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{err: err})
			if isRetryableError(err) && attempt < maxRetries {
				lastErr = err
				continue
			}
			return ChatCompletionResponse{}, err
		}

		responseBody, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{err: err, httpStatus: resp.StatusCode})
			if isRetryableError(err) && attempt < maxRetries {
				lastErr = err
				continue
			}
			return ChatCompletionResponse{}, err
		}

		c.logger.Debug("OpenRouter response received", "status", resp.Status, "attempt", attempt)

		if resp.StatusCode == http.StatusOK {
			// OpenRouter sometimes returns 200 with an error payload in body when
			// the upstream provider fails. Treat as retryable.
			if bodyErr := detectOpenRouterBodyError(responseBody); bodyErr != nil {
				recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{httpStatus: resp.StatusCode, bodyErr: bodyErr, body: responseBody})
				c.logger.Warn("OpenRouter returned HTTP 200 with error body",
					"error", bodyErr, "body", string(responseBody), "attempt", attempt)
				if attempt < maxRetries {
					lastErr = bodyErr
					continue
				}
				RecordLLMRequest(req.UserID, req.Model, time.Since(startTime).Seconds(), false, 0, 0, nil, jt)
				recordOpenRouterError(span, responseBody, resp.StatusCode)
				return ChatCompletionResponse{}, bodyErr
			}
			recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{httpStatus: resp.StatusCode, body: responseBody})
			break // Success
		}

		recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{httpStatus: resp.StatusCode, body: responseBody})

		// Check if we should retry
		if isRetryableStatusCode(resp.StatusCode) && attempt < maxRetries {
			lastErr = fmt.Errorf("openrouter API error: %s", resp.Status)
			continue
		}

		// Non-retryable error or max retries reached
		c.logger.Error("OpenRouter returned non-OK status", "status", resp.Status, "body", string(responseBody))
		RecordLLMRequest(req.UserID, req.Model, time.Since(startTime).Seconds(), false, 0, 0, nil, jt)
		recordOpenRouterError(span, responseBody, resp.StatusCode)
		return ChatCompletionResponse{}, fmt.Errorf("openrouter API error: %s", resp.Status)
	}

	var chatResp ChatCompletionResponse
	if err := json.NewDecoder(bytes.NewBuffer(responseBody)).Decode(&chatResp); err != nil {
		c.logger.Error("Failed to decode OpenRouter response", "error", err, "body_length", len(responseBody))
		RecordLLMRequest(req.UserID, req.Model, time.Since(startTime).Seconds(), false, 0, 0, nil, jt)
		recordOpenRouterError(span, responseBody, http.StatusOK)
		return ChatCompletionResponse{}, err
	}

	// Store raw request/response bodies for debugging
	chatResp.DebugRequestBody = string(body)
	chatResp.DebugResponseBody = string(responseBody)

	if len(chatResp.Choices) > 0 {
		msg := chatResp.Choices[0].Message
		c.logger.Debug("OpenRouter response content",
			"content", msg.Content,
			"tool_calls_count", len(msg.ToolCalls),
			"reasoning_details", FilterReasoningForLog(msg.ReasoningDetails),
		)

		// Log warning if model outputs tool call as text instead of proper tool_calls
		// This is a known Gemini issue where it hallucinates the internal tool format
		if strings.Contains(msg.Content, "default_api:") {
			c.logger.Warn("Model leaked internal tool call format in content (Gemini hallucination)",
				"content_length", len(msg.Content),
				"content_preview", truncateForLog(msg.Content, 500),
				"tool_calls_count", len(msg.ToolCalls),
				"finish_reason", chatResp.Choices[0].FinishReason,
				"completion_tokens", chatResp.Usage.CompletionTokens,
			)
		}
	}

	logAttrs := []any{
		"model", chatResp.Model,
		"choices", len(chatResp.Choices),
		"prompt_tokens", chatResp.Usage.PromptTokens,
		"completion_tokens", chatResp.Usage.CompletionTokens,
		"total_tokens", chatResp.Usage.TotalTokens,
	}
	if chatResp.Provider != "" {
		logAttrs = append(logAttrs, "provider", chatResp.Provider)
	}
	if chatResp.Usage.Cost != nil {
		logAttrs = append(logAttrs, "cost_usd", *chatResp.Usage.Cost)
	}
	c.logger.Info("OpenRouter response parsed successfully", logAttrs...)

	// Record success metrics
	duration := time.Since(startTime).Seconds()
	RecordLLMRequest(req.UserID, req.Model, duration, true, chatResp.Usage.PromptTokens, chatResp.Usage.CompletionTokens, chatResp.Usage.Cost, jt)

	return chatResp, nil
}

func (c *clientImpl) CreateEmbeddings(ctx context.Context, req EmbeddingRequest) (resp EmbeddingResponse, err error) {
	// Compute total input size once; used both as a span attribute and as a
	// content-event payload when trace_content is on.
	totalChars := 0
	for _, s := range req.Input {
		totalChars += len(s)
	}
	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/openrouter").Start(
		ctx, "openrouter.CreateEmbeddings",
		trace.WithAttributes(
			attribute.String("gen_ai.system", "openrouter"),
			attribute.String("gen_ai.request.model", req.Model),
			attribute.Int("emb.dimensions", req.Dimensions),
			attribute.Int("emb.input_count", len(req.Input)),
			attribute.Int("emb.total_chars", totalChars),
		),
	)
	defer func() {
		if resp.Usage.PromptTokens > 0 {
			span.SetAttributes(attribute.Int("gen_ai.usage.input_tokens", resp.Usage.PromptTokens))
		}
		if obs.ContentEnabled() && len(req.Input) > 0 {
			if body, mErr := json.Marshal(req.Input); mErr == nil {
				obs.RecordContent(span, "emb.inputs", string(body))
			}
		}
		_ = obs.ObserveErr(span, err)
		span.End()
	}()

	embeddingsURL, err := url.JoinPath(c.apiEndpoint, "embeddings")
	if err != nil {
		return EmbeddingResponse{}, err
	}

	if req.Provider == nil && c.defaultProvider != nil {
		req.Provider = c.defaultProvider
	}

	// No per-request userID is plumbed into EmbeddingRequest today (unlike
	// ChatCompletionRequest.UserID), so user is left empty unless the
	// caller sets it explicitly — but trace_id/parent_span_id still link
	// the OR-side span to our tracing.
	req.Trace, req.User = withBroadcastFields(ctx, req.Trace, req.User, 0)

	c.logger.Debug("Sending embedding request to OpenRouter",
		"model", req.Model,
		"input_count", len(req.Input),
		"meta", req.LogMeta,
	)

	body, err := json.Marshal(req)
	if err != nil {
		return EmbeddingResponse{}, err
	}

	var responseBody []byte
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := calculateBackoff(attempt - 1)
			c.logger.Warn("Retrying OpenRouter embeddings request",
				"attempt", attempt,
				"max_retries", maxRetries,
				"delay", delay,
				"last_error", lastErr,
			)

			select {
			case <-ctx.Done():
				return EmbeddingResponse{}, ctx.Err()
			case <-time.After(delay):
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", embeddingsURL, bytes.NewBuffer(body))
		if err != nil {
			return EmbeddingResponse{}, err
		}

		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("User-Agent", "laplaced/1.0")

		attemptStart := time.Now()
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{err: err})
			if isRetryableError(err) && attempt < maxRetries {
				lastErr = err
				continue
			}
			return EmbeddingResponse{}, err
		}

		responseBody, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{err: err, httpStatus: resp.StatusCode})
			if isRetryableError(err) && attempt < maxRetries {
				lastErr = err
				continue
			}
			return EmbeddingResponse{}, err
		}

		if resp.StatusCode == http.StatusOK {
			// OpenRouter returns 200 with an error payload when the upstream
			// provider is unavailable (e.g. {"error":{"code":404}}). Treat as
			// retryable so we don't silently return an empty Data array and
			// cause callers to waste tokens on the next attempt.
			if bodyErr := detectOpenRouterBodyError(responseBody); bodyErr != nil {
				recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{httpStatus: resp.StatusCode, bodyErr: bodyErr, body: responseBody})
				c.logger.Warn("OpenRouter embeddings returned HTTP 200 with error body",
					"error", bodyErr, "body", string(responseBody), "attempt", attempt)
				if attempt < maxRetries {
					lastErr = bodyErr
					continue
				}
				recordOpenRouterError(span, responseBody, resp.StatusCode)
				return EmbeddingResponse{}, bodyErr
			}
			recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{httpStatus: resp.StatusCode, body: responseBody})
			break // Success
		}

		recordAttemptEvent(span, attempt, attemptStart, attemptOutcome{httpStatus: resp.StatusCode, body: responseBody})

		// Check if we should retry
		if isRetryableStatusCode(resp.StatusCode) && attempt < maxRetries {
			lastErr = fmt.Errorf("openrouter API error: %s", resp.Status)
			continue
		}

		// Non-retryable error or max retries reached
		c.logger.Error("OpenRouter embeddings returned non-OK status", "status", resp.Status, "body", string(responseBody))
		recordOpenRouterError(span, responseBody, resp.StatusCode)
		return EmbeddingResponse{}, fmt.Errorf("openrouter API error: %s", resp.Status)
	}

	var embeddingResp EmbeddingResponse
	if err := json.Unmarshal(responseBody, &embeddingResp); err != nil {
		c.logger.Error("Failed to decode embedding response", "error", err, "body", string(responseBody))
		recordOpenRouterError(span, responseBody, http.StatusOK)
		return EmbeddingResponse{}, err
	}

	if len(embeddingResp.Data) == 0 {
		c.logger.Warn("OpenRouter embeddings received NO DATA", "body", string(responseBody))
	} else {
		attrs := []any{
			"object", embeddingResp.Object,
			"count", len(embeddingResp.Data),
			"prompt_tokens", embeddingResp.Usage.PromptTokens,
			"total_tokens", embeddingResp.Usage.TotalTokens,
		}
		if embeddingResp.Usage.Cost != nil {
			attrs = append(attrs, "cost_usd", *embeddingResp.Usage.Cost)
		}
		c.logger.Debug("OpenRouter embeddings received", attrs...)
	}

	return embeddingResp, nil
}

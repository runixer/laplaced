package openrouter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkcodes "go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/runixer/laplaced/internal/obs"
)

// withTracingCapture mirrors testutil.WithTracingCapture — inlined here to
// avoid an import cycle (testutil depends on openrouter for mock fixtures).
func withTracingCapture(t *testing.T) func() tracetest.SpanStubs {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})
	return exporter.GetSpans
}

// TestCreateChatCompletion_RecordsSpan captures the happy path. Asserts the
// span name, gen_ai.* attrs, and laplaced-specific attrs (attempts, tokens).
// Error-path + retry cases live in sibling subtests below.
func TestCreateChatCompletion_RecordsSpan(t *testing.T) {
	getSpans := withTracingCapture(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := ChatCompletionResponse{
			Model: "test-model-resolved",
			Choices: []ResponseChoice{
				{Message: ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"},
			},
			Usage: struct {
				PromptTokens     int      `json:"prompt_tokens"`
				CompletionTokens int      `json:"completion_tokens"`
				TotalTokens      int      `json:"total_tokens"`
				Cost             *float64 `json:"cost,omitempty"`
			}{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	_, err = client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model:    "requested-model",
		UserID:   42,
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	spans := getSpans()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, "openrouter.CreateChatCompletion", span.Name)

	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range span.Attributes {
		attrs[kv.Key] = kv.Value
	}
	assert.Equal(t, "openrouter", attrs["gen_ai.system"].AsString())
	assert.Equal(t, "requested-model", attrs["gen_ai.request.model"].AsString())
	assert.Equal(t, "test-model-resolved", attrs["gen_ai.response.model"].AsString())
	assert.Equal(t, int64(100), attrs["gen_ai.usage.input_tokens"].AsInt64())
	assert.Equal(t, int64(10), attrs["gen_ai.usage.output_tokens"].AsInt64())
	assert.Equal(t, int64(42), attrs["user.id"].AsInt64())
	assert.Equal(t, int64(1), attrs["llm.attempts"].AsInt64(), "single successful attempt")
	assert.Equal(t, sdkcodes.Unset, span.Status.Code)
}

func TestCreateChatCompletion_ContentEventsGatedByToggle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ChatCompletionResponse{
			Choices: []ResponseChoice{{Message: ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()
	client, err := NewClientWithBaseURL(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	run := func() []string {
		getSpans := withTracingCapture(t)
		_, err := client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
			Model:    "m",
			Messages: []Message{{Role: "user", Content: "hi"}},
		})
		require.NoError(t, err)
		spans := getSpans()
		require.Len(t, spans, 1)
		// Filter out "attempt" events — those are structured per-attempt signals
		// (idx, duration, status, class) and are not content; they're emitted
		// regardless of obs.ContentEnabled.
		names := make([]string, 0, len(spans[0].Events))
		for _, ev := range spans[0].Events {
			if ev.Name == "attempt" {
				continue
			}
			names = append(names, ev.Name)
		}
		return names
	}

	t.Run("disabled: no content events", func(t *testing.T) {
		prev := obs.ContentEnabled()
		t.Cleanup(func() { obs.SetContentEnabled(prev) })
		obs.SetContentEnabled(false)

		assert.Empty(t, run())
	})

	t.Run("enabled: llm.request and llm.response present", func(t *testing.T) {
		prev := obs.ContentEnabled()
		t.Cleanup(func() { obs.SetContentEnabled(prev) })
		obs.SetContentEnabled(true)

		names := run()
		assert.Contains(t, names, "llm.request")
		assert.Contains(t, names, "llm.response")
	})
}

func TestCreateChatCompletion_TerminalError_SetsErrorStatus(t *testing.T) {
	getSpans := withTracingCapture(t)

	// 401 is non-retryable per isRetryableStatusCode — terminates immediately.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"nope"}`))
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	_, err = client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)

	spans := getSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, sdkcodes.Error, spans[0].Status.Code)

	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range spans[0].Attributes {
		attrs[kv.Key] = kv.Value
	}
	assert.Equal(t, int64(401), attrs["http.response.status_code"].AsInt64(),
		"http.response.status_code attribute should always be present on error path")
}

// TestCreateChatCompletion_UpstreamProviderError_AttachesEnvelope mirrors the
// 2026-04-28 reranker incident: AI Studio rejected a vertex-issued thought
// signature with "Corrupted thought signature.", OR wrapped it in its standard
// envelope with provider_name and raw fields. Asserts that we lift those
// fields onto the span as queryable attrs and emit the body as llm.response.
func TestCreateChatCompletion_UpstreamProviderError_AttachesEnvelope(t *testing.T) {
	prev := obs.ContentEnabled()
	t.Cleanup(func() { obs.SetContentEnabled(prev) })
	obs.SetContentEnabled(true)

	getSpans := withTracingCapture(t)

	envelope := `{"error":{"message":"Provider returned error","code":400,"metadata":{"raw":"{\"error\":{\"code\":400,\"message\":\"Corrupted thought signature.\",\"status\":\"INVALID_ARGUMENT\"}}","provider_name":"Google AI Studio","is_byok":false}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(envelope))
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	_, err = client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)

	spans := getSpans()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, sdkcodes.Error, span.Status.Code)

	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range span.Attributes {
		attrs[kv.Key] = kv.Value
	}
	assert.Equal(t, "Provider returned error", attrs["error.upstream_message"].AsString())
	assert.Equal(t, int64(400), attrs["error.upstream_code"].AsInt64())
	assert.Equal(t, "Google AI Studio", attrs["error.upstream_provider"].AsString())

	var responseEvent *string
	for i := range span.Events {
		if span.Events[i].Name == "llm.response" {
			for _, kv := range span.Events[i].Attributes {
				if kv.Key == "body" {
					s := kv.Value.AsString()
					responseEvent = &s
				}
			}
		}
	}
	require.NotNil(t, responseEvent, "llm.response event must be present on error path")
	assert.Equal(t, envelope, *responseEvent)
}

// TestCreateChatCompletion_PopulatesBroadcastFields asserts the outgoing
// request carries the fields OpenRouter Broadcast needs to nest its own
// spans under ours: trace.trace_id, trace.parent_span_id, and user.
func TestCreateChatCompletion_PopulatesBroadcastFields(t *testing.T) {
	_ = withTracingCapture(t)

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ChatCompletionResponse{
			Choices: []ResponseChoice{{Message: ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	_, err = client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model:    "m",
		UserID:   314,
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	require.NotNil(t, gotBody, "server should have received a request body")
	assert.Equal(t, "314", gotBody["user"], "user field must be stringified UserID")

	trc, ok := gotBody["trace"].(map[string]any)
	require.True(t, ok, "trace field must be a map")
	// Active span is the openrouter.CreateChatCompletion one — so
	// trace_id matches the span's trace, and parent_span_id is that
	// span's id. We do not know the IDs ahead of time; assert shape only.
	assert.NotEmpty(t, trc["trace_id"], "trace.trace_id must be populated from ctx")
	assert.NotEmpty(t, trc["parent_span_id"], "trace.parent_span_id must be populated from ctx")
}

// TestCreateChatCompletion_BroadcastFields_CallerWins asserts caller-set
// trace/user values are not overwritten by the auto-populate helper.
func TestCreateChatCompletion_BroadcastFields_CallerWins(t *testing.T) {
	_ = withTracingCapture(t)

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ChatCompletionResponse{
			Choices: []ResponseChoice{{Message: ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	_, err = client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model:    "m",
		UserID:   314,
		User:     "explicit-user",
		Trace:    map[string]any{"trace_id": "caller-trace", "parent_span_id": "caller-span"},
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	assert.Equal(t, "explicit-user", gotBody["user"])
	trc := gotBody["trace"].(map[string]any)
	assert.Equal(t, "caller-trace", trc["trace_id"])
	assert.Equal(t, "caller-span", trc["parent_span_id"])
}

func TestCreateEmbeddings_RecordsSpan(t *testing.T) {
	getSpans := withTracingCapture(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := EmbeddingResponse{
			Object: "list",
			Data:   []EmbeddingObject{{Embedding: []float32{0.1, 0.2, 0.3}}},
			Usage: struct {
				PromptTokens int      `json:"prompt_tokens"`
				TotalTokens  int      `json:"total_tokens"`
				Cost         *float64 `json:"cost,omitempty"`
			}{PromptTokens: 7},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	_, err = client.CreateEmbeddings(context.Background(), EmbeddingRequest{
		Model:      "emb-model",
		Input:      []string{"hello", "world"},
		Dimensions: 1536,
	})
	require.NoError(t, err)

	spans := getSpans()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, "openrouter.CreateEmbeddings", span.Name)

	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range span.Attributes {
		attrs[kv.Key] = kv.Value
	}
	assert.Equal(t, "emb-model", attrs["gen_ai.request.model"].AsString())
	assert.Equal(t, int64(2), attrs["emb.input_count"].AsInt64())
	assert.Equal(t, int64(10), attrs["emb.total_chars"].AsInt64(), "len(hello)+len(world)=10")
	assert.Equal(t, int64(1536), attrs["emb.dimensions"].AsInt64())
	assert.Equal(t, int64(7), attrs["gen_ai.usage.input_tokens"].AsInt64())
	assert.Equal(t, sdkcodes.Unset, span.Status.Code)
}

// TestCreateChatCompletion_RedactsBase64InContentEvents is the guard against
// raw base64 leaking into trace events. The exact failure mode this prevents:
// a 5 MB image in llm.request × 3-4 OR calls × bot+OR sides crosses Tempo's
// max_bytes_per_trace and the second half of the trace silently disappears.
//
// Asserts that BOTH llm.request and llm.response (imagegen output) carry
// only "redacted:sha256:..." placeholders — no "data:<mime>;base64,<long>"
// substring should survive on any span event body.
func TestCreateChatCompletion_RedactsBase64InContentEvents(t *testing.T) {
	prev := obs.ContentEnabled()
	t.Cleanup(func() { obs.SetContentEnabled(prev) })
	obs.SetContentEnabled(true)

	getSpans := withTracingCapture(t)

	// Synthesize a payload that satisfies the regex's 32+ char floor.
	rawIn := []byte(strings.Repeat("INPUT-IMAGE-PAYLOAD-", 4))   // 80 bytes
	rawOut := []byte(strings.Repeat("OUTPUT-IMAGE-PAYLOAD-", 4)) // 84 bytes
	inputDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(rawIn)
	outputDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(rawOut)

	// imagegen-shape response: choices[].message.images[].image_url.url
	// carries the generated image as a data URL — the redact must catch it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := map[string]any{
			"model": "google/gemini-3.1-flash-image-preview",
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "",
					"images": []map[string]any{{
						"type":      "image_url",
						"image_url": map[string]any{"url": outputDataURL},
					}},
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 50, "completion_tokens": 0, "total_tokens": 50},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	_, err = client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model:      "google/gemini-3.1-flash-image-preview",
		Modalities: []string{"image", "text"},
		Messages: []Message{{Role: "user", Content: []interface{}{
			TextPart{Type: "text", Text: "edit this"},
			ImageURLPart{Type: "image_url", ImageURL: ImageURLValue{URL: inputDataURL}},
		}}},
	})
	require.NoError(t, err)

	spans := getSpans()
	require.Len(t, spans, 1)

	rawBase64Re := regexp.MustCompile(`data:[a-zA-Z0-9./+\-]+;base64,[A-Za-z0-9+/=]{32,}`)

	var sawRequest, sawResponse bool
	for _, ev := range spans[0].Events {
		if ev.Name != "llm.request" && ev.Name != "llm.response" {
			continue
		}
		for _, kv := range ev.Attributes {
			if kv.Key != "body" {
				continue
			}
			body := kv.Value.AsString()
			assert.False(t, rawBase64Re.MatchString(body),
				"event %q body must not contain raw data:<mime>;base64,<long-payload> after redact: got %q",
				ev.Name, body[:min(200, len(body))])
			assert.Contains(t, body, "redacted:sha256:",
				"event %q body should carry the placeholder for the multimodal input", ev.Name)
			if ev.Name == "llm.request" {
				sawRequest = true
			}
			if ev.Name == "llm.response" {
				sawResponse = true
			}
		}
	}
	assert.True(t, sawRequest, "llm.request event must be present")
	assert.True(t, sawResponse, "llm.response event must be present")
}

// TestClassifyUpstreamError pins the coarse error kind classification used by
// recordOpenRouterError. The scenarios mirror real OR error envelopes seen
// in prod (INVALID_ARGUMENT for image-size+input-image incompat, Corrupted
// thought signature for the reranker-turn-2 case from 2026-04-28, plus
// generic 5xx and 429s). New classes get "unknown" not error.
func TestClassifyUpstreamError(t *testing.T) {
	cases := []struct {
		name string
		in   *openRouterBodyError
		want string
	}{
		{"nil", nil, ""},
		{
			"invalid_argument from Google",
			&openRouterBodyError{Message: "Provider returned error", Code: 400, Raw: `{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT"}}`},
			"invalid_argument",
		},
		{
			"thought signature corruption",
			&openRouterBodyError{Message: "Provider returned error", Code: 400, Raw: `{"error":{"message":"Corrupted thought signature."}}`},
			"thought_signature",
		},
		{
			"safety blocked",
			&openRouterBodyError{Message: "Content blocked due to safety policy", Code: 400},
			"safety",
		},
		{
			"rate limit by code",
			&openRouterBodyError{Message: "Too many requests", Code: 429},
			"rate_limited",
		},
		{
			"rate limit by message",
			&openRouterBodyError{Message: "rate_limit_exceeded", Code: 400},
			"rate_limited",
		},
		{
			"context length",
			&openRouterBodyError{Message: "context length exceeded for model", Code: 400},
			"context_length",
		},
		{
			"upstream 5xx",
			&openRouterBodyError{Message: "internal error", Code: 503},
			"upstream_5xx",
		},
		{
			"plain 4xx falls through to upstream_4xx",
			&openRouterBodyError{Message: "weird payload", Code: 422},
			"upstream_4xx",
		},
		{
			"unknown shape",
			&openRouterBodyError{Message: "something weird"},
			"unknown",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, classifyUpstreamError(c.in))
		})
	}
}

// TestCreateChatCompletion_UpstreamError_PopulatesKindAttr verifies the
// classification feeds onto the span as error.upstream_kind. With this attr
// in place, TraceQL queries like {span.error.upstream_kind="invalid_argument"}
// surface 2026-04-29-style imagegen failures without scraping logs.
func TestCreateChatCompletion_UpstreamError_PopulatesKindAttr(t *testing.T) {
	getSpans := withTracingCapture(t)

	envelope := `{"error":{"message":"Provider returned error","code":400,"metadata":{"raw":"{\"error\":{\"code\":400,\"message\":\"Request contains an invalid argument.\",\"status\":\"INVALID_ARGUMENT\"}}","provider_name":"Google AI Studio","is_byok":false}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(envelope))
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	_, err = client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)

	spans := getSpans()
	require.Len(t, spans, 1)
	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range spans[0].Attributes {
		attrs[kv.Key] = kv.Value
	}
	assert.Equal(t, "invalid_argument", attrs["error.upstream_kind"].AsString())
}

// attemptEventsByIdx returns attempt-event attribute maps in idx order. Helper
// for the attempt-event tests below.
func attemptEventsByIdx(t *testing.T, evs []sdktrace.Event) []map[attribute.Key]attribute.Value {
	t.Helper()
	out := []map[attribute.Key]attribute.Value{}
	for _, ev := range evs {
		if ev.Name != "attempt" {
			continue
		}
		m := map[attribute.Key]attribute.Value{}
		for _, kv := range ev.Attributes {
			m[kv.Key] = kv.Value
		}
		out = append(out, m)
	}
	return out
}

// TestCreateChatCompletion_EdgePlainText502_4xRetries covers the case where
// OR's edge returns a non-JSON 502 ("error code: 502") for every retry. With
// no JSON envelope to parse, classifyUpstreamError can't help — the new
// instrumentation must still classify the span as error.upstream_kind="edge_502"
// + http.response.status_code=502, and emit one "attempt" event per retry
// with class="edge_502" so TraceQL can find these without scraping bodies.
func TestCreateChatCompletion_EdgePlainText502_4xRetries(t *testing.T) {
	getSpans := withTracingCapture(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("error code: 502"))
	}))
	defer server.Close()

	// Override the package-level baseDelay/maxDelay via a tighter http client
	// timeout — the retry loop itself uses calculateBackoff(maxDelay=30s) so
	// a real four-attempt loop would block 30+ seconds. We can't shrink that
	// from outside, so this test accepts ~6s of real time across 3 backoffs
	// (1s + 2s + 4s with ±20% jitter). Acceptable for a guard test that fires
	// once per CI run; if it becomes painful, parameterize calculateBackoff.
	client, err := NewClientWithBaseURL(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	_, err = client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model:    "m",
		UserID:   42,
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)

	spans := getSpans()
	require.Len(t, spans, 1)
	span := spans[0]
	assert.Equal(t, sdkcodes.Error, span.Status.Code)

	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range span.Attributes {
		attrs[kv.Key] = kv.Value
	}
	assert.Equal(t, int64(maxRetries+1), attrs["llm.attempts"].AsInt64(),
		"all retries should exhaust on 502")
	assert.Equal(t, int64(502), attrs["http.response.status_code"].AsInt64())
	assert.Equal(t, "edge_502", attrs["error.upstream_kind"].AsString(),
		"plain-text 502 body must classify as edge_502, not unknown")
	assert.Equal(t, "error code: 502", attrs["error.upstream_message"].AsString())

	events := attemptEventsByIdx(t, span.Events)
	require.Len(t, events, maxRetries+1, "one attempt event per HTTP attempt")
	for i, ev := range events {
		assert.Equal(t, int64(i), ev["attempt.idx"].AsInt64())
		assert.Equal(t, int64(502), ev["http.response.status_code"].AsInt64())
		assert.Equal(t, "edge_502", ev["error.class"].AsString())
		assert.Equal(t, "error code: 502", ev["body_preview"].AsString())
		assert.GreaterOrEqual(t, ev["attempt.duration_ms"].AsInt64(), int64(0))
	}
}

// TestCreateChatCompletion_AttemptEvent_SuccessSingleAttempt asserts that a
// single successful 200 response still emits exactly one attempt event with
// class="none" and no body_preview. Lets dashboards count attempt events as
// a stand-in for "OR call count" without conditioning on error state.
func TestCreateChatCompletion_AttemptEvent_SuccessSingleAttempt(t *testing.T) {
	getSpans := withTracingCapture(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ChatCompletionResponse{
			Choices: []ResponseChoice{{Message: ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	_, err = client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	spans := getSpans()
	require.Len(t, spans, 1)

	events := attemptEventsByIdx(t, spans[0].Events)
	require.Len(t, events, 1)
	ev := events[0]
	assert.Equal(t, int64(0), ev["attempt.idx"].AsInt64())
	assert.Equal(t, int64(200), ev["http.response.status_code"].AsInt64())
	assert.Equal(t, "none", ev["error.class"].AsString())
	_, hasPreview := ev["body_preview"]
	assert.False(t, hasPreview, "success attempts must skip body_preview to keep happy-path traces lean")
}

// TestCreateChatCompletion_AttemptEvent_RetryThenSucceed asserts both
// transient-failure and final-success attempts are recorded — one retryable
// 503 followed by a 200 should produce exactly two attempt events with the
// expected class progression (http_503 → none).
func TestCreateChatCompletion_AttemptEvent_RetryThenSucceed(t *testing.T) {
	getSpans := withTracingCapture(t)

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream busy","code":503}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ChatCompletionResponse{
			Choices: []ResponseChoice{{Message: ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test_api_key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	_, err = client.CreateChatCompletion(context.Background(), ChatCompletionRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	spans := getSpans()
	require.Len(t, spans, 1)

	events := attemptEventsByIdx(t, spans[0].Events)
	require.Len(t, events, 2)

	assert.Equal(t, int64(0), events[0]["attempt.idx"].AsInt64())
	assert.Equal(t, int64(503), events[0]["http.response.status_code"].AsInt64())
	// JSON-shaped 5xx body classifies as http_503 (not edge) — the body has
	// the OR envelope shape, so OR's app server saw the request.
	assert.Equal(t, "http_503", events[0]["error.class"].AsString())
	assert.Contains(t, events[0]["body_preview"].AsString(), "upstream busy")

	assert.Equal(t, int64(1), events[1]["attempt.idx"].AsInt64())
	assert.Equal(t, int64(200), events[1]["http.response.status_code"].AsInt64())
	assert.Equal(t, "none", events[1]["error.class"].AsString())
}

// TestClassifyAttemptOutcome pins the class buckets used by attempt events.
// Cases mirror real shapes seen in production: edge plain-text 502 from a
// CDN, JSON-shaped upstream 5xx, 200 with envelope error, network and
// timeout transport errors, and the plain happy path.
func TestClassifyAttemptOutcome(t *testing.T) {
	netTimeout := &net.OpError{Op: "dial", Net: "tcp", Err: &timeoutError{}}
	netRefused := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}

	cases := []struct {
		name string
		o    attemptOutcome
		want string
	}{
		{"happy path 200", attemptOutcome{httpStatus: 200, body: []byte(`{"id":"x"}`)}, "none"},
		{"200 with body envelope", attemptOutcome{httpStatus: 200, bodyErr: &openRouterBodyError{Message: "x"}, body: []byte(`{"error":{}}`)}, "body_error"},
		{"edge plain-text 502", attemptOutcome{httpStatus: 502, body: []byte("error code: 502")}, "edge_502"},
		{"edge empty body 504", attemptOutcome{httpStatus: 504, body: nil}, "edge_504"},
		{"edge HTML 503", attemptOutcome{httpStatus: 503, body: []byte("<html>nope</html>")}, "edge_503"},
		{"json 503 from OR", attemptOutcome{httpStatus: 503, body: []byte(`{"error":{"code":503}}`)}, "http_503"},
		{"json 401", attemptOutcome{httpStatus: 401, body: []byte(`{"error":"nope"}`)}, "http_401"},
		{"timeout", attemptOutcome{err: netTimeout}, "timeout"},
		{"network", attemptOutcome{err: netRefused}, "network"},
		{"unknown no status no err", attemptOutcome{}, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, classifyAttemptOutcome(c.o))
		})
	}
}

package openrouter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
		names := make([]string, 0, len(spans[0].Events))
		for _, ev := range spans[0].Events {
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

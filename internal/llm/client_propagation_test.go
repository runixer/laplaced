package llm

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// traceparentRe matches a W3C traceparent header: 00-<trace_id>-<span_id>-<flags>.
var traceparentRe = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

// withW3CPropagator installs the same propagator obs.InitTracing sets and
// restores the previous one on cleanup, so tests don't leak global state.
func withW3CPropagator(t *testing.T) {
	t.Helper()
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })
}

func TestNewAPIRequest_TraceContextPropagation(t *testing.T) {
	c := &clientImpl{apiKey: "test-key"}

	t.Run("recording span injects valid traceparent", func(t *testing.T) {
		withTracingCapture(t)
		withW3CPropagator(t)

		ctx, span := otel.Tracer("test").Start(context.Background(), "parent")
		defer span.End()

		req, err := c.newAPIRequest(ctx, "http://example.com/v1/chat/completions", []byte("{}"))
		require.NoError(t, err)

		tp := req.Header.Get("traceparent")
		require.Regexp(t, traceparentRe, tp)
		sc := trace.SpanContextFromContext(ctx)
		assert.Contains(t, tp, sc.TraceID().String(), "traceparent must carry the active trace id")
		assert.Contains(t, tp, sc.SpanID().String(), "traceparent must carry the active span id")

		// Standard headers still present after the refactor.
		assert.Equal(t, "Bearer test-key", req.Header.Get("Authorization"))
		assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
		assert.Equal(t, "laplaced/1.0", req.Header.Get("User-Agent"))
	})

	t.Run("no span in context means no traceparent", func(t *testing.T) {
		withW3CPropagator(t)

		req, err := c.newAPIRequest(context.Background(), "http://example.com/v1/chat/completions", []byte("{}"))
		require.NoError(t, err)
		assert.Empty(t, req.Header.Get("traceparent"))
	})

	t.Run("noop propagator (telemetry disabled) means no traceparent", func(t *testing.T) {
		withTracingCapture(t)
		prev := otel.GetTextMapPropagator()
		// Empty composite == noop, the OTel default when tracing is disabled.
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
		t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

		ctx, span := otel.Tracer("test").Start(context.Background(), "parent")
		defer span.End()

		req, err := c.newAPIRequest(ctx, "http://example.com/v1/chat/completions", []byte("{}"))
		require.NoError(t, err)
		assert.Empty(t, req.Header.Get("traceparent"))
	})
}

// TestCreateChatCompletion_PropagatesTraceContext guards the call site: the
// request that actually leaves CreateChatCompletion must carry traceparent,
// not just one built by the helper in isolation.
func TestCreateChatCompletion_PropagatesTraceContext(t *testing.T) {
	withTracingCapture(t)
	withW3CPropagator(t)

	var gotTraceparent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		resp := ChatCompletionResponse{
			Choices: []ResponseChoice{
				{Message: ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClientWithBaseURL(slog.New(slog.NewJSONHandler(io.Discard, nil)), "test-key", "", server.URL+"/api/v1", nil)
	require.NoError(t, err)

	ctx, span := otel.Tracer("test").Start(context.Background(), "parent")
	defer span.End()

	_, err = client.CreateChatCompletion(ctx, ChatCompletionRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Regexp(t, traceparentRe, gotTraceparent)
}

package obs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"go.opentelemetry.io/otel/attribute"
)

// withTestProvider swaps the global TracerProvider with an in-memory one
// for the duration of the test. Scoped here to avoid an import cycle with
// internal/testutil (which would in turn depend on obs).
func withTestProvider(t *testing.T) func() tracetest.SpanStubs {
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

func TestContentToggle_DefaultsOff(t *testing.T) {
	// Each test must not leak state. Force a known starting state and
	// restore it in cleanup so unrelated tests are unaffected.
	prev := ContentEnabled()
	t.Cleanup(func() { SetContentEnabled(prev) })
	SetContentEnabled(false)

	assert.False(t, ContentEnabled())
}

func TestRecordContent_Disabled_NoEvent(t *testing.T) {
	prev := ContentEnabled()
	t.Cleanup(func() { SetContentEnabled(prev) })
	SetContentEnabled(false)

	getSpans := withTestProvider(t)
	_, span := otel.Tracer("test").Start(context.Background(), "op")
	RecordContent(span, "llm.request", "hidden body")
	span.End()

	spans := getSpans()
	require.Len(t, spans, 1)
	assert.Empty(t, spans[0].Events, "no events when content disabled")
}

func TestRecordContent_Enabled_AttachesBodyAndExtras(t *testing.T) {
	prev := ContentEnabled()
	t.Cleanup(func() { SetContentEnabled(prev) })
	SetContentEnabled(true)

	getSpans := withTestProvider(t)
	_, span := otel.Tracer("test").Start(context.Background(), "op")
	RecordContent(span, "llm.request", "the body",
		attribute.Int("size", 42),
		attribute.String("model", "test-model"),
	)
	span.End()

	spans := getSpans()
	require.Len(t, spans, 1)
	require.Len(t, spans[0].Events, 1)
	ev := spans[0].Events[0]
	assert.Equal(t, "llm.request", ev.Name)

	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range ev.Attributes {
		attrs[kv.Key] = kv.Value
	}
	assert.Equal(t, "the body", attrs["body"].AsString())
	assert.Equal(t, int64(42), attrs["size"].AsInt64())
	assert.Equal(t, "test-model", attrs["model"].AsString())
}

package testutil

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// WithTracingCapture swaps the global TracerProvider with an in-memory one
// for the duration of the test and returns a closure that yields the spans
// collected so far. The previous provider is restored on t.Cleanup.
//
// Use this to assert on span names, attributes, and events without running
// an exporter or touching the network:
//
//	getSpans := testutil.WithTracingCapture(t)
//	// ... exercise code that opens spans ...
//	spans := getSpans()
//	require.Len(t, spans, 1)
//	assert.Equal(t, "bot.processMessageGroup", spans[0].Name)
func WithTracingCapture(t *testing.T) func() tracetest.SpanStubs {
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

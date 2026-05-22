package rag

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/storage"
)

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

func attrMap(kvs []attribute.KeyValue) map[attribute.Key]attribute.Value {
	out := make(map[attribute.Key]attribute.Value, len(kvs))
	for _, kv := range kvs {
		out[kv.Key] = kv.Value
	}
	return out
}

// TestRecordCoverage_NoStragglers verifies the happy path — splitter covered
// the whole chunk, coverage_ok=true and no stragglers attrs/events emitted.
func TestRecordCoverage_NoStragglers(t *testing.T) {
	getSpans := withTracingCapture(t)
	tracer := otel.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "rag.processChunk")

	chunk := []storage.Message{
		{ID: 100, Role: "user", Content: "a"},
		{ID: 101, Role: "assistant", Content: "b"},
	}
	recordCoverage(ctx, chunk, nil)
	span.End()

	spans := getSpans()
	require.Len(t, spans, 1)
	attrs := attrMap(spans[0].Attributes)
	assert.True(t, attrs["splitter.coverage_ok"].AsBool())
	assert.Equal(t, int64(0), attrs["splitter.straggler_count"].AsInt64())
	assert.InDelta(t, 1.0, attrs["splitter.coverage_pct"].AsFloat64(), 0.0001)
	// No straggler_ids attr when none.
	_, hasIDs := attrs["splitter.straggler_ids"]
	assert.False(t, hasIDs)
}

// TestRecordCoverage_StragglersPopulated is the motivating case: when the
// splitter LLM leaves N messages uncovered, the rag.processChunk span gets
// straggler_count, straggler_ids and coverage_pct attached. TraceQL query
// `{ span.splitter.straggler_count > 0 }` becomes the answer to "which chunk
// is stuck and which msg IDs did the model drop".
func TestRecordCoverage_StragglersPopulated(t *testing.T) {
	getSpans := withTracingCapture(t)
	tracer := otel.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "rag.processChunk")

	chunk := []storage.Message{
		{ID: 100, Role: "user", Content: "covered"},
		{ID: 101, Role: "assistant", Content: "covered"},
		{ID: 102, Role: "user", Content: "lost message"},
		{ID: 103, Role: "assistant", Content: "lost response"},
	}
	recordCoverage(ctx, chunk, []int64{102, 103})
	span.End()

	spans := getSpans()
	require.Len(t, spans, 1)
	attrs := attrMap(spans[0].Attributes)
	assert.False(t, attrs["splitter.coverage_ok"].AsBool())
	assert.Equal(t, int64(2), attrs["splitter.straggler_count"].AsInt64())
	assert.Equal(t, []int64{102, 103}, attrs["splitter.straggler_ids"].AsInt64Slice())
	assert.InDelta(t, 0.5, attrs["splitter.coverage_pct"].AsFloat64(), 0.0001)
}

// TestRecordCoverage_CapsLargeStragglerList ensures the straggler_ids attr
// is bounded at maxStragglerIDsInTrace — a pathological chunk where the LLM
// drops nearly everything should not blow up trace size.
func TestRecordCoverage_CapsLargeStragglerList(t *testing.T) {
	getSpans := withTracingCapture(t)
	tracer := otel.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "rag.processChunk")

	chunk := make([]storage.Message, 200)
	ids := make([]int64, 200)
	for i := range chunk {
		chunk[i] = storage.Message{ID: int64(1000 + i), Role: "user", Content: "x"}
		ids[i] = int64(1000 + i)
	}
	recordCoverage(ctx, chunk, ids)
	span.End()

	spans := getSpans()
	require.Len(t, spans, 1)
	attrs := attrMap(spans[0].Attributes)
	// Count is the true total even though IDs are capped — both signals
	// matter: count for severity, IDs for actionable debugging.
	assert.Equal(t, int64(200), attrs["splitter.straggler_count"].AsInt64())
	assert.Equal(t, maxStragglerIDsInTrace, len(attrs["splitter.straggler_ids"].AsInt64Slice()))
}

// TestRecordCoverage_ContentEventGated checks the trace_content toggle gates
// the splitter.stragglers content event. When off, no event; when on, JSON of
// the dropped messages lands as an event body.
func TestRecordCoverage_ContentEventGated(t *testing.T) {
	chunk := []storage.Message{
		{ID: 100, Role: "user", Content: "hi"},
		{ID: 101, Role: "assistant", Content: "lost"},
	}

	t.Run("content disabled - no event", func(t *testing.T) {
		obs.SetContentEnabled(false)
		t.Cleanup(func() { obs.SetContentEnabled(false) })

		getSpans := withTracingCapture(t)
		tracer := otel.Tracer("test")
		ctx, span := tracer.Start(context.Background(), "rag.processChunk")
		recordCoverage(ctx, chunk, []int64{101})
		span.End()

		spans := getSpans()
		require.Len(t, spans, 1)
		assert.Empty(t, spans[0].Events, "no content events should fire while gated off")
	})

	t.Run("content enabled - event with body", func(t *testing.T) {
		obs.SetContentEnabled(true)
		t.Cleanup(func() { obs.SetContentEnabled(false) })

		getSpans := withTracingCapture(t)
		tracer := otel.Tracer("test")
		ctx, span := tracer.Start(context.Background(), "rag.processChunk")
		recordCoverage(ctx, chunk, []int64{101})
		span.End()

		spans := getSpans()
		require.Len(t, spans, 1)
		var stragglerEvent *string
		for _, ev := range spans[0].Events {
			if ev.Name != "splitter.stragglers" {
				continue
			}
			for _, a := range ev.Attributes {
				if a.Key == "body" {
					v := a.Value.AsString()
					stragglerEvent = &v
				}
			}
		}
		require.NotNil(t, stragglerEvent, "splitter.stragglers event missing")
		var decoded []map[string]any
		require.NoError(t, json.Unmarshal([]byte(*stragglerEvent), &decoded))
		require.Len(t, decoded, 1)
		assert.Equal(t, float64(101), decoded[0]["id"])
		assert.Equal(t, "assistant", decoded[0]["role"])
		assert.Contains(t, decoded[0]["preview"], "lost")
	})
}

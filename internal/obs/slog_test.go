package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestLoggerWithSpan_NoSpanInContext_ReturnsUnchanged(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, nil))

	got := LoggerWithSpan(context.Background(), base)
	got.Info("hello")

	// Logger should be unchanged: no trace_id/span_id fields present.
	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	_, hasTrace := rec["trace_id"]
	_, hasSpan := rec["span_id"]
	assert.False(t, hasTrace, "no span in ctx → no trace_id should be added")
	assert.False(t, hasSpan, "no span in ctx → no span_id should be added")
}

func TestLoggerWithSpan_WithValidSpan_AddsIDs(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("0102030405060708")
	require.NoError(t, err)

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, nil))
	got := LoggerWithSpan(ctx, base)
	got.Info("hello")

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	assert.Equal(t, "0102030405060708090a0b0c0d0e0f10", rec["trace_id"])
	assert.Equal(t, "0102030405060708", rec["span_id"])
}

func TestLoggerWithSpan_BindingPersistsAcrossCalls(t *testing.T) {
	traceID, _ := trace.TraceIDFromHex("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	spanID, _ := trace.SpanIDFromHex("bbbbbbbbbbbbbbbb")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, nil))
	scoped := LoggerWithSpan(ctx, base)
	scoped.Info("first")
	scoped.Info("second")

	// Both records should carry the trace/span ids.
	dec := json.NewDecoder(&buf)
	for i := 0; i < 2; i++ {
		var rec map[string]any
		require.NoError(t, dec.Decode(&rec), "record %d should decode", i)
		assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", rec["trace_id"])
		assert.Equal(t, "bbbbbbbbbbbbbbbb", rec["span_id"])
	}
}

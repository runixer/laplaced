package obs

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// LoggerWithSpan returns logger augmented with "trace_id" and "span_id" fields
// taken from the OTEL span attached to ctx. If ctx has no valid span (e.g. a
// background goroutine that hasn't been instrumented), logger is returned
// unchanged.
//
// Intended to be called once per logical scope — typically right after the
// root span for that scope is started — and the returned logger then threaded
// through the entire scope. All subsequent `.Info()`/`.Warn()`/`.Error()`
// calls on the scoped logger inherit the trace/span ids automatically.
//
// This bridges the gap that this codebase uses non-context slog APIs
// (`logger.Info(...)` rather than `logger.InfoContext(ctx, ...)`), so a
// context-extracting Handler would never see the span. Binding the ids
// onto the logger at scope entry is the simplest fix without rewriting
// every call site.
//
// The format of trace_id ("0123...32-hex") and span_id ("0123...16-hex")
// matches Grafana's Tempo derived-fields configuration, so a Loki log line
// with these fields renders a one-click "open in Tempo" button.
func LoggerWithSpan(ctx context.Context, logger *slog.Logger) *slog.Logger {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.IsValid() {
		return logger
	}
	return logger.With(
		"trace_id", sc.TraceID().String(),
		"span_id", sc.SpanID().String(),
	)
}

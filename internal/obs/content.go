package obs

import (
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// contentEnabled is a process-wide toggle for recording content-bearing span
// events (LLM request/response, RAG query text, etc). Default off so the
// helper stays cheap in production; dev turns it on via configuration.
var contentEnabled atomic.Bool

// SetContentEnabled flips the content-recording toggle. Called from
// InitTracing based on TracingConfig.TraceContent.
func SetContentEnabled(v bool) { contentEnabled.Store(v) }

// ContentEnabled reports the current state of the toggle. Exposed for tests
// and for callers that want to cheaply skip assembling a payload when
// recording is disabled.
func ContentEnabled() bool { return contentEnabled.Load() }

// RecordContent adds a span event carrying a content payload when the
// content toggle is enabled; it is a no-op otherwise. The body is stored
// under the reserved attribute key "body" — extra attributes are appended
// alongside but must not shadow this key.
func RecordContent(span trace.Span, name, body string, extra ...attribute.KeyValue) {
	if !contentEnabled.Load() {
		return
	}
	attrs := make([]attribute.KeyValue, 0, len(extra)+1)
	attrs = append(attrs, attribute.String("body", body))
	attrs = append(attrs, extra...)
	span.AddEvent(name, trace.WithAttributes(attrs...))
}

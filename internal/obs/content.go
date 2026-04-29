package obs

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
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

// dataURLBase64Re matches a base64 data URL embedded as a string fragment in
// any text payload (typically inside JSON-marshalled OR request/response).
//
// Anatomy: "data:" mime ";base64," payload.
//   - mime: standard charset for IANA media types (RFC 2045 token chars).
//     "+" appears in mime subtypes like image/svg+xml; "-" in vnd.* and
//     model-specific subtypes. "." in subtypes like vnd.ms-excel.
//   - payload: standard base64 alphabet plus "=" padding. Lower bound 32
//     guards against false matches on very short literal strings; legitimate
//     image/PDF payloads start in the kilobytes.
//
// The "32+" lower bound is a defensive choice — any real base64 file payload
// is far longer. False positives on short strings would corrupt the trace
// content for no size benefit.
var dataURLBase64Re = regexp.MustCompile(`data:([a-zA-Z0-9./+\-]+);base64,([A-Za-z0-9+/=]{32,})`)

// RedactBase64Payloads replaces every "data:<mime>;base64,<payload>" fragment
// in body with "redacted:sha256:<hex>:<mime>:<size>", where <hex> is the
// sha256 of the decoded bytes (matching artifacts.content_hash) and <size>
// is the decoded byte count. The shape is deliberately distinct from a real
// data URL so the regex below can't loop on already-redacted output.
//
// Designed for use inside RecordContent payloads where the body is typically
// a marshalled JSON request/response carrying multimodal file inputs. With
// 1-5 MB images per OR call and 3-4 OR calls per turn, leaving raw base64
// in trace events drives per-trace size beyond Tempo's max_bytes_per_trace
// limit; redacting brings a media-heavy turn from ~30 MB down to ~1 MB.
//
// Replay reconstructs the original FilePart by parsing this placeholder,
// looking up the artifact by content_hash, and reading bytes from the
// snapshotted artifact storage. Validity checks (sha256 + size) catch any
// drift between trace and snapshot.
//
// Hot-path note: a no-match body returns in O(len(body)) with zero allocs.
// On match we run the regex exactly once via FindAllStringSubmatchIndex and
// stream the result into a byte buffer — avoiding the double-regex cost of
// ReplaceAllStringFunc + per-match FindStringSubmatch.
func RedactBase64Payloads(body string) string {
	matches := dataURLBase64Re.FindAllStringSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return body
	}
	var b strings.Builder
	b.Grow(len(body))
	cursor := 0
	for _, m := range matches {
		// m layout: [matchStart, matchEnd, mimeStart, mimeEnd, payloadStart, payloadEnd]
		matchStart, matchEnd := m[0], m[1]
		mimeStart, mimeEnd := m[2], m[3]
		payloadStart, payloadEnd := m[4], m[5]
		b.WriteString(body[cursor:matchStart])

		decoded, err := base64.StdEncoding.DecodeString(body[payloadStart:payloadEnd])
		if err != nil {
			// Truncated or non-standard base64 — leave original so we
			// don't corrupt the body. RecordContent caller will see real
			// data and decide; surfaces as a debug clue rather than a
			// silent drop.
			b.WriteString(body[matchStart:matchEnd])
			cursor = matchEnd
			continue
		}
		sum := sha256.Sum256(decoded)
		fmt.Fprintf(&b, "redacted:sha256:%s:%s:%d",
			hex.EncodeToString(sum[:]),
			body[mimeStart:mimeEnd],
			len(decoded))
		cursor = matchEnd
	}
	b.WriteString(body[cursor:])
	return b.String()
}

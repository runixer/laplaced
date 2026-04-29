package obs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
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

// makeBase64DataURL produces a deterministic data URL with mime + given
// raw bytes. Used to assemble fixtures whose hash and size we can recompute
// independently from the helper under test.
func makeBase64DataURL(t *testing.T, mime string, raw []byte) (url string, hashHex string, size int) {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(raw)
	url = fmt.Sprintf("data:%s;base64,%s", mime, encoded)
	sum := sha256.Sum256(raw)
	return url, hex.EncodeToString(sum[:]), len(raw)
}

func TestRedactBase64Payloads_NoMatch_ReturnsOriginal(t *testing.T) {
	cases := []string{
		"",
		"plain text body",
		`{"messages":[{"role":"user","content":"hello"}]}`,
		// short base64-looking payload (< 32 chars) must NOT match — guards
		// against false positives on inline tokens that look base64-ish.
		"data:text/plain;base64,aGVsbG8=",
		// already-redacted form must NOT match — idempotency guard.
		"redacted:sha256:abcdef:image/png:1234",
	}
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c, "\n", " "), func(t *testing.T) {
			assert.Equal(t, c, RedactBase64Payloads(c))
		})
	}
}

func TestRedactBase64Payloads_SingleImage(t *testing.T) {
	// Big enough payload (>32 base64 chars) for the lower bound to match.
	raw := []byte(strings.Repeat("PNGDATA-", 10)) // 80 bytes — encodes to 108 base64 chars
	url, want, size := makeBase64DataURL(t, "image/png", raw)
	body := fmt.Sprintf(`{"file_data":"%s","other":"text"}`, url)

	got := RedactBase64Payloads(body)

	expected := fmt.Sprintf("redacted:sha256:%s:image/png:%d", want, size)
	assert.Contains(t, got, expected)
	assert.NotContains(t, got, ";base64,", "raw base64 must be gone")
	assert.Contains(t, got, `"other":"text"`, "non-media body content preserved")
}

func TestRedactBase64Payloads_MultipleImages(t *testing.T) {
	rawA := []byte(strings.Repeat("A", 100))
	rawB := []byte(strings.Repeat("B", 200))
	urlA, hashA, sizeA := makeBase64DataURL(t, "image/png", rawA)
	urlB, hashB, sizeB := makeBase64DataURL(t, "image/jpeg", rawB)

	body := fmt.Sprintf(`{"messages":[{"file":"%s"},{"file":"%s"}]}`, urlA, urlB)

	got := RedactBase64Payloads(body)

	assert.Contains(t, got, fmt.Sprintf("redacted:sha256:%s:image/png:%d", hashA, sizeA))
	assert.Contains(t, got, fmt.Sprintf("redacted:sha256:%s:image/jpeg:%d", hashB, sizeB))
	// Independent counts — both placeholders, no leftover base64 anywhere.
	assert.NotContains(t, got, ";base64,")
}

func TestRedactBase64Payloads_Idempotent(t *testing.T) {
	raw := []byte(strings.Repeat("X", 64))
	url, _, _ := makeBase64DataURL(t, "application/pdf", raw)
	body := fmt.Sprintf(`{"file":"%s"}`, url)

	once := RedactBase64Payloads(body)
	twice := RedactBase64Payloads(once)

	assert.Equal(t, once, twice, "running redact on already-redacted body must be a no-op")
}

func TestRedactBase64Payloads_VariedMimes(t *testing.T) {
	mimes := []string{
		"image/png",
		"image/jpeg",
		"image/webp",
		"image/svg+xml", // "+" in subtype
		"application/pdf",
		"audio/ogg",
		"audio/mpeg",
		"video/mp4",
		"application/vnd.ms-excel", // "." in subtype
	}
	raw := []byte(strings.Repeat("Z", 64))
	for _, mime := range mimes {
		t.Run(mime, func(t *testing.T) {
			url, hash, size := makeBase64DataURL(t, mime, raw)
			body := fmt.Sprintf(`"%s"`, url)
			got := RedactBase64Payloads(body)
			assert.Contains(t, got, fmt.Sprintf("redacted:sha256:%s:%s:%d", hash, mime, size))
		})
	}
}

func TestRedactBase64Payloads_LargePayload_PerfSanity(t *testing.T) {
	if testing.Short() {
		t.Skip("perf-sanity skipped in -short")
	}
	// 5 MB raw → ~6.7 MB base64 chars per fragment, with 5 fragments ≈ 33 MB
	// of input — same shape as a realistic media-heavy turn body.
	raw := make([]byte, 5*1024*1024)
	for i := range raw {
		raw[i] = byte(i)
	}
	url, _, _ := makeBase64DataURL(t, "image/png", raw)
	var b strings.Builder
	b.WriteString(`{"messages":[`)
	for i := 0; i < 5; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"file":"%s"}`, url)
	}
	b.WriteString("]}")
	body := b.String()

	got := RedactBase64Payloads(body)

	// Expect dramatic shrinkage — placeholder is ~120 bytes vs ~6.7 MB.
	assert.Less(t, len(got), len(body)/100, "redacted body should be <1%% of original")
	assert.NotContains(t, got, ";base64,")
}

func TestRedactBase64Payloads_HashMatchesArtifactStorage(t *testing.T) {
	// Replay relies on the placeholder hash matching artifacts.content_hash,
	// which is sha256 of raw bytes. Pin this contract with an explicit test.
	raw := []byte("the quick brown fox jumps over the lazy dog")
	url, _, _ := makeBase64DataURL(t, "text/plain", raw)
	body := fmt.Sprintf(`{"file":"%s"}`, url)

	got := RedactBase64Payloads(body)

	expectedSum := sha256.Sum256(raw)
	expectedHash := hex.EncodeToString(expectedSum[:])
	assert.Contains(t, got, expectedHash, "placeholder hash must equal sha256 of decoded bytes")
}

func TestRedactBase64Payloads_MalformedBase64_LeavesOriginal(t *testing.T) {
	// Defensive: if the captured fragment isn't valid base64 (truncation
	// during ingestion or a content corruption), don't poison the body —
	// keep the original so a human can still see what was there.
	body := `{"file":"data:image/png;base64,!!!!INVALID-BASE64-WITH-MORE-THAN-32-CHARS!!!!"}`

	got := RedactBase64Payloads(body)

	// Regex won't match because "!" isn't in the base64 alphabet — so the
	// payload chars get scanned but the match fails entirely. Body returns
	// unchanged. This is the desired property; verify explicitly.
	assert.Equal(t, body, got)
}

func TestRedactBase64Payloads_ReplayContract(t *testing.T) {
	// The format is the contract between bot-side redact and replay-side
	// reconstructor. Pin parser shape here so a future format change surfaces
	// as a failed test in this file (and forces a coordinated update).
	parseRe := regexp.MustCompile(`redacted:sha256:([a-f0-9]{64}):([^:]+):(\d+)`)

	raw := []byte("hello world hello world hello world hello world hello world")
	url, hash, size := makeBase64DataURL(t, "text/plain", raw)
	body := fmt.Sprintf(`"%s"`, url)
	got := RedactBase64Payloads(body)

	m := parseRe.FindStringSubmatch(got)
	require.Len(t, m, 4, "placeholder must match the published parse regex")
	assert.Equal(t, hash, m[1])
	assert.Equal(t, "text/plain", m[2])
	assert.Equal(t, fmt.Sprintf("%d", size), m[3])
}

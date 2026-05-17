package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/openrouter"
)

func makeFilePart(name, mime string, raw []byte) openrouter.FilePart {
	url := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
	return openrouter.FilePart{
		Type: "file",
		File: openrouter.File{FileName: name, FileData: url},
	}
}

func TestFormatMediaParts_EmptyInput(t *testing.T) {
	assert.Equal(t, "", FormatMediaParts(nil, ""))
	assert.Equal(t, "", FormatMediaParts([]interface{}{}, ""))
}

func TestFormatMediaParts_SkipsNonFileParts(t *testing.T) {
	parts := []interface{}{
		openrouter.TextPart{Type: "text", Text: "hello"},
		"not a part",
	}
	assert.Equal(t, "", FormatMediaParts(parts, ""))
}

func TestFormatMediaParts_HashMatchesContentHash(t *testing.T) {
	// Pin the contract: hash in event body must equal sha256 of decoded
	// bytes — same shape as artifacts.content_hash. Replay uses this for
	// snapshot DB lookup.
	raw := []byte("the quick brown fox jumps over the lazy dog")
	fp := makeFilePart("note.txt", "text/plain", raw)

	got := FormatMediaParts([]interface{}{fp}, "")

	var entries []MediaEntry
	require.NoError(t, json.Unmarshal([]byte(got), &entries))
	require.Len(t, entries, 1)

	expectedSum := sha256.Sum256(raw)
	assert.Equal(t, hex.EncodeToString(expectedSum[:]), entries[0].SHA256)
	assert.Equal(t, len(raw), entries[0].SizeBytes)
	assert.Equal(t, "text/plain", entries[0].Mime)
	assert.Equal(t, "note.txt", entries[0].Filename)
}

func TestFormatMediaParts_AppliesSourceLabel(t *testing.T) {
	fp := makeFilePart("a.png", "image/png", []byte(strings.Repeat("X", 100)))
	got := FormatMediaParts([]interface{}{fp}, "current_message")

	var entries []MediaEntry
	require.NoError(t, json.Unmarshal([]byte(got), &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, "current_message", entries[0].Source)
}

func TestFormatMediaParts_OmitsEmptySource(t *testing.T) {
	// json:"source,omitempty" — empty source must not appear in the
	// emitted JSON to keep reranker bodies (which never set source) compact.
	fp := makeFilePart("a.png", "image/png", []byte(strings.Repeat("X", 100)))
	got := FormatMediaParts([]interface{}{fp}, "")

	assert.NotContains(t, got, `"source":`)
}

func TestFormatMediaPartsWithSources_MixedOrigins(t *testing.T) {
	// Replicates the laplace use case: one current-message photo + two
	// reranker-selected artifacts in the same multimodal context.
	current := makeFilePart("today.jpg", "image/jpeg", []byte(strings.Repeat("A", 100)))
	rrA := makeFilePart("past1.png", "image/png", []byte(strings.Repeat("B", 200)))
	rrB := makeFilePart("past2.pdf", "application/pdf", []byte(strings.Repeat("C", 300)))

	parts := []MediaPartWithSource{
		{Part: current, Source: "current_message"},
		{Part: rrA, Source: "reranker_selected"},
		{Part: rrB, Source: "reranker_selected"},
	}
	got := FormatMediaPartsWithSources(parts)

	var entries []MediaEntry
	require.NoError(t, json.Unmarshal([]byte(got), &entries))
	require.Len(t, entries, 3)
	assert.Equal(t, "current_message", entries[0].Source)
	assert.Equal(t, "reranker_selected", entries[1].Source)
	assert.Equal(t, "reranker_selected", entries[2].Source)

	// Sizes should reflect the original raw byte counts.
	assert.Equal(t, 100, entries[0].SizeBytes)
	assert.Equal(t, 200, entries[1].SizeBytes)
	assert.Equal(t, 300, entries[2].SizeBytes)
}

func TestFormatMediaParts_MalformedDataURL_RecordsZeroSize(t *testing.T) {
	// Non-base64 payload: still record the FilePart (we know name/mime
	// from the part itself), but size_bytes=0 and sha256 omitted. Replay
	// can't reconstruct it, but at least the trace shows the part existed.
	fp := openrouter.FilePart{
		File: openrouter.File{
			FileName: "broken.png",
			FileData: "data:image/png;base64,!!!INVALID!!!",
		},
	}
	got := FormatMediaParts([]interface{}{fp}, "")

	var entries []MediaEntry
	require.NoError(t, json.Unmarshal([]byte(got), &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, "broken.png", entries[0].Filename)
	assert.Equal(t, "image/png", entries[0].Mime)
	assert.Equal(t, 0, entries[0].SizeBytes)
	assert.Empty(t, entries[0].SHA256)
}

func TestFormatMediaParts_MultipleFiles(t *testing.T) {
	parts := make([]interface{}, 5)
	for i := range parts {
		raw := []byte(fmt.Sprintf("file-%d-content-padding", i))
		parts[i] = makeFilePart(fmt.Sprintf("f%d.bin", i), "application/octet-stream", raw)
	}
	got := FormatMediaParts(parts, "")

	var entries []MediaEntry
	require.NoError(t, json.Unmarshal([]byte(got), &entries))
	require.Len(t, entries, 5)
	for i, e := range entries {
		assert.Equal(t, fmt.Sprintf("f%d.bin", i), e.Filename)
	}
}

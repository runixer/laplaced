package obs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTextPreview(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		maxLen      int
		wantPreview string
	}{
		{
			name:        "short ASCII passes through",
			input:       "hello world",
			maxLen:      160,
			wantPreview: "hello world",
		},
		{
			name:        "newlines collapse to single space",
			input:       "line one\nline two\rline three\tlast",
			maxLen:      160,
			wantPreview: "line one line two line three last",
		},
		{
			name:        "runs of whitespace collapse",
			input:       "a    b\n\n\nc",
			maxLen:      160,
			wantPreview: "a b c",
		},
		{
			name:        "leading and trailing whitespace trimmed",
			input:       "   leading   ",
			maxLen:      160,
			wantPreview: "leading",
		},
		{
			name:        "exact boundary not truncated",
			input:       strings.Repeat("a", 10),
			maxLen:      10,
			wantPreview: strings.Repeat("a", 10),
		},
		{
			name:        "over boundary truncated with ellipsis",
			input:       strings.Repeat("a", 11),
			maxLen:      10,
			wantPreview: strings.Repeat("a", 10) + "…",
		},
		{
			name:        "default maxLen used when zero",
			input:       strings.Repeat("a", DefaultPreviewLen+5),
			maxLen:      0,
			wantPreview: strings.Repeat("a", DefaultPreviewLen) + "…",
		},
		{
			name:        "default maxLen used when negative",
			input:       strings.Repeat("a", DefaultPreviewLen+5),
			maxLen:      -1,
			wantPreview: strings.Repeat("a", DefaultPreviewLen) + "…",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preview, hash := TextPreview(tt.input, tt.maxLen)
			assert.Equal(t, tt.wantPreview, preview)
			assert.Len(t, hash, 16, "hash should be 16 hex chars (sha256[:8])")
		})
	}
}

func TestTextPreview_UTF8BoundarySafe(t *testing.T) {
	// "ё" is 2 bytes in UTF-8; truncating at an odd byte would split it.
	input := strings.Repeat("ё", 100)
	preview, _ := TextPreview(input, 5) // 5 bytes = 2.5 ё's
	// The preview must end with valid UTF-8, never a half-rune.
	assert.True(t, len(preview) <= 5+len("…"))
	// Count complete "ё" runes in the preview (excluding the ellipsis).
	body := strings.TrimSuffix(preview, "…")
	for _, r := range body {
		assert.NotEqual(t, '�', r, "no replacement chars allowed — truncation broke a rune")
	}
}

func TestTextPreview_HashIsDeterministicOnRawInput(t *testing.T) {
	// Hash must be over the original input, NOT the truncated preview.
	// Two long strings with the same first 10 chars must hash differently.
	a := "hello world: alpha branch"
	b := "hello world: beta branch"

	_, hashA := TextPreview(a, 10)
	_, hashB := TextPreview(b, 10)
	assert.NotEqual(t, hashA, hashB,
		"hash must distinguish inputs that share a truncated prefix")

	// Same input produces same hash.
	_, hashA2 := TextPreview(a, 10)
	assert.Equal(t, hashA, hashA2)
}

func TestTextPreview_RedactsBase64DataURL(t *testing.T) {
	// A multimodal user query may include an inline image data URL via the
	// llm_parts marshaling. The preview must not leak the raw payload.
	payload := strings.Repeat("A", 100) // valid base64 alphabet, long enough to match the redaction regex
	input := "Please describe: data:image/png;base64," + payload + " — what's in it?"

	preview, _ := TextPreview(input, DefaultPreviewLen)
	assert.NotContains(t, preview, payload[:32],
		"raw base64 payload must be redacted out of the preview")
	assert.Contains(t, preview, "redacted:sha256:",
		"redaction placeholder must be present")
}

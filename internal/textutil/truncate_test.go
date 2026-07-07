package textutil

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		max    int
		suffix string
		want   string
	}{
		{"shorter than max", "hello", 10, "...", "hello"},
		{"exactly max", "hello", 5, "...", "hello"},
		{"truncated ascii", "hello world", 5, "...", "hello..."},
		{"empty string", "", 5, "...", ""},
		{"cyrillic truncated on rune boundary", "привет мир", 6, "...", "привет..."},
		{"cyrillic shorter than max", "привет", 10, "...", "привет"},
		{"emoji truncated", "🎤🎤🎤🎤", 2, "…", "🎤🎤…"},
		{"empty suffix", "hello world", 5, "", "hello"},
		{"zero max", "hello", 0, "...", "..."},
		{"negative max", "hello", -1, "...", "..."},
		{"exactly max multibyte", "привет", 6, "...", "привет"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateRunes(tt.s, tt.max, tt.suffix)
			assert.Equal(t, tt.want, got)
			assert.True(t, utf8.ValidString(got), "result must remain valid UTF-8")
		})
	}
}

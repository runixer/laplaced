package markdown

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpoiler(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Simple spoiler",
			input:    "||secret||",
			expected: "<tg-spoiler>secret</tg-spoiler>",
		},
		{
			name:     "Spoiler with spaces",
			input:    "||secret text||",
			expected: "<tg-spoiler>secret text</tg-spoiler>",
		},
		{
			name:     "Multiple spoilers",
			input:    "||first|| and ||second||",
			expected: "<tg-spoiler>first</tg-spoiler> and <tg-spoiler>second</tg-spoiler>",
		},
		{
			name:     "Spoiler in sentence",
			input:    "This is a ||spoiler|| in text",
			expected: "This is a <tg-spoiler>spoiler</tg-spoiler> in text",
		},
		{
			name:     "Unclosed spoiler",
			input:    "||unclosed",
			expected: "||unclosed",
		},
		{
			name:     "Single pipe",
			input:    "| not a spoiler",
			expected: "| not a spoiler",
		},
		{
			name:     "Empty spoiler",
			input:    "||||",
			expected: "<tg-spoiler></tg-spoiler>",
		},
		{
			name:     "Spoiler with bold",
			input:    "||**bold secret**||",
			expected: "<tg-spoiler>**bold secret**</tg-spoiler>",
		},
		{
			name:     "Spoiler with italic",
			input:    "||*italic secret*||",
			expected: "<tg-spoiler>*italic secret*</tg-spoiler>",
		},
		{
			name:     "Spoiler with code",
			input:    "||`code secret`||",
			expected: "<tg-spoiler>`code secret`</tg-spoiler>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ToHTML(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

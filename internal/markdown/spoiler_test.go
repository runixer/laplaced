package markdown

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/text"
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

func TestSpoilerNodeDump(t *testing.T) {
	node := NewSpoilerNode()
	source := []byte("test content")

	// Dump should not panic - it's a debugging function
	assert.NotPanics(t, func() {
		dump := node.Dump(source)
		require.NotNil(t, dump)
	})

	// Test node properties
	assert.Equal(t, KindSpoiler, node.Kind())
	assert.NotNil(t, node)
}

func TestSpoilerNodeKind(t *testing.T) {
	node := NewSpoilerNode()
	assert.Equal(t, KindSpoiler, node.Kind(), "SpoilerNode should have KindSpoiler as its kind")
}

func TestNewSpoilerNode(t *testing.T) {
	node := NewSpoilerNode()
	assert.NotNil(t, node)
	assert.IsType(t, &SpoilerNode{}, node)
}

func TestSpoilerNodeBaseInline(t *testing.T) {
	node := NewSpoilerNode()

	// SpoilerNode should be able to have children
	textNode := ast.NewText(text.NewSingleLineValueFromIndex(text.NewIndex(0, 5), text.IdentityDecoder))
	node.AppendChild(textNode)

	assert.Equal(t, node.FirstChild(), textNode)
}

func TestSpoilerParserTrigger(t *testing.T) {
	parser := &spoilerParser{}

	// Test trigger characters
	triggers := parser.Trigger()
	assert.Contains(t, triggers, byte('|'), "Trigger should include '|' character")
}

package bot

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitByDelimiter(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "No delimiter",
			input:    "Simple text without any delimiter",
			expected: []string{"Simple text without any delimiter"},
		},
		{
			name:     "Single split",
			input:    "Part one###SPLIT###Part two",
			expected: []string{"Part one", "Part two"},
		},
		{
			name:     "Multiple splits",
			input:    "Part one###SPLIT###Part two###SPLIT###Part three",
			expected: []string{"Part one", "Part two", "Part three"},
		},
		{
			name:     "Delimiter at start (empty first part ignored)",
			input:    "###SPLIT###Part one###SPLIT###Part two",
			expected: []string{"Part one", "Part two"},
		},
		{
			name:     "Delimiter at end (empty last part ignored)",
			input:    "Part one###SPLIT###Part two###SPLIT###",
			expected: []string{"Part one", "Part two"},
		},
		{
			name:     "Consecutive delimiters (empty middle parts ignored)",
			input:    "Part one###SPLIT######SPLIT###Part two",
			expected: []string{"Part one", "Part two"},
		},
		{
			name:     "Delimiter inside code block (NOT split)",
			input:    "Text before\n```\ncode###SPLIT###more code\n```\nText after",
			expected: []string{"Text before\n```\ncode###SPLIT###more code\n```\nText after"},
		},
		{
			name:     "Delimiter outside code block but near it",
			input:    "Part one\n```\ncode\n```\n###SPLIT###Part two",
			expected: []string{"Part one\n```\ncode\n```", "Part two"},
		},
		{
			name:     "Mixed: some inside, some outside code blocks",
			input:    "Part one###SPLIT###```\n###SPLIT###\n```###SPLIT###Part three",
			expected: []string{"Part one", "```\n###SPLIT###\n```", "Part three"},
		},
		{
			name:     "Whitespace around delimiter trimmed",
			input:    "Part one  \n###SPLIT###\n  Part two",
			expected: []string{"Part one", "Part two"},
		},
		{
			name:     "Only delimiter (returns original)",
			input:    "###SPLIT###",
			expected: []string{"###SPLIT###"},
		},
		{
			name:     "Only whitespace after split (ignored)",
			input:    "Part one###SPLIT###   \n   ###SPLIT###Part three",
			expected: []string{"Part one", "Part three"},
		},
		{
			name:     "Real-world example: greeting + copyable content",
			input:    "С удовольствием помогу с поздравлением!\n###SPLIT###Дорогая Мария! Поздравляю тебя с днём рождения! Желаю счастья и здоровья!",
			expected: []string{"С удовольствием помогу с поздравлением!", "Дорогая Мария! Поздравляю тебя с днём рождения! Желаю счастья и здоровья!"},
		},
		{
			name:     "Empty string",
			input:    "",
			expected: []string{""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitByDelimiter(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

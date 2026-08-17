package extractor

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseExtractionResult_RepairsOnlyRAGHintDecorations(t *testing.T) {
	content := `{
		"summary": "A valid summary",
		"keywords": ["one", "two"],
		"entities": ["entity"],
		"rag_hints": [
			- "dash",
			* "star",
			1. "numbered",
			__q4: "pseudo key",
			вопрос5: "unicode key"
		]
	}`

	result, repaired, dropped, err := parseExtractionResult(content, false)
	require.NoError(t, err)
	assert.True(t, repaired)
	assert.False(t, dropped)
	assert.Equal(t, "A valid summary", result.Summary)
	assert.Equal(t, []string{"dash", "star", "numbered", "pseudo key", "unicode key"}, result.RAGHints)
}

func TestParseExtractionResult_ToleratesRAGHintShapes(t *testing.T) {
	tests := []struct {
		name     string
		rawHints string
		expected []string
	}{
		{name: "flat", rawHints: `["one", "two"]`, expected: []string{"one", "two"}},
		{name: "nested", rawHints: `[["one"], ["two", ["three"]]]`, expected: []string{"one", "two", "three"}},
		{name: "single string", rawHints: `"one"`, expected: []string{"one"}},
		{name: "null", rawHints: `null`, expected: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := `{"summary":"summary","keywords":[],"entities":[],"rag_hints":` + tt.rawHints + `}`
			result, repaired, dropped, err := parseExtractionResult(content, false)
			require.NoError(t, err)
			assert.False(t, repaired)
			assert.False(t, dropped)
			assert.Equal(t, tt.expected, result.RAGHints)
		})
	}
}

func TestRepairRAGHintDecorations_DoesNotRewriteValidValues(t *testing.T) {
	tests := []string{
		`{"summary":"literal rag_hints: [ - invalid ]","keywords":[],"entities":[],"rag_hints":["- keep", "* keep", "1. keep", "__q1: keep"]}`,
		`{"summary":"summary","keywords":[],"entities":[],"rag_hints":[-1]}`,
		`{"summary":"summary","keywords":[],"entities":[],"rag_hints":["escaped comma, and quote: \"-"]}`,
		`{"summary":"summary","keywords":[],"entities":[],"rag_hints":[{"valid":"value", invalid: "not an array item"}]}`,
	}

	for _, content := range tests {
		repaired, changed := repairRAGHintDecorations(content)
		assert.False(t, changed)
		assert.Equal(t, content, repaired)
	}

	_, repaired, dropped, err := parseExtractionResult(tests[1], false)
	require.Error(t, err)
	assert.False(t, repaired)
	assert.False(t, dropped)
}

func TestParseExtractionResult_DropsOnlyMalformedHintsOnTerminalAttempt(t *testing.T) {
	content := `{
		"summary": "Preserved summary",
		"keywords": ["preserved"],
		"entities": ["entity"],
		"rag_hints": {"unexpected": "object"}
	}`

	_, _, _, err := parseExtractionResult(content, false)
	require.Error(t, err, "a non-terminal attempt must still fail and be retried")

	result, repaired, dropped, err := parseExtractionResult(content, true)
	require.NoError(t, err)
	assert.True(t, repaired)
	assert.True(t, dropped)
	assert.Equal(t, "Preserved summary", result.Summary)
	assert.Equal(t, []string{"preserved"}, result.Keywords)
	assert.Equal(t, []string{"entity"}, result.Entities)
	assert.Empty(t, result.RAGHints)
}

func TestParseExtractionResult_TerminalDropRejectsOtherMalformedFields(t *testing.T) {
	content := `{
		"summary": "Preserved summary",
		"keywords": [not_json],
		"entities": ["entity"],
		"rag_hints": {"unexpected": "object"}
	}`

	_, _, dropped, err := parseExtractionResult(content, true)
	require.Error(t, err)
	assert.False(t, dropped)
}

func TestParseExtractionResult_TerminalDropDoesNotMistakeStringValueForKey(t *testing.T) {
	content := `{
		"summary": "summary",
		"keywords": "rag_hints": ["not the real field"],
		"entities": []
	}`

	_, _, dropped, err := parseExtractionResult(content, true)
	require.Error(t, err)
	assert.False(t, dropped)
}

func TestParseExtractionResult_TerminalDropRejectsTruncatedJSON(t *testing.T) {
	content := `{"summary":"summary","keywords":[],"entities":[],"rag_hints":["truncated"`

	_, _, dropped, err := parseExtractionResult(content, true)
	require.Error(t, err)
	assert.False(t, dropped)
}

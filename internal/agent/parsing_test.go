package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "clean object unchanged",
			input:    `{"topics": [1, 2]}`,
			expected: `{"topics": [1, 2]}`,
		},
		{
			name:     "clean array unchanged",
			input:    `[1, 2, 3]`,
			expected: `[1, 2, 3]`,
		},
		{
			name:     "markdown fenced object",
			input:    "```json\n{\"key\": \"value\"}\n```",
			expected: `{"key": "value"}`,
		},
		{
			name:     "markdown fenced array",
			input:    "```json\n[1, 2]\n```",
			expected: `[1, 2]`,
		},
		{
			name:     "preamble text before object",
			input:    "Here is the result: {\"key\": 1}",
			expected: `{"key": 1}`,
		},
		{
			name: "stray prefix char before object",
			// Production regression (merger, 2026-06-10): single garbage byte
			// before an otherwise valid JSON object.
			input:    `H{"should_merge": true, "new_summary": "x"}`,
			expected: `{"should_merge": true, "new_summary": "x"}`,
		},
		{
			name: "trailing garbage after object",
			// Production regression (extractor): valid object followed by an
			// extra closing brace.
			input:    `{"summary": "s", "keywords": ["k"]}}`,
			expected: `{"summary": "s", "keywords": ["k"]}`,
		},
		{
			name:     "text after json ignored",
			input:    `{"a": 1} some explanation`,
			expected: `{"a": 1}`,
		},
		{
			name:     "nested objects",
			input:    `x{"a": {"b": {"c": 1}}}y`,
			expected: `{"a": {"b": {"c": 1}}}`,
		},
		{
			name:     "braces inside strings",
			input:    `note: {"text": "value with {braces} and \"quotes\" inside"}`,
			expected: `{"text": "value with {braces} and \"quotes\" inside"}`,
		},
		{
			name:     "array preferred when first",
			input:    `[{"id": 1}] {"other": 2}`,
			expected: `[{"id": 1}]`,
		},
		{
			name:     "no json returns content unchanged",
			input:    "no json here",
			expected: "no json here",
		},
		{
			name:     "unbalanced json returns content unchanged",
			input:    `{"truncated": "value`,
			expected: `{"truncated": "value`,
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ExtractJSON(tt.input))
		})
	}
}

func TestUnmarshalLenient(t *testing.T) {
	type payload struct {
		ShouldMerge bool   `json:"should_merge"`
		NewSummary  string `json:"new_summary"`
	}

	t.Run("clean content not repaired", func(t *testing.T) {
		var p payload
		repaired, err := UnmarshalLenient(`{"should_merge": true, "new_summary": "s"}`, &p)
		require.NoError(t, err)
		assert.False(t, repaired)
		assert.True(t, p.ShouldMerge)
	})

	t.Run("prefixed content repaired", func(t *testing.T) {
		var p payload
		repaired, err := UnmarshalLenient(`H{"should_merge": true, "new_summary": "s"}`, &p)
		require.NoError(t, err)
		assert.True(t, repaired)
		assert.True(t, p.ShouldMerge)
		assert.Equal(t, "s", p.NewSummary)
	})

	t.Run("fenced content repaired", func(t *testing.T) {
		var p payload
		repaired, err := UnmarshalLenient("```json\n{\"should_merge\": false}\n```", &p)
		require.NoError(t, err)
		assert.True(t, repaired)
		assert.False(t, p.ShouldMerge)
	})

	t.Run("empty content errors", func(t *testing.T) {
		var p payload
		_, err := UnmarshalLenient("", &p)
		assert.Error(t, err)
	})

	t.Run("invalid json errors", func(t *testing.T) {
		var p payload
		_, err := UnmarshalLenient("not json at all", &p)
		assert.Error(t, err)
	})
}

func TestFlexID_UnmarshalJSON(t *testing.T) {
	type doc struct {
		FactID FlexID `json:"fact_id"`
	}

	tests := []struct {
		name    string
		input   string
		want    FlexID
		wantErr bool
	}{
		{"prefixed string", `{"fact_id": "Fact:1522"}`, "Fact:1522", false},
		{"plain numeric string", `{"fact_id": "1522"}`, "1522", false},
		// Production regression (archivist, 2026-06-07): the model emitted
		// the id as a JSON number and the whole extraction was discarded.
		{"json number", `{"fact_id": 5215}`, "5215", false},
		{"large number stays exact", `{"fact_id": 9007199254740993}`, "9007199254740993", false},
		{"float number kept verbatim", `{"fact_id": 5.0}`, "5.0", false},
		{"null becomes empty", `{"fact_id": null}`, "", false},
		{"absent stays empty", `{}`, "", false},
		{"boolean rejected", `{"fact_id": true}`, "", true},
		{"object rejected", `{"fact_id": {"x": 1}}`, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d doc
			err := json.Unmarshal([]byte(tt.input), &d)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, d.FactID)
		})
	}
}

func TestFlexID_MarshalsAsString(t *testing.T) {
	out, err := json.Marshal(struct {
		ID FlexID `json:"id"`
	}{ID: "Fact:7"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"id": "Fact:7"}`, string(out))
}

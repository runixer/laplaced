package laplace

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeLLMResponse_NoSanitizationNeeded(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"normal text", "Hello, this is a normal response."},
		{"with markdown", "# Header\n\nSome **bold** text."},
		{"single json block", "Here's some JSON:\n```json\n{\"key\": \"value\"}\n```"},
		{"two json blocks", "First:\n```json\n{\"a\": 1}\n```\nSecond:\n```json\n{\"b\": 2}\n```"},
		{"empty string", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, sanitized := SanitizeLLMResponse(tt.input)
			assert.Equal(t, tt.input, result)
			assert.False(t, sanitized)
		})
	}
}

func TestSanitizeLLMResponse_TruncatesOnHallucinationTags(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			"tool_code tag",
			"Normal response.</tool_code> garbage after",
			"Normal response.",
		},
		{
			"tool_call tag",
			"Good text</tool_call>\n\nmore garbage",
			"Good text",
		},
		{
			"end of sequence tag",
			"Valid content</s> invalid content",
			"Valid content",
		},
		{
			"endoftext tag",
			"Response here<|endoftext|>more text",
			"Response here",
		},
		{
			"end tag",
			"Some text<|end|>garbage",
			"Some text",
		},
		{
			"tag in middle of text",
			"First part.\n\nSecond part.</tool_code></s>\n```json\n{}\n```",
			"First part.\n\nSecond part.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, sanitized := SanitizeLLMResponse(tt.input)
			assert.Equal(t, tt.expected, result)
			assert.True(t, sanitized)
		})
	}
}

func TestSanitizeLLMResponse_TruncatesOnConsecutiveJSONBlocks(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			"three consecutive json blocks",
			"Good response.\n\n```json\n{\"a\": 1}\n```\n```json\n{\"b\": 2}\n```\n```json\n{\"c\": 3}\n```",
			"Good response.",
		},
		{
			"many json blocks",
			"Normal text.\n```json\n{\"q\": \"test1\"}\n```\n```json\n{\"q\": \"test2\"}\n```\n```json\n{\"q\": \"test3\"}\n```\n```json\n{\"q\": \"test4\"}\n```\n```json\n{\"q\": \"test5\"}\n```",
			"Normal text.",
		},
		{
			"real world example",
			"Response here.\n\n```json\n{\"query\": \"search 1\"}\n```\n\n```json\n{\"query\": \"search 2\"}\n```\n\n```json\n{\"query\": \"search 3\"}\n```\n\n```json\n{\"query\": \"search 4\"}\n```",
			"Response here.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, sanitized := SanitizeLLMResponse(tt.input)
			assert.Equal(t, tt.expected, result)
			assert.True(t, sanitized)
		})
	}
}

func TestSanitizeLLMResponse_CombinedArtifacts(t *testing.T) {
	// This simulates the real bug: tag followed by JSON blocks
	input := `Баланс Силы восстановлен. ⚖️😈</tool_code></s>
` + "```json\n{\"query\": \"test 1\"}\n```\n```json\n{\"query\": \"test 2\"}\n```\n```json\n{\"query\": \"test 3\"}\n```"

	result, sanitized := SanitizeLLMResponse(input)
	assert.Equal(t, "Баланс Силы восстановлен. ⚖️😈", result)
	assert.True(t, sanitized)
}

func TestSanitizeLLMResponse_DoesNotReturnEmpty(t *testing.T) {
	// If sanitization would result in empty string, return original
	input := "</tool_code>"
	result, sanitized := SanitizeLLMResponse(input)
	assert.Equal(t, input, result)
	assert.False(t, sanitized)
}

func TestSanitizeLLMResponse_RemovesDefaultAPIBlock(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			"default_api at start with text after",
			`default_api:manage_memory{query:{"operations": [{"action": "add"}]}}Отличные новости!`,
			"Отличные новости!",
		},
		{
			"default_api at start with newline and text",
			"default_api:internet_search{query:home assistant golang}\n\nHere are the results:",
			"Here are the results:",
		},
		{
			"text before and after default_api",
			"Let me search for that. default_api:internet_search{query:test} Found it!",
			"Let me search for that.\n\nFound it!",
		},
		{
			"nested braces in JSON",
			`default_api:manage_memory{query:{"data": {"nested": {"deep": true}}}}Done!`,
			"Done!",
		},
		{
			"default_api with escaped quotes in JSON",
			`default_api:manage_memory{query:{"content": "He said \"hello\""}}Text after`,
			"Text after",
		},
		{
			"only default_api block (returns original)",
			`default_api:manage_memory{query:{"action": "test"}}`,
			`default_api:manage_memory{query:{"action": "test"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, sanitized := SanitizeLLMResponse(tt.input)
			assert.Equal(t, tt.expected, result)
			if tt.input != tt.expected {
				assert.True(t, sanitized)
			}
		})
	}
}

func TestFindMatchingBrace(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		startIdx int
		expected int
	}{
		{"simple", `{"a": 1}`, 0, 7},
		{"nested", `{"a": {"b": 2}}`, 0, 14},
		{"with string", `{"a": "hello"}`, 0, 13},
		{"with escaped quote", `{"a": "say \"hi\""}`, 0, 18},
		{"deeply nested", `{"a": {"b": {"c": {"d": 1}}}}`, 0, 28},
		{"no closing brace", `{"a": 1`, 0, -1},
		{"invalid start", `hello`, 0, -1},
		{"start not at brace", `hello {world}`, 0, -1},
		{"correct start index", `hello {world}`, 6, 12},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FindMatchingBrace(tt.text, tt.startIdx)
			assert.Equal(t, tt.expected, result)
		})
	}
}

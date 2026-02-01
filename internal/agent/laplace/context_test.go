package laplace

import (
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/stretchr/testify/assert"
)

func TestDeduplicateTopics(t *testing.T) {
	tests := []struct {
		name          string
		topics        []rag.TopicSearchResult
		recentHistory []storage.Message
		expected      []rag.TopicSearchResult
	}{
		{
			name:          "empty topics",
			topics:        []rag.TopicSearchResult{},
			recentHistory: []storage.Message{},
			expected:      nil, // deduplicateTopics returns nil for empty result
		},
		{
			name:          "empty history",
			topics:        []rag.TopicSearchResult{{Messages: []storage.Message{{ID: 1}}}},
			recentHistory: []storage.Message{},
			expected:      []rag.TopicSearchResult{{Messages: []storage.Message{{ID: 1}}}},
		},
		{
			name: "removes duplicate messages",
			topics: []rag.TopicSearchResult{
				{
					Topic:    storage.Topic{ID: 1},
					Messages: []storage.Message{{ID: 1, Content: "msg1"}, {ID: 2, Content: "msg2"}},
				},
			},
			recentHistory: []storage.Message{{ID: 1, Content: "recent1"}},
			expected: []rag.TopicSearchResult{
				{
					Topic:    storage.Topic{ID: 1},
					Messages: []storage.Message{{ID: 2, Content: "msg2"}},
				},
			},
		},
		{
			name: "removes all messages if all duplicate",
			topics: []rag.TopicSearchResult{
				{
					Topic:    storage.Topic{ID: 1},
					Messages: []storage.Message{{ID: 1, Content: "msg1"}},
				},
			},
			recentHistory: []storage.Message{{ID: 1, Content: "recent1"}},
			expected:      nil, // deduplicateTopics returns nil when all messages filtered out
		},
		{
			name: "multiple topics with partial duplicates",
			topics: []rag.TopicSearchResult{
				{
					Topic:    storage.Topic{ID: 1},
					Messages: []storage.Message{{ID: 1}, {ID: 2}, {ID: 3}},
				},
				{
					Topic:    storage.Topic{ID: 2},
					Messages: []storage.Message{{ID: 4}, {ID: 5}},
				},
			},
			recentHistory: []storage.Message{{ID: 2}, {ID: 4}},
			expected: []rag.TopicSearchResult{
				{
					Topic:    storage.Topic{ID: 1},
					Messages: []storage.Message{{ID: 1}, {ID: 3}},
				},
				{
					Topic:    storage.Topic{ID: 2},
					Messages: []storage.Message{{ID: 5}},
				},
			},
		},
		{
			name: "preserves order of messages",
			topics: []rag.TopicSearchResult{
				{
					Topic:    storage.Topic{ID: 1},
					Messages: []storage.Message{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}},
				},
			},
			recentHistory: []storage.Message{{ID: 2}},
			expected: []rag.TopicSearchResult{
				{
					Topic:    storage.Topic{ID: 1},
					Messages: []storage.Message{{ID: 1}, {ID: 3}, {ID: 4}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := deduplicateTopics(tt.topics, tt.recentHistory)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatRAGResults(t *testing.T) {
	baseTime := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name     string
		results  []rag.TopicSearchResult
		query    string
		expected string
	}{
		{
			name:     "empty results",
			results:  []rag.TopicSearchResult{},
			query:    "test query",
			expected: "",
		},
		{
			name: "single topic with user message",
			results: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{
						ID:      1,
						Summary: "Discussion about AI",
					},
					Score: 0.85,
					Messages: []storage.Message{
						{
							ID:        10,
							Role:      "user",
							Content:   "What is AI?",
							CreatedAt: baseTime,
						},
					},
				},
			},
			query: "AI question",
			expected: `<retrieved_context query="AI question">
  <topic id="1" summary="Discussion about AI" relevance="0.85" date="2025-01-15">
1. What is AI?
  </topic>
</retrieved_context>`,
		},
		{
			name: "topic with assistant message",
			results: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{
						ID:      2,
						Summary: "Code discussion",
					},
					Score: 0.92,
					Messages: []storage.Message{
						{
							ID:        20,
							Role:      "assistant",
							Content:   "Here is the code example",
							CreatedAt: baseTime,
						},
					},
				},
			},
			query: "code help",
			expected: `<retrieved_context query="code help">
  <topic id="2" summary="Code discussion" relevance="0.92" date="2025-01-15">
1. [Assistant (2025-01-15 10:30:00)]: Here is the code example
  </topic>
</retrieved_context>`,
		},
		{
			name: "multiple topics",
			results: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{ID: 1, Summary: "First topic", CreatedAt: baseTime},
					Score: 0.8,
					Messages: []storage.Message{
						{ID: 1, Role: "user", Content: "msg1", CreatedAt: baseTime},
					},
				},
				{
					Topic: storage.Topic{ID: 2, Summary: "Second topic", CreatedAt: baseTime},
					Score: 0.9,
					Messages: []storage.Message{
						{ID: 2, Role: "user", Content: "msg2", CreatedAt: baseTime},
					},
				},
			},
			query: "search query",
			expected: `<retrieved_context query="search query">
  <topic id="1" summary="First topic" relevance="0.80" date="2025-01-15">
1. msg1
  </topic>
  <topic id="2" summary="Second topic" relevance="0.90" date="2025-01-15">
1. msg2
  </topic>
</retrieved_context>`,
		},
		{
			name: "XML escaping in summary and query",
			results: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{
						ID:        1,
						Summary:   `Summary with "quotes" & <tags>`,
						CreatedAt: baseTime,
					},
					Score: 0.7,
					Messages: []storage.Message{
						{ID: 1, Role: "user", Content: "content", CreatedAt: baseTime},
					},
				},
			},
			query: `query with "quotes" & <brackets>`,
			expected: `<retrieved_context query="query with &quot;quotes&quot; &amp; &lt;brackets&gt;">
  <topic id="1" summary="Summary with &quot;quotes&quot; &amp; &lt;tags&gt;" relevance="0.70" date="2025-01-15">
1. content
  </topic>
</retrieved_context>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatRAGResults(tt.results, tt.query)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEscapeXMLAttr(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain text",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "ampersand",
			input:    "a & b",
			expected: "a &amp; b",
		},
		{
			name:     "double quote",
			input:    `say "hello"`,
			expected: `say &quot;hello&quot;`,
		},
		{
			name:     "less than",
			input:    "a < b",
			expected: "a &lt; b",
		},
		{
			name:     "greater than",
			input:    "a > b",
			expected: "a &gt; b",
		},
		{
			name:     "all special chars",
			input:    `& " < >`,
			expected: `&amp; &quot; &lt; &gt;`,
		},
		{
			name:     "multiple ampersands",
			input:    "A & B & C",
			expected: "A &amp; B &amp; C",
		},
		{
			name:     "already escaped",
			input:    "a &amp; b",
			expected: "a &amp;amp; b", // double-escaped, but consistent
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := escapeXMLAttr(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEscapeXMLText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain text",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "ampersand",
			input:    "a & b",
			expected: "a &amp; b",
		},
		{
			name:     "less than",
			input:    "a < b",
			expected: "a &lt; b",
		},
		{
			name:     "greater than",
			input:    "a > b",
			expected: "a &gt; b",
		},
		{
			name:     "all special chars",
			input:    "& < >",
			expected: "&amp; &lt; &gt;",
		},
		{
			name:     "quotes not escaped in text",
			input:    `say "hello"`,
			expected: `say "hello"`,
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := escapeXMLText(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

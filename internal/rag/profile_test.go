package rag

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/runixer/laplaced/internal/storage"
)

func TestFormatUserProfile(t *testing.T) {
	tests := []struct {
		name     string
		facts    []storage.Fact
		contains []string
		notEmpty bool
	}{
		{
			name:     "empty facts",
			facts:    []storage.Fact{},
			contains: []string{"<user_profile>", "</user_profile>"},
			notEmpty: false,
		},
		{
			name: "single fact",
			facts: []storage.Fact{
				{
					ID:          1,
					Category:    "identity",
					Type:        "name",
					Content:     "Меня зовут Алексей",
					LastUpdated: time.Date(2025, 1, 9, 0, 0, 0, 0, time.UTC),
				},
			},
			contains: []string{
				"<user_profile>",
				"[Fact:1]",
				"[identity/name]",
				"(Updated: 2025-01-09)",
				"Меня зовут Алексей",
				"</user_profile>",
			},
			notEmpty: true,
		},
		{
			name: "multiple facts",
			facts: []storage.Fact{
				{
					ID:          1,
					Category:    "identity",
					Type:        "core",
					Content:     "Работаю программистом",
					LastUpdated: time.Date(2025, 1, 8, 0, 0, 0, 0, time.UTC),
				},
				{
					ID:          2,
					Category:    "identity",
					Type:        "relationship",
					Content:     "Жену зовут Мария",
					LastUpdated: time.Date(2025, 1, 7, 0, 0, 0, 0, time.UTC),
				},
			},
			contains: []string{
				"<user_profile>",
				"[Fact:1]",
				"[Fact:2]",
				"</user_profile>",
			},
			notEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatUserProfile(tt.facts)

			for _, s := range tt.contains {
				assert.Contains(t, result, s)
			}

			if tt.notEmpty {
				// Should have content between tags
				assert.NotEqual(t, "<user_profile>\n</user_profile>", result)
			}
		})
	}
}

func TestFormatRecentTopics(t *testing.T) {
	tests := []struct {
		name     string
		topics   []storage.TopicExtended
		contains []string
		notEmpty bool
	}{
		{
			name:     "empty topics",
			topics:   []storage.TopicExtended{},
			contains: []string{"<recent_topics>", "</recent_topics>"},
			notEmpty: false,
		},
		{
			name: "single topic",
			topics: []storage.TopicExtended{
				{
					Topic: storage.Topic{
						Summary:   "Обсуждение архитектуры RAG",
						SizeChars: 5000,
						CreatedAt: time.Date(2025, 1, 9, 0, 0, 0, 0, time.UTC),
					},
					MessageCount: 12,
				},
			},
			contains: []string{
				"<recent_topics>",
				"2025-01-09:",
				"Обсуждение архитектуры RAG",
				"(12 msg, ~5k chars)",
				"</recent_topics>",
			},
			notEmpty: true,
		},
		{
			name: "multiple topics",
			topics: []storage.TopicExtended{
				{
					Topic: storage.Topic{
						Summary:   "Topic 1",
						SizeChars: 3000,
						CreatedAt: time.Date(2025, 1, 9, 0, 0, 0, 0, time.UTC),
					},
					MessageCount: 8,
				},
				{
					Topic: storage.Topic{
						Summary:   "Topic 2",
						SizeChars: 2000,
						CreatedAt: time.Date(2025, 1, 8, 0, 0, 0, 0, time.UTC),
					},
					MessageCount: 5,
				},
			},
			contains: []string{
				"<recent_topics>",
				"Topic 1",
				"Topic 2",
				"2025-01-09:",
				"2025-01-08:",
				"</recent_topics>",
			},
			notEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatRecentTopics(tt.topics)

			for _, s := range tt.contains {
				assert.Contains(t, result, s)
			}

			if tt.notEmpty {
				assert.NotEqual(t, "<recent_topics>\n</recent_topics>", result)
			}
		})
	}
}

func TestFilterProfileFacts(t *testing.T) {
	facts := []storage.Fact{
		{ID: 1, Type: "identity", Importance: 50},   // include: identity
		{ID: 2, Type: "preference", Importance: 90}, // include: high importance
		{ID: 3, Type: "random", Importance: 70},     // exclude: not identity, low importance
		{ID: 4, Type: "identity", Importance: 95},   // include: identity + high importance
		{ID: 5, Type: "other", Importance: 80},      // include: exactly 80
		{ID: 6, Type: "other", Importance: 79},      // exclude: below 80
	}

	result := FilterProfileFacts(facts)

	assert.Len(t, result, 4)

	ids := make([]int64, len(result))
	for i, f := range result {
		ids[i] = f.ID
	}

	assert.Contains(t, ids, int64(1))
	assert.Contains(t, ids, int64(2))
	assert.Contains(t, ids, int64(4))
	assert.Contains(t, ids, int64(5))
	assert.NotContains(t, ids, int64(3))
	assert.NotContains(t, ids, int64(6))
}

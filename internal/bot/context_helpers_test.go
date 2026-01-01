package bot

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

func TestDeduplicateTopics(t *testing.T) {
	bot := &Bot{}

	tests := []struct {
		name          string
		topics        []rag.TopicSearchResult
		recentHistory []storage.Message
		expectedCount int
		expectedMsgs  map[int]int // topicIndex -> expected message count
	}{
		{
			name:          "empty topics",
			topics:        []rag.TopicSearchResult{},
			recentHistory: []storage.Message{{ID: 1}},
			expectedCount: 0,
		},
		{
			name: "empty recent history - no dedup",
			topics: []rag.TopicSearchResult{
				{
					Topic:    storage.Topic{ID: 1},
					Messages: []storage.Message{{ID: 10}, {ID: 11}},
				},
			},
			recentHistory: []storage.Message{},
			expectedCount: 1,
			expectedMsgs:  map[int]int{0: 2},
		},
		{
			name: "no overlap",
			topics: []rag.TopicSearchResult{
				{
					Topic:    storage.Topic{ID: 1},
					Messages: []storage.Message{{ID: 10}, {ID: 11}},
				},
			},
			recentHistory: []storage.Message{{ID: 1}, {ID: 2}},
			expectedCount: 1,
			expectedMsgs:  map[int]int{0: 2},
		},
		{
			name: "partial overlap",
			topics: []rag.TopicSearchResult{
				{
					Topic:    storage.Topic{ID: 1},
					Messages: []storage.Message{{ID: 1}, {ID: 10}, {ID: 11}},
				},
			},
			recentHistory: []storage.Message{{ID: 1}, {ID: 2}},
			expectedCount: 1,
			expectedMsgs:  map[int]int{0: 2}, // only 10 and 11 remain
		},
		{
			name: "full overlap - topic removed",
			topics: []rag.TopicSearchResult{
				{
					Topic:    storage.Topic{ID: 1},
					Messages: []storage.Message{{ID: 1}, {ID: 2}},
				},
			},
			recentHistory: []storage.Message{{ID: 1}, {ID: 2}},
			expectedCount: 0,
		},
		{
			name: "multiple topics with mixed overlap",
			topics: []rag.TopicSearchResult{
				{
					Topic:    storage.Topic{ID: 1},
					Messages: []storage.Message{{ID: 1}, {ID: 2}}, // all overlap
				},
				{
					Topic:    storage.Topic{ID: 2},
					Messages: []storage.Message{{ID: 3}, {ID: 10}}, // partial overlap
				},
				{
					Topic:    storage.Topic{ID: 3},
					Messages: []storage.Message{{ID: 20}, {ID: 21}}, // no overlap
				},
			},
			recentHistory: []storage.Message{{ID: 1}, {ID: 2}, {ID: 3}},
			expectedCount: 2,                        // topic 1 removed
			expectedMsgs:  map[int]int{0: 1, 1: 2}, // topic2 has 1, topic3 has 2
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := bot.deduplicateTopics(tt.topics, tt.recentHistory)

			assert.Len(t, result, tt.expectedCount)

			for idx, expectedMsgCount := range tt.expectedMsgs {
				assert.Len(t, result[idx].Messages, expectedMsgCount,
					"topic at index %d should have %d messages", idx, expectedMsgCount)
			}
		})
	}
}

func TestFormatCoreIdentityFacts(t *testing.T) {
	translator := createTestTranslator(t)
	cfg := &config.Config{Bot: config.BotConfig{Language: "en"}}
	bot := &Bot{translator: translator, cfg: cfg}

	now := time.Now()

	tests := []struct {
		name     string
		facts    []storage.Fact
		contains []string
		notEmpty bool
	}{
		{
			name:     "empty facts",
			facts:    []storage.Fact{},
			notEmpty: false,
		},
		{
			name: "user facts only",
			facts: []storage.Fact{
				{ID: 1, Entity: "User", Category: "personal", Type: "identity", Content: "Loves Go", LastUpdated: now},
			},
			contains: []string{"[ID:1]", "[User]", "[personal/identity]", "Loves Go"},
			notEmpty: true,
		},
		{
			name: "other entity facts only",
			facts: []storage.Fact{
				{ID: 2, Entity: "Alice", Category: "work", Type: "context", Content: "Is a colleague", LastUpdated: now},
			},
			contains: []string{"[ID:2]", "[Alice]", "[work/context]", "Is a colleague"},
			notEmpty: true,
		},
		{
			name: "mixed user and other facts",
			facts: []storage.Fact{
				{ID: 1, Entity: "User", Category: "personal", Type: "identity", Content: "Loves Go", LastUpdated: now},
				{ID: 2, Entity: "user", Category: "hobby", Type: "identity", Content: "Plays chess", LastUpdated: now}, // lowercase user
				{ID: 3, Entity: "Bob", Category: "friend", Type: "context", Content: "Lives nearby", LastUpdated: now},
			},
			contains: []string{"[ID:1]", "[ID:2]", "[ID:3]", "Loves Go", "Plays chess", "Lives nearby"},
			notEmpty: true,
		},
		{
			name: "case insensitive user entity",
			facts: []storage.Fact{
				{ID: 1, Entity: "USER", Category: "test", Type: "identity", Content: "Uppercase", LastUpdated: now},
				{ID: 2, Entity: "uSeR", Category: "test", Type: "identity", Content: "Mixed case", LastUpdated: now},
			},
			contains: []string{"Uppercase", "Mixed case"},
			notEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := bot.formatCoreIdentityFacts(tt.facts)

			if tt.notEmpty {
				assert.NotEmpty(t, result)
				for _, substr := range tt.contains {
					assert.Contains(t, result, substr)
				}
			} else {
				assert.Empty(t, result)
			}
		})
	}
}

package bot

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

func TestIntPtrOrNil(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected *int
	}{
		{
			name:     "zero returns nil",
			input:    0,
			expected: nil,
		},
		{
			name:  "positive returns pointer",
			input: 42,
		},
		{
			name:  "negative returns pointer",
			input: -5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := intPtrOrNil(tt.input)
			if tt.input == 0 {
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
				assert.Equal(t, tt.input, *result)
			}
		})
	}
}

func TestIsAllowed(t *testing.T) {
	tests := []struct {
		name           string
		allowedUserIDs []int64
		userID         int64
		expected       bool
	}{
		{
			name:           "empty list denies all",
			allowedUserIDs: []int64{},
			userID:         123,
			expected:       false,
		},
		{
			name:           "user in allowed list",
			allowedUserIDs: []int64{100, 200, 300},
			userID:         200,
			expected:       true,
		},
		{
			name:           "user not in allowed list",
			allowedUserIDs: []int64{100, 200, 300},
			userID:         999,
			expected:       false,
		},
		{
			name:           "first user in list",
			allowedUserIDs: []int64{123},
			userID:         123,
			expected:       true,
		},
		{
			name:           "last user in list",
			allowedUserIDs: []int64{100, 200, 300},
			userID:         300,
			expected:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Bot: config.BotConfig{
					AllowedUserIDs: tt.allowedUserIDs,
				},
			}
			bot := &Bot{cfg: cfg}

			result := bot.isAllowed(tt.userID)
			assert.Equal(t, tt.expected, result)
		})
	}
}

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
			expectedCount: 2,                       // topic 1 removed
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

func TestSetMessageReactionConfigParams(t *testing.T) {
	tests := []struct {
		name           string
		config         SetMessageReactionConfig
		expectedKeys   []string
		expectedValues map[string]string
	}{
		{
			name: "basic reaction",
			config: SetMessageReactionConfig{
				ChatID:    12345,
				MessageID: 100,
				Reaction:  []ReactionType{{Type: "emoji", Emoji: "👍"}},
				IsBig:     false,
			},
			expectedKeys:   []string{"chat_id", "message_id", "reaction"},
			expectedValues: map[string]string{"chat_id": "12345", "message_id": "100"},
		},
		{
			name: "with is_big true",
			config: SetMessageReactionConfig{
				ChatID:    999,
				MessageID: 200,
				Reaction:  []ReactionType{{Type: "emoji", Emoji: "❤️"}},
				IsBig:     true,
			},
			expectedKeys:   []string{"chat_id", "message_id", "reaction", "is_big"},
			expectedValues: map[string]string{"is_big": "true"},
		},
		{
			name: "empty reaction",
			config: SetMessageReactionConfig{
				ChatID:    123,
				MessageID: 1,
				Reaction:  []ReactionType{},
			},
			expectedKeys:   []string{"chat_id", "message_id"},
			expectedValues: map[string]string{"chat_id": "123", "message_id": "1"},
		},
		{
			name: "multiple reactions",
			config: SetMessageReactionConfig{
				ChatID:    555,
				MessageID: 42,
				Reaction: []ReactionType{
					{Type: "emoji", Emoji: "👍"},
					{Type: "emoji", Emoji: "❤️"},
				},
			},
			expectedKeys:   []string{"chat_id", "message_id", "reaction"},
			expectedValues: map[string]string{"chat_id": "555", "message_id": "42"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := tt.config.Params()
			assert.NoError(t, err)

			for _, key := range tt.expectedKeys {
				assert.Contains(t, params, key)
			}

			for k, v := range tt.expectedValues {
				assert.Equal(t, v, params[k])
			}
		})
	}
}

func TestSetMessageReactionConfigMethod(t *testing.T) {
	config := SetMessageReactionConfig{}
	assert.Equal(t, "setMessageReaction", config.Method())
}

func TestBotAPIGetter(t *testing.T) {
	mockAPI := new(MockBotAPI)
	bot := &Bot{api: mockAPI}
	assert.Equal(t, mockAPI, bot.API())
}

func TestBotStop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)

	bot := &Bot{
		api:    mockAPI,
		logger: logger,
	}

	// Create a message grouper with correct signature
	bot.messageGrouper = NewMessageGrouper(bot, logger, 10*time.Millisecond, func(ctx context.Context, g *MessageGroup) {
		// no-op handler
	})

	// Stop should complete without panic
	bot.Stop()
}

func TestMessageOriginUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name         string
		json         string
		expectedType string
		shouldErr    bool
	}{
		{
			name:         "origin user",
			json:         `{"type":"user","date":1234567890,"sender_user":{"id":123,"first_name":"John"}}`,
			expectedType: "user",
		},
		{
			name:         "origin hidden_user",
			json:         `{"type":"hidden_user","date":1234567890,"sender_user_name":"Anonymous"}`,
			expectedType: "hidden_user",
		},
		{
			name:         "origin chat",
			json:         `{"type":"chat","date":1234567890,"sender_chat":{"id":-100123,"type":"supergroup"}}`,
			expectedType: "chat",
		},
		{
			name:         "origin channel",
			json:         `{"type":"channel","date":1234567890,"message_id":42,"author_signature":"Admin"}`,
			expectedType: "channel",
		},
		{
			name:      "invalid json",
			json:      `{invalid`,
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mo MessageOrigin
			err := mo.UnmarshalJSON([]byte(tt.json))
			if tt.shouldErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedType, mo.Type)
			}
		})
	}
}

func TestFormatRAGResults(t *testing.T) {
	translator := createTestTranslator(t)
	cfg := &config.Config{Bot: config.BotConfig{Language: "en"}}
	bot := &Bot{translator: translator, cfg: cfg}

	now := time.Now()

	tests := []struct {
		name     string
		results  []rag.TopicSearchResult
		query    string
		isEmpty  bool
		contains []string
	}{
		{
			name:    "empty results returns empty string",
			results: []rag.TopicSearchResult{},
			query:   "test query",
			isEmpty: true,
		},
		{
			name: "single topic with messages - XML format",
			results: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{ID: 1, Summary: "Test Topic Summary", CreatedAt: now},
					Score: 0.85,
					Messages: []storage.Message{
						{ID: 1, Role: "user", Content: "[User]: Hello", CreatedAt: now},
						{ID: 2, Role: "assistant", Content: "Hi there!", CreatedAt: now},
					},
				},
			},
			query: "greeting",
			contains: []string{
				"<retrieved_context query=\"greeting\">",
				"<topic id=\"1\" summary=\"Test Topic Summary\" relevance=\"0.85\"",
				"[User]: Hello",
				"Hi there!",
				"1.",
				"2.",
				"</topic>",
				"</retrieved_context>",
			},
		},
		{
			name: "multiple topics - XML format",
			results: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{ID: 1, Summary: "First Topic", CreatedAt: now},
					Score: 0.9,
					Messages: []storage.Message{
						{ID: 1, Role: "user", Content: "[User]: Message 1", CreatedAt: now},
					},
				},
				{
					Topic: storage.Topic{ID: 2, Summary: "Second Topic", CreatedAt: now},
					Score: 0.7,
					Messages: []storage.Message{
						{ID: 2, Role: "assistant", Content: "Response 2", CreatedAt: now},
					},
				},
			},
			query: "multi",
			contains: []string{
				"<retrieved_context query=\"multi\">",
				"<topic id=\"1\" summary=\"First Topic\" relevance=\"0.90\"",
				"<topic id=\"2\" summary=\"Second Topic\" relevance=\"0.70\"",
			},
		},
		{
			name: "with system role message - XML format",
			results: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{ID: 1, Summary: "System Topic", CreatedAt: now},
					Score: 0.5,
					Messages: []storage.Message{
						{ID: 1, Role: "system", Content: "System instruction", CreatedAt: now},
					},
				},
			},
			query: "system",
			contains: []string{
				"summary=\"System Topic\"",
				"[System",
			},
		},
		{
			name: "query with special XML characters",
			results: []rag.TopicSearchResult{
				{
					Topic: storage.Topic{ID: 1, Summary: "Test <Topic> & Summary", CreatedAt: now},
					Score: 0.5,
					Messages: []storage.Message{
						{ID: 1, Role: "user", Content: "Test", CreatedAt: now},
					},
				},
			},
			query: "query <with> \"quotes\" & ampersand",
			contains: []string{
				"query &lt;with&gt; &quot;quotes&quot; &amp; ampersand",
				"summary=\"Test &lt;Topic&gt; &amp; Summary\"",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := bot.formatRAGResults(tt.results, tt.query)
			if tt.isEmpty {
				assert.Empty(t, result)
			} else {
				for _, substr := range tt.contains {
					assert.Contains(t, result, substr)
				}
			}
		})
	}
}

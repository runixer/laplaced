package bot

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/testutil"
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
	mockAPI := new(testutil.MockBotAPI)
	bot := &Bot{api: mockAPI}
	assert.Equal(t, mockAPI, bot.API())
}

func TestBotStop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mockAPI := new(testutil.MockBotAPI)

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

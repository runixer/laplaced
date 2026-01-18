package main

import (
	"context"
	"testing"

	"github.com/runixer/laplaced/internal/telegram"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoOpBotAPI(t *testing.T) {
	api := &noOpBotAPI{}
	ctx := context.Background()

	t.Run("SendMessage returns mock message", func(t *testing.T) {
		req := telegram.SendMessageRequest{
			ChatID: 123,
			Text:   "test message",
		}
		msg, err := api.SendMessage(ctx, req)
		require.NoError(t, err)
		assert.NotNil(t, msg)
		assert.Equal(t, 1, msg.MessageID)
	})

	t.Run("SetMyCommands no error", func(t *testing.T) {
		req := telegram.SetMyCommandsRequest{
			Commands: []telegram.BotCommand{},
		}
		err := api.SetMyCommands(ctx, req)
		assert.NoError(t, err)
	})

	t.Run("SetWebhook no error", func(t *testing.T) {
		req := telegram.SetWebhookRequest{
			URL: "https://example.com/webhook",
		}
		err := api.SetWebhook(ctx, req)
		assert.NoError(t, err)
	})

	t.Run("SendChatAction no error", func(t *testing.T) {
		req := telegram.SendChatActionRequest{
			ChatID: 123,
			Action: "typing",
		}
		err := api.SendChatAction(ctx, req)
		assert.NoError(t, err)
	})

	t.Run("GetFile returns file info", func(t *testing.T) {
		req := telegram.GetFileRequest{
			FileID: "test_file_id",
		}
		file, err := api.GetFile(ctx, req)
		require.NoError(t, err)
		assert.NotNil(t, file)
		assert.Equal(t, "test_file_id", file.FileID)
	})

	t.Run("SetMessageReaction no error", func(t *testing.T) {
		req := telegram.SetMessageReactionRequest{
			ChatID:    123,
			MessageID: 456,
		}
		err := api.SetMessageReaction(ctx, req)
		assert.NoError(t, err)
	})

	t.Run("GetUpdates returns empty", func(t *testing.T) {
		req := telegram.GetUpdatesRequest{
			Offset: 0,
			Limit:  100,
		}
		updates, err := api.GetUpdates(ctx, req)
		require.NoError(t, err)
		assert.NotNil(t, updates)
		assert.Empty(t, updates)
	})

	t.Run("GetToken returns placeholder", func(t *testing.T) {
		token := api.GetToken()
		assert.Equal(t, "testbot_token", token)
	})
}

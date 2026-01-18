package main

import (
	"context"

	"github.com/runixer/laplaced/internal/telegram"
)

// noOpBotAPI provides a no-op implementation of telegram.BotAPI for testbot.
// This eliminates the dependency on testutil.MockBotAPI in production code.
type noOpBotAPI struct{}

// SendMessage is a no-op for testbot (returns a mock message).
func (n *noOpBotAPI) SendMessage(ctx context.Context, req telegram.SendMessageRequest) (*telegram.Message, error) {
	return &telegram.Message{MessageID: 1}, nil
}

// SetMyCommands is a no-op for testbot (no Telegram commands needed).
func (n *noOpBotAPI) SetMyCommands(ctx context.Context, req telegram.SetMyCommandsRequest) error {
	return nil
}

// SetWebhook is a no-op for testbot (no webhook needed).
func (n *noOpBotAPI) SetWebhook(ctx context.Context, req telegram.SetWebhookRequest) error {
	return nil
}

// SendChatAction is a no-op for testbot.
func (n *noOpBotAPI) SendChatAction(ctx context.Context, req telegram.SendChatActionRequest) error {
	return nil
}

// GetFile is a no-op for testbot (returns empty file info).
func (n *noOpBotAPI) GetFile(ctx context.Context, req telegram.GetFileRequest) (*telegram.File, error) {
	return &telegram.File{FileID: req.FileID}, nil
}

// SetMessageReaction is a no-op for testbot.
func (n *noOpBotAPI) SetMessageReaction(ctx context.Context, req telegram.SetMessageReactionRequest) error {
	return nil
}

// GetUpdates is a no-op for testbot (returns no updates).
func (n *noOpBotAPI) GetUpdates(ctx context.Context, req telegram.GetUpdatesRequest) ([]telegram.Update, error) {
	return []telegram.Update{}, nil
}

// GetToken returns a placeholder token for testbot.
func (n *noOpBotAPI) GetToken() string {
	return "testbot_token"
}

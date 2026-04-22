package main

import (
	"context"
	"fmt"
	"os"

	"github.com/runixer/laplaced/internal/telegram"
)

// mockFileDownloader implements telegram.FileDownloader for testbot.
// It reads files from local disk instead of downloading from Telegram.
type mockFileDownloader struct {
	// voiceFilePath is set by --voice flag to override voice downloads
	voiceFilePath string
}

// DownloadFile simulates downloading a file from Telegram.
// For voice messages (file_id starting with "voice_"), it reads from the configured path.
func (m *mockFileDownloader) DownloadFile(ctx context.Context, fileID string) ([]byte, error) {
	// Check if this is a voice message mock
	if len(fileID) > 6 && fileID[:6] == "voice_" {
		if m.voiceFilePath == "" {
			return nil, fmt.Errorf("voice file requested but --voice not provided")
		}
		return os.ReadFile(m.voiceFilePath)
	}

	// For other file types, return empty data (not implemented yet)
	return []byte{}, fmt.Errorf("mock file downloader: only voice files supported, got file_id: %s", fileID)
}

// DownloadFileAsBase64 simulates downloading and base64-encoding a file.
func (m *mockFileDownloader) DownloadFileAsBase64(ctx context.Context, fileID string) (string, error) {
	data, err := m.DownloadFile(ctx, fileID)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// noOpBotAPI provides a no-op implementation of telegram.BotAPI for testbot.
// This eliminates the dependency on testutil.MockBotAPI in production code.
type noOpBotAPI struct {
	downloader telegram.FileDownloader // Optional: for file support
}

// GetFile returns a mock file info. If downloader is set, it delegates to it.
func (n *noOpBotAPI) GetFile(ctx context.Context, req telegram.GetFileRequest) (*telegram.File, error) {
	return &telegram.File{FileID: req.FileID}, nil
}

// GetDownloader returns the file downloader for mock API.
func (n *noOpBotAPI) GetDownloader() telegram.FileDownloader {
	return n.downloader
}

// SetDownloader sets the file downloader for mock API.
func (n *noOpBotAPI) SetDownloader(d telegram.FileDownloader) {
	n.downloader = d
}

// SendMessage is a no-op for testbot (returns a mock message).
func (n *noOpBotAPI) SendMessage(ctx context.Context, req telegram.SendMessageRequest) (*telegram.Message, error) {
	return &telegram.Message{MessageID: 1}, nil
}

// SendPhoto is a no-op for testbot.
func (n *noOpBotAPI) SendPhoto(ctx context.Context, req telegram.SendPhotoRequest) (*telegram.Message, error) {
	return &telegram.Message{MessageID: 1}, nil
}

// SendDocument is a no-op for testbot.
func (n *noOpBotAPI) SendDocument(ctx context.Context, req telegram.SendDocumentRequest) (*telegram.Message, error) {
	return &telegram.Message{MessageID: 1}, nil
}

// SendMediaGroup is a no-op for testbot.
func (n *noOpBotAPI) SendMediaGroup(ctx context.Context, req telegram.SendMediaGroupRequest) ([]telegram.Message, error) {
	return []telegram.Message{{MessageID: 1}}, nil
}

// SendMediaGroupDocuments is a no-op for testbot.
func (n *noOpBotAPI) SendMediaGroupDocuments(ctx context.Context, req telegram.SendMediaGroupDocumentsRequest) ([]telegram.Message, error) {
	return []telegram.Message{{MessageID: 1}}, nil
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

package main

import (
	"context"

	"github.com/runixer/laplaced/internal/telegram"
)

type noOpBotAPI struct {
	downloader telegram.FileDownloader
}

func (n *noOpBotAPI) GetFile(_ context.Context, req telegram.GetFileRequest) (*telegram.File, error) {
	return &telegram.File{FileID: req.FileID}, nil
}
func (n *noOpBotAPI) GetDownloader() telegram.FileDownloader  { return n.downloader }
func (n *noOpBotAPI) SetDownloader(d telegram.FileDownloader) { n.downloader = d }
func (n *noOpBotAPI) SendMessage(_ context.Context, _ telegram.SendMessageRequest) (*telegram.Message, error) {
	return &telegram.Message{MessageID: 1}, nil
}
func (n *noOpBotAPI) SendRichMessage(_ context.Context, _ telegram.SendRichMessageRequest) (*telegram.Message, error) {
	return &telegram.Message{MessageID: 1}, nil
}
func (n *noOpBotAPI) SendRichMessageDraft(_ context.Context, _ telegram.SendRichMessageDraftRequest) error {
	return nil
}
func (n *noOpBotAPI) EditMessageText(_ context.Context, req telegram.EditMessageTextRequest) (*telegram.Message, error) {
	return &telegram.Message{MessageID: req.MessageID}, nil
}
func (n *noOpBotAPI) SendPhoto(_ context.Context, _ telegram.SendPhotoRequest) (*telegram.Message, error) {
	return &telegram.Message{MessageID: 1}, nil
}
func (n *noOpBotAPI) SendDocument(_ context.Context, _ telegram.SendDocumentRequest) (*telegram.Message, error) {
	return &telegram.Message{MessageID: 1}, nil
}
func (n *noOpBotAPI) SendMediaGroup(_ context.Context, _ telegram.SendMediaGroupRequest) ([]telegram.Message, error) {
	return []telegram.Message{{MessageID: 1}}, nil
}
func (n *noOpBotAPI) SendMediaGroupDocuments(_ context.Context, _ telegram.SendMediaGroupDocumentsRequest) ([]telegram.Message, error) {
	return []telegram.Message{{MessageID: 1}}, nil
}
func (n *noOpBotAPI) SetMyCommands(context.Context, telegram.SetMyCommandsRequest) error { return nil }
func (n *noOpBotAPI) SetWebhook(context.Context, telegram.SetWebhookRequest) error       { return nil }
func (n *noOpBotAPI) SendChatAction(context.Context, telegram.SendChatActionRequest) error {
	return nil
}
func (n *noOpBotAPI) SetMessageReaction(context.Context, telegram.SetMessageReactionRequest) error {
	return nil
}
func (n *noOpBotAPI) GetUpdates(context.Context, telegram.GetUpdatesRequest) ([]telegram.Update, error) {
	return nil, nil
}
func (n *noOpBotAPI) GetToken() string { return "longmemeval_token" }

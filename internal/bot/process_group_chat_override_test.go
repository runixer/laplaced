package bot

import (
	"context"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestProcessMessageGroup_ChatUserOverride verifies that the sender's native
// id selects the chat model end to end: listed senders get the override,
// everyone else keeps the global chat model.
func TestProcessMessageGroup_ChatUserOverride(t *testing.T) {
	tests := []struct {
		name      string
		senderID  int64
		wantModel func(cfg *config.Config) string
	}{
		{"listed sender gets override", 555, func(*config.Config) string { return "override-model" }},
		{"other sender keeps global model", 556, func(cfg *config.Config) string { return cfg.Agents.GetChatModel() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := testutil.TestTranslator(t)
			logger := testutil.TestLogger()
			mockAPI := new(testutil.MockBotAPI)
			mockStore := new(testutil.MockStorage)
			mockStore.On("GetPrivacyMode", mock.Anything).Return(false, nil).Maybe()
			mockORClient := new(testutil.MockLLMClient)
			cfg := testutil.TestConfig()
			cfg.RAG.Enabled = false
			cfg.Agents.Chat.UserOverrides = []config.ChatUserOverride{
				{UserIDs: []string{"555"}, Model: "override-model"},
			}
			require.NotEqual(t, "override-model", cfg.Agents.GetChatModel())

			mockDownloader := new(testutil.MockFileDownloader)
			b := &Bot{
				api:             mockAPI,
				userRepo:        mockStore,
				msgRepo:         mockStore,
				statsRepo:       mockStore,
				factRepo:        mockStore,
				factHistoryRepo: mockStore,
				orClient:        mockORClient,
				downloader:      mockDownloader,
				fileProcessor:   files.NewProcessor(mockDownloader, translator, "en", logger),
				cfg:             cfg,
				logger:          logger,
				translator:      translator,
				laplaceAgent:    laplace.New(cfg, mockORClient, nil, mockStore, mockStore, nil, translator, logger),
			}
			b.messageGrouper = NewMessageGrouper(b, logger, 0, b.processMessageGroup)

			userID := storage.ScopeID("scope-" + tt.name)
			messages := []*telegram.Message{{
				MessageID: 42,
				Text:      "Hello",
				From:      &telegram.User{ID: tt.senderID, FirstName: "User", Username: "testuser"},
				Chat:      &telegram.Chat{ID: 777},
				Date:      int(time.Now().Unix()),
			}}

			mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
			mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(&telegram.Message{MessageID: 1}, nil)
			mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
				{Role: "user", Content: messages[0].BuildContent(translator, "en")},
			}, nil)
			mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
			mockStore.On("AddMessageToHistory", userID, mock.Anything).Return(nil)
			mockStore.On("AddStat", mock.Anything).Return(nil)

			wantModel := tt.wantModel(cfg)
			mockORClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(r llm.ChatCompletionRequest) bool {
				return r.Model == wantModel
			})).Return(llm.ChatCompletionResponse{
				Choices: []llm.ResponseChoice{
					{Message: llm.ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"},
				},
				Usage: llm.Usage{TotalTokens: 5},
			}, nil)

			b.processMessageGroup(context.Background(), &MessageGroup{
				Messages: tgIncomings(b, messages...),
				UserID:   userID,
			})

			mockORClient.AssertExpectations(t)
		})
	}
}

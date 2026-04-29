package bot

import (
	"context"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkcodes "go.opentelemetry.io/otel/codes"
)

// TestProcessMessageGroup_RecordsRootSpan verifies the shape of the root
// span produced by Bot.processMessageGroup. Kept as the minimal template
// for future span-structure tests added in Stage 1.2 (RAG, reranker, LLM,
// tools): copy this pattern, swap the exercised function, assert on the
// new span's Name/Attributes/Events.
func TestProcessMessageGroup_RecordsRootSpan(t *testing.T) {
	getSpans := testutil.WithTracingCapture(t)

	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockAPI := new(testutil.MockBotAPI)
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false

	mockDownloader := new(testutil.MockFileDownloader)
	laplaceAgent := laplace.New(cfg, mockORClient, nil, mockStore, mockStore, nil, translator, logger)

	b := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		downloader:      mockDownloader,
		fileProcessor:   files.NewProcessor(mockDownloader, translator, "en", testutil.TestLogger()),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
		laplaceAgent:    laplaceAgent,
	}
	b.messageGrouper = NewMessageGrouper(b, logger, 0, b.processMessageGroup)

	const userID = int64(555)
	const text = "Hello tracing world"
	chatID := int64(777)
	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 42,
			Text:      text,
			From:      &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
			Chat:      &telegram.Chat{ID: chatID},
			Date:      now,
		},
	}

	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil)

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: messages[0].BuildContent(translator, "en")},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.Anything).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(openrouter.ChatCompletionResponse{
		Choices: []openrouter.ResponseChoice{
			{Message: openrouter.ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{TotalTokens: 5},
	}, nil)

	b.processMessageGroup(context.Background(), &MessageGroup{
		Messages: messages,
		UserID:   userID,
	})

	spans := getSpans()
	// processMessageGroup now starts a child laplace.Execute span too —
	// pick out the root by name rather than asserting span count.
	var rootIdx = -1
	for i := range spans {
		if spans[i].Name == "bot.processMessageGroup" {
			rootIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, rootIdx, 0, "bot.processMessageGroup span must be present")
	span := spans[rootIdx]

	attrs := make(map[attribute.Key]attribute.Value, len(span.Attributes))
	for _, kv := range span.Attributes {
		attrs[kv.Key] = kv.Value
	}

	userIDAttr, ok := attrs["user.id"]
	require.True(t, ok, "user.id attribute missing")
	assert.Equal(t, userID, userIDAttr.AsInt64())

	countAttr, ok := attrs["message.count"]
	require.True(t, ok, "message.count attribute missing")
	assert.Equal(t, int64(1), countAttr.AsInt64())

	charsAttr, ok := attrs["message.total_chars"]
	require.True(t, ok, "message.total_chars attribute missing")
	assert.Equal(t, int64(len(text)), charsAttr.AsInt64())

	// Successful run leaves status Unset — Error status is reserved for
	// the !success path.
	assert.Equal(t, sdkcodes.Unset, span.Status.Code)
}

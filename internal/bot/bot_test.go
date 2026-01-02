package bot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Mock BotAPI
type MockBotAPI struct {
	mock.Mock
}

func (m *MockBotAPI) SendMessage(ctx context.Context, req telegram.SendMessageRequest) (*telegram.Message, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*telegram.Message), args.Error(1)
}

func (m *MockBotAPI) SetMyCommands(ctx context.Context, req telegram.SetMyCommandsRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockBotAPI) SetWebhook(ctx context.Context, req telegram.SetWebhookRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockBotAPI) SendChatAction(ctx context.Context, req telegram.SendChatActionRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockBotAPI) GetFile(ctx context.Context, req telegram.GetFileRequest) (*telegram.File, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*telegram.File), args.Error(1)
}

func (m *MockBotAPI) SetMessageReaction(ctx context.Context, req telegram.SetMessageReactionRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockBotAPI) GetToken() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockBotAPI) GetUpdates(ctx context.Context, req telegram.GetUpdatesRequest) ([]telegram.Update, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]telegram.Update), args.Error(1)
}

// Mock Storage
type MockStorage struct {
	mock.Mock
}

func (m *MockStorage) AddMessageToHistory(userID int64, message storage.Message) error {
	args := m.Called(userID, message)
	return args.Error(0)
}

func (m *MockStorage) ImportMessage(userID int64, message storage.Message) error {
	args := m.Called(userID, message)
	return args.Error(0)
}

func (m *MockStorage) GetHistory(userID int64) ([]storage.Message, error) {
	args := m.Called(userID)
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) GetRecentHistory(userID int64, limit int) ([]storage.Message, error) {
	args := m.Called(userID, limit)
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) GetMessagesByIDs(ids []int64) ([]storage.Message, error) {
	args := m.Called(ids)
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) GetFactsByIDs(ids []int64) ([]storage.Fact, error) {
	args := m.Called(ids)
	return args.Get(0).([]storage.Fact), args.Error(1)
}

func (m *MockStorage) ClearHistory(userID int64) error {
	args := m.Called(userID)
	return args.Error(0)
}

func (m *MockStorage) UpsertUser(user storage.User) error {
	args := m.Called(user)
	return args.Error(0)
}

func (m *MockStorage) GetAllUsers() ([]storage.User, error) {
	args := m.Called()
	return args.Get(0).([]storage.User), args.Error(1)
}

func (m *MockStorage) AddStat(stat storage.Stat) error {
	args := m.Called(stat)
	return args.Error(0)
}

func (m *MockStorage) GetStats() (map[int64]storage.Stat, error) {
	args := m.Called()
	return args.Get(0).(map[int64]storage.Stat), args.Error(1)
}

func (m *MockStorage) GetDashboardStats(userID int64) (*storage.DashboardStats, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.DashboardStats), args.Error(1)
}

func (m *MockStorage) AddRAGLog(log storage.RAGLog) error {
	args := m.Called(log)
	return args.Error(0)
}

func (m *MockStorage) GetRAGLogs(userID int64, limit int) ([]storage.RAGLog, error) {
	args := m.Called(userID, limit)
	return args.Get(0).([]storage.RAGLog), args.Error(1)
}

func (m *MockStorage) AddTopic(topic storage.Topic) (int64, error) {
	args := m.Called(topic)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) CreateTopic(topic storage.Topic) (int64, error) {
	args := m.Called(topic)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) ResetUserData(userID int64) error {
	args := m.Called(userID)
	return args.Error(0)
}

func (m *MockStorage) DeleteTopic(id int64) error {
	args := m.Called(id)
	return args.Error(0)
}

func (m *MockStorage) DeleteTopicCascade(id int64) error {
	args := m.Called(id)
	return args.Error(0)
}

func (m *MockStorage) GetLastTopicEndMessageID(userID int64) (int64, error) {
	args := m.Called(userID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) GetAllTopics() ([]storage.Topic, error) {
	args := m.Called()
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopicsByIDs(ids []int64) ([]storage.Topic, error) {
	args := m.Called(ids)
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopicsAfterID(minID int64) ([]storage.Topic, error) {
	args := m.Called(minID)
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopics(userID int64) ([]storage.Topic, error) {
	args := m.Called(userID)
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopicsPendingFacts(userID int64) ([]storage.Topic, error) {
	args := m.Called(userID)
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopicsExtended(filter storage.TopicFilter, limit, offset int, sortBy, sortDir string) (storage.TopicResult, error) {
	args := m.Called(filter, limit, offset, sortBy, sortDir)
	return args.Get(0).(storage.TopicResult), args.Error(1)
}

func (m *MockStorage) UpdateMessageTopic(messageID, topicID int64) error {
	args := m.Called(messageID, topicID)
	return args.Error(0)
}

func (m *MockStorage) SetTopicFactsExtracted(topicID int64, extracted bool) error {
	args := m.Called(topicID, extracted)
	return args.Error(0)
}

func (m *MockStorage) SetTopicConsolidationChecked(topicID int64, checked bool) error {
	args := m.Called(topicID, checked)
	return args.Error(0)
}

func (m *MockStorage) GetMergeCandidates(userID int64) ([]storage.MergeCandidate, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.MergeCandidate), args.Error(1)
}

func (m *MockStorage) GetMessagesInRange(ctx context.Context, userID int64, startID, endID int64) ([]storage.Message, error) {
	args := m.Called(ctx, userID, startID, endID)
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) GetMemoryBank(userID int64) (string, error) {
	args := m.Called(userID)
	return args.String(0), args.Error(1)
}

func (m *MockStorage) UpdateMemoryBank(userID int64, content string) error {
	args := m.Called(userID, content)
	return args.Error(0)
}

func (m *MockStorage) AddFact(fact storage.Fact) (int64, error) {
	args := m.Called(fact)
	if args.Get(0) == nil {
		return 0, args.Error(1)
	}
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) AddFactHistory(history storage.FactHistory) error {
	args := m.Called(history)
	return args.Error(0)
}

func (m *MockStorage) UpdateFactHistoryTopic(oldTopicID, newTopicID int64) error {
	args := m.Called(oldTopicID, newTopicID)
	return args.Error(0)
}

func (m *MockStorage) GetFactHistory(userID int64, limit int) ([]storage.FactHistory, error) {
	args := m.Called(userID, limit)
	return args.Get(0).([]storage.FactHistory), args.Error(1)
}

func (m *MockStorage) GetFactHistoryExtended(filter storage.FactHistoryFilter, limit, offset int, sortBy, sortDir string) (storage.FactHistoryResult, error) {
	args := m.Called(filter, limit, offset, sortBy, sortDir)
	return args.Get(0).(storage.FactHistoryResult), args.Error(1)
}

func (m *MockStorage) DeleteFact(userID, id int64) error {
	args := m.Called(userID, id)
	return args.Error(0)
}

func (m *MockStorage) UpdateFact(fact storage.Fact) error {
	args := m.Called(fact)
	return args.Error(0)
}

func (m *MockStorage) UpdateFactTopic(oldTopicID, newTopicID int64) error {
	args := m.Called(oldTopicID, newTopicID)
	return args.Error(0)
}

func (m *MockStorage) GetAllFacts() ([]storage.Fact, error) {
	args := m.Called()
	return args.Get(0).([]storage.Fact), args.Error(1)
}

func (m *MockStorage) GetFactsAfterID(minID int64) ([]storage.Fact, error) {
	args := m.Called(minID)
	return args.Get(0).([]storage.Fact), args.Error(1)
}

func (m *MockStorage) GetFactStats() (storage.FactStats, error) {
	args := m.Called()
	return args.Get(0).(storage.FactStats), args.Error(1)
}

func (m *MockStorage) GetFacts(userID int64) ([]storage.Fact, error) {
	args := m.Called(userID)
	return args.Get(0).([]storage.Fact), args.Error(1)
}

func (m *MockStorage) GetUnprocessedMessages(userID int64) ([]storage.Message, error) {
	// Fallback to avoid updating every single test expectation
	// This is a temporary measure because `m.Called` panics if no expectation is set.
	// We can't check m.ExpectedCalls easily.
	// But `Called` is the standard way.
	// The panic happens because we updated `buildContext` to call `GetUnprocessedMessages`
	// but the tests don't expect it.
	// I will update the tests to expect it.
	// But first, let's revert this method to standard form.
	args := m.Called(userID)
	if args.Get(0) == nil {
		return []storage.Message{}, args.Error(1)
	}
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) GetTopicExtractionLogs(limit, offset int) ([]storage.RAGLog, int, error) {
	args := m.Called(limit, offset)
	return args.Get(0).([]storage.RAGLog), args.Int(1), args.Error(2)
}

// Mock OpenRouter Client
type MockOpenRouterClient struct {
	mock.Mock
}

func (m *MockOpenRouterClient) CreateChatCompletion(ctx context.Context, req openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(openrouter.ChatCompletionResponse), args.Error(1)
}

func (m *MockOpenRouterClient) CreateEmbeddings(ctx context.Context, req openrouter.EmbeddingRequest) (openrouter.EmbeddingResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(openrouter.EmbeddingResponse), args.Error(1)
}

// Mock FileDownloader
type MockFileDownloader struct {
	mock.Mock
}

func (m *MockFileDownloader) DownloadFile(ctx context.Context, fileID string) ([]byte, error) {
	args := m.Called(ctx, fileID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockFileDownloader) DownloadFileAsBase64(ctx context.Context, fileID string) (string, error) {
	args := m.Called(ctx, fileID)
	return args.String(0), args.Error(1)
}

// Mock YandexClient
type MockYandexClient struct {
	mock.Mock
}

func (m *MockYandexClient) Recognize(ctx context.Context, audioData []byte) (string, error) {
	args := m.Called(ctx, audioData)
	return args.String(0), args.Error(1)
}

func createTestTranslator(t *testing.T) *i18n.Translator {
	tmpDir := t.TempDir()
	content := `
telegram:
  forwarded_from: "[Переслано от %s пользователем %s в %s]"
bot:
  voice_recognition_prefix: "(Распознано из аудио):"
  voice_message_marker: "[Voice message]"
  voice_instruction: "The user sent a voice message (audio file below). Listen to it and respond in English. Do not describe the listening process — just respond to the content."
  system_prompt: "System"
memory:
  facts_user_header: "=== Facts about User ==="
  facts_others_header: "=== Facts about Others ==="
`
	_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte(content), 0644)
	tr, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")
	return tr
}

func TestProcessMessageGroup_ForwardedMessages(t *testing.T) {
	// Setup
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	mockStore := new(MockStorage)
	mockORClient := new(MockOpenRouterClient)
	cfg := &config.Config{
		Bot: config.BotConfig{
			SystemPrompt: "Test prompt",
		},
		OpenRouter: config.OpenRouterConfig{
			Model: "test-model",
		},
	}

	mockDownloader := new(MockFileDownloader)
	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		logRepo:         mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		downloader:      mockDownloader,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}
	bot.messageGrouper = NewMessageGrouper(bot, logger, 0, bot.processMessageGroup)

	userID := int64(123)
	chatID := int64(456)
	now := int(time.Now().Unix())
	forwarderUser := &telegram.User{ID: 789, FirstName: "Forwarder", Username: "fwd_user"}
	forwarderChat := &telegram.Chat{ID: 987, Title: "Forward Channel", Username: "fwd_channel"}

	// Test Data
	messages := []*telegram.Message{
		{
			MessageID: 1, Text: "Original message 1",
			From: &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
			Chat: &telegram.Chat{ID: chatID}, Date: now,
		},
		{
			MessageID: 2, Text: "Forwarded from user",
			From: &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
			Chat: &telegram.Chat{ID: chatID}, Date: now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type:       "user",
				Date:       now - 3600,
				SenderUser: forwarderUser,
			},
		},
		{
			MessageID: 3, Text: "Forwarded from channel",
			From: &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
			Chat: &telegram.Chat{ID: chatID}, Date: now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type:       "channel",
				Date:       now - 7200,
				SenderChat: forwarderChat,
			},
		},
	}

	// Mock expectations
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)

	expectedHistoryContent := fmt.Sprintf("%s\n%s\n%s",
		messages[0].BuildContent(translator, "en"),
		messages[1].BuildContent(translator, "en"),
		messages[2].BuildContent(translator, "en"),
	)

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: expectedHistoryContent},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(msg storage.Message) bool {
		return msg.Role == "user" && msg.Content == expectedHistoryContent
	})).Return(nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "assistant"
	})).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)

	// Mock API calls
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil)

	// Mock OpenRouter call
	var capturedRequest openrouter.ChatCompletionRequest
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		capturedRequest = args.Get(1).(openrouter.ChatCompletionRequest)
	}).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{Role: "assistant", Content: "Test response"}},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{TotalTokens: 10},
	}, nil)

	// Execute
	group := &MessageGroup{
		Messages: messages,
		UserID:   userID,
	}
	bot.processMessageGroup(context.Background(), group)

	// Assertions
	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)

	assert.Len(t, capturedRequest.Messages, 2) // system + user
	userContent := capturedRequest.Messages[1].Content
	contentParts, ok := userContent.([]interface{})
	assert.True(t, ok)
	// Now we expect one text part for each message.
	assert.Len(t, contentParts, len(messages))

	// Check the formatted text parts
	for i, part := range contentParts {
		// The structure is now flat: a slice of parts, not a slice of slices of parts.
		// Each part can be a TextPart, ImagePart, etc.
		// In this specific test, we only have text.
		textPart, ok := part.(openrouter.TextPart)
		assert.True(t, ok, "Part should be a TextPart")
		assert.Equal(t, messages[i].BuildContent(translator, "en"), textPart.Text)
	}
}

func TestProcessMessageGroup_PhotoMessage(t *testing.T) {
	// Setup
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	mockStore := new(MockStorage)
	mockORClient := new(MockOpenRouterClient)
	cfg := &config.Config{
		Bot: config.BotConfig{
			SystemPrompt: "Test prompt",
		},
		OpenRouter: config.OpenRouterConfig{
			Model: "test-model",
		},
	}

	mockDownloader := new(MockFileDownloader)
	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		logRepo:         mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		downloader:      mockDownloader,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}
	bot.messageGrouper = NewMessageGrouper(bot, logger, 0, bot.processMessageGroup)

	userID := int64(123)
	chatID := int64(456)
	now := int(time.Now().Unix())
	photoFileID := "photo_file_id_123"
	encodedPhotoData := "ZmFrZV9pbWFnZV9ieXRlcw==" // base64 of "fake_image_bytes"

	// Test Data
	messages := []*telegram.Message{
		{
			MessageID: 1,
			Caption:   "Check out this photo!",
			From:      &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
			Chat:      &telegram.Chat{ID: chatID},
			Date:      now,
			Photo: []telegram.PhotoSize{
				{FileID: "small_photo", Width: 100, Height: 100},
				{FileID: photoFileID, Width: 500, Height: 500}, // This one should be picked
			},
		},
	}

	// Mock expectations
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)

	expectedHistoryContent := messages[0].BuildContent(translator, "en")
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: expectedHistoryContent},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(msg storage.Message) bool {
		return msg.Role == "user" && msg.Content == expectedHistoryContent
	})).Return(nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "assistant"
	})).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)

	// Mock API calls
	mockDownloader.On("DownloadFileAsBase64", mock.Anything, photoFileID).Return(encodedPhotoData, nil)
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil)

	// Mock OpenRouter call
	var capturedRequest openrouter.ChatCompletionRequest
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		capturedRequest = args.Get(1).(openrouter.ChatCompletionRequest)
	}).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{Role: "assistant", Content: "Test response"}},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{TotalTokens: 10},
	}, nil)

	// Execute
	group := &MessageGroup{
		Messages: messages,
		UserID:   userID,
	}
	bot.processMessageGroup(context.Background(), group)

	// Assertions
	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
	mockDownloader.AssertExpectations(t)

	assert.Len(t, capturedRequest.Messages, 2) // System + User
	userContent := capturedRequest.Messages[1].Content
	contentParts, ok := userContent.([]interface{})
	assert.True(t, ok)
	assert.Len(t, contentParts, 2) // Text part + Image part

	// Check text part
	textPart, ok := contentParts[0].(openrouter.TextPart)
	assert.True(t, ok)
	expectedText := messages[0].BuildContent(translator, "en")
	assert.Equal(t, expectedText, textPart.Text)

	// Check image part
	imagePart, ok := contentParts[1].(openrouter.ImagePart)
	assert.True(t, ok)
	assert.Equal(t, "image_url", imagePart.Type)
	expectedImageURL := fmt.Sprintf("data:image/jpeg;base64,%s", encodedPhotoData)
	assert.Equal(t, expectedImageURL, imagePart.ImageURL.URL)
}

func TestProcessMessageGroup_DocumentAsImageMessage(t *testing.T) {
	// Setup
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	mockStore := new(MockStorage)
	mockORClient := new(MockOpenRouterClient)
	cfg := &config.Config{
		Bot: config.BotConfig{
			SystemPrompt: "Test prompt",
		},
		OpenRouter: config.OpenRouterConfig{
			Model: "test-model",
		},
	}

	mockDownloader := new(MockFileDownloader)
	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		logRepo:         mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		downloader:      mockDownloader,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}
	bot.messageGrouper = NewMessageGrouper(bot, logger, 0, bot.processMessageGroup)

	userID := int64(123)
	chatID := int64(456)
	now := int(time.Now().Unix())
	docFileID := "doc_file_id_456"
	encodedDocData := "ZmFrZV9kb2NfaW1hZ2VfYnl0ZXM=" // base64 of "fake_doc_image_bytes"

	// Test Data
	messages := []*telegram.Message{
		{
			MessageID: 1,
			Caption:   "Check out this document image!",
			From:      &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
			Chat:      &telegram.Chat{ID: chatID},
			Date:      now,
			Document: &telegram.Document{
				FileID:   docFileID,
				MimeType: "image/png",
				FileName: "image.png",
			},
		},
	}

	// Mock expectations
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)

	expectedHistoryContent := messages[0].BuildContent(translator, "en")
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: expectedHistoryContent},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(msg storage.Message) bool {
		return msg.Role == "user" && msg.Content == expectedHistoryContent
	})).Return(nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "assistant"
	})).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)

	// Mock API calls
	mockDownloader.On("DownloadFileAsBase64", mock.Anything, docFileID).Return(encodedDocData, nil)
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil)

	// Mock OpenRouter call
	var capturedRequest openrouter.ChatCompletionRequest
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		capturedRequest = args.Get(1).(openrouter.ChatCompletionRequest)
	}).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{Role: "assistant", Content: "Test response"}},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{TotalTokens: 10},
	}, nil)

	// Execute
	group := &MessageGroup{
		Messages: messages,
		UserID:   userID,
	}
	bot.processMessageGroup(context.Background(), group)

	// Assertions
	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
	mockDownloader.AssertExpectations(t)

	assert.Len(t, capturedRequest.Messages, 2) // System + User
	userContent := capturedRequest.Messages[1].Content
	contentParts, ok := userContent.([]interface{})
	assert.True(t, ok)
	assert.Len(t, contentParts, 2) // Text part + Image part

	// Check text part
	textPart, ok := contentParts[0].(openrouter.TextPart)
	assert.True(t, ok)
	expectedText := messages[0].BuildContent(translator, "en")
	assert.Equal(t, expectedText, textPart.Text)

	// Check image part
	imagePart, ok := contentParts[1].(openrouter.ImagePart)
	assert.True(t, ok)
	assert.Equal(t, "image_url", imagePart.Type)
	expectedImageURL := fmt.Sprintf("data:image/png;base64,%s", encodedDocData)
	assert.Equal(t, expectedImageURL, imagePart.ImageURL.URL)
}

func TestProcessMessageGroup_PDFMessage(t *testing.T) {
	// Setup
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	mockStore := new(MockStorage)
	mockORClient := new(MockOpenRouterClient)
	cfg := &config.Config{
		Bot: config.BotConfig{
			SystemPrompt: "Test prompt",
		},
		OpenRouter: config.OpenRouterConfig{
			Model:           "test-model",
			PDFParserEngine: "native",
		},
	}

	mockDownloader := new(MockFileDownloader)
	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		logRepo:         mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		downloader:      mockDownloader,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}
	bot.messageGrouper = NewMessageGrouper(bot, logger, 0, bot.processMessageGroup)

	userID := int64(123)
	chatID := int64(456)
	now := int(time.Now().Unix())
	pdfFileID := "pdf_file_id_789"
	encodedPDFData := "ZmFrZV9wZGZfYnl0ZXM=" // base64 of "fake_pdf_bytes"

	// Test Data
	messages := []*telegram.Message{
		{
			MessageID: 1,
			Caption:   "Check out this PDF!",
			From:      &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
			Chat:      &telegram.Chat{ID: chatID},
			Date:      now,
			Document: &telegram.Document{
				FileID:   pdfFileID,
				MimeType: "application/pdf",
				FileName: "document.pdf",
			},
		},
	}

	// Mock expectations
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)

	expectedHistoryContent := messages[0].BuildContent(translator, "en")
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: expectedHistoryContent},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(msg storage.Message) bool {
		return msg.Role == "user" && msg.Content == expectedHistoryContent
	})).Return(nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "assistant"
	})).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)

	// Mock API calls
	mockDownloader.On("DownloadFileAsBase64", mock.Anything, pdfFileID).Return(encodedPDFData, nil)
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil)

	// Mock OpenRouter call
	var capturedRequest openrouter.ChatCompletionRequest
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		capturedRequest = args.Get(1).(openrouter.ChatCompletionRequest)
	}).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{Role: "assistant", Content: "Test response"}},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{TotalTokens: 10},
	}, nil)

	// Execute
	group := &MessageGroup{
		Messages: messages,
		UserID:   userID,
	}
	bot.processMessageGroup(context.Background(), group)

	// Assertions
	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
	mockDownloader.AssertExpectations(t)

	assert.Len(t, capturedRequest.Messages, 2) // System + User
	userContent := capturedRequest.Messages[1].Content
	contentParts, ok := userContent.([]interface{})
	assert.True(t, ok)
	assert.Len(t, contentParts, 2) // Text part + File part

	// Check text part
	textPart, ok := contentParts[0].(openrouter.TextPart)
	assert.True(t, ok)
	expectedText := messages[0].BuildContent(translator, "en")
	assert.Equal(t, expectedText, textPart.Text)

	// Check file part
	filePart, ok := contentParts[1].(openrouter.FilePart)
	assert.True(t, ok)
	assert.Equal(t, "file", filePart.Type)
	assert.Equal(t, "document.pdf", filePart.File.FileName)
	expectedFileData := fmt.Sprintf("data:application/pdf;base64,%s", encodedPDFData)
	assert.Equal(t, expectedFileData, filePart.File.FileData)

	// Check plugins
	assert.Len(t, capturedRequest.Plugins, 1)
	assert.Equal(t, "file-parser", capturedRequest.Plugins[0].ID)
	assert.Equal(t, "native", capturedRequest.Plugins[0].PDF.Engine)
}

func TestProcessMessageGroup_TextDocumentMessage(t *testing.T) {
	// Setup
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	mockStore := new(MockStorage)
	mockORClient := new(MockOpenRouterClient)
	cfg := &config.Config{
		Bot: config.BotConfig{
			SystemPrompt: "Test prompt",
		},
		OpenRouter: config.OpenRouterConfig{
			Model: "test-model",
		},
	}

	mockDownloader := new(MockFileDownloader)
	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		logRepo:         mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		downloader:      mockDownloader,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}
	bot.messageGrouper = NewMessageGrouper(bot, logger, 0, bot.processMessageGroup)

	userID := int64(123)
	chatID := int64(456)
	now := int(time.Now().Unix())
	docFileID := "doc_file_id_txt"
	docData := "This is the content of the text file."
	docFileName := "info.txt"

	// Test Data
	messages := []*telegram.Message{
		{
			MessageID: 1,
			Caption:   "What is in this file?",
			From:      &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
			Chat:      &telegram.Chat{ID: chatID},
			Date:      now,
			Document: &telegram.Document{
				FileID:   docFileID,
				MimeType: "text/plain",
				FileName: docFileName,
			},
		},
	}

	// Mock expectations
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)

	// The history content will now include the file content directly.
	expectedFileTextContent := fmt.Sprintf("%s:\n\n%s", docFileName, docData)
	expectedHistoryContent := fmt.Sprintf("%s\n%s", messages[0].BuildContent(translator, "en"), expectedFileTextContent)

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: expectedHistoryContent},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(msg storage.Message) bool {
		return msg.Role == "user" && msg.Content == expectedHistoryContent
	})).Return(nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "assistant"
	})).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)

	// Mock API calls
	mockDownloader.On("DownloadFile", mock.Anything, docFileID).Return([]byte(docData), nil)
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil)

	// Mock OpenRouter call
	var capturedRequest openrouter.ChatCompletionRequest
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		capturedRequest = args.Get(1).(openrouter.ChatCompletionRequest)
	}).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{Role: "assistant", Content: "Test response"}},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{TotalTokens: 10},
	}, nil)

	// Execute
	group := &MessageGroup{
		Messages: messages,
		UserID:   userID,
	}
	bot.processMessageGroup(context.Background(), group)

	// Assertions
	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
	mockDownloader.AssertExpectations(t)

	assert.Len(t, capturedRequest.Messages, 2) // System + User
	userContent := capturedRequest.Messages[1].Content
	contentParts, ok := userContent.([]interface{})
	assert.True(t, ok)
	assert.Len(t, contentParts, 2) // Text part from caption + Text part from document

	// Check caption text part
	captionPart, ok := contentParts[0].(openrouter.TextPart)
	assert.True(t, ok)
	expectedCaptionText := messages[0].BuildContent(translator, "en")
	assert.Equal(t, expectedCaptionText, captionPart.Text)

	// Check document text part
	docPart, ok := contentParts[1].(openrouter.TextPart)
	assert.True(t, ok)
	assert.Equal(t, "text", docPart.Type)
	assert.Equal(t, expectedFileTextContent, docPart.Text)
}

// TestProcessMessageGroup_VoiceMessage tests that voice messages are properly
// sent as native audio to the LLM (Gemini supports audio directly).
func TestProcessMessageGroup_VoiceMessage(t *testing.T) {
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	mockStore := new(MockStorage)
	mockORClient := new(MockOpenRouterClient)
	mockDownloader := new(MockFileDownloader)

	cfg := &config.Config{
		Bot: config.BotConfig{
			SystemPrompt: "Test prompt",
			Language:     "en",
		},
		OpenRouter: config.OpenRouterConfig{
			Model: "test-model",
		},
	}

	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		logRepo:         mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		downloader:      mockDownloader,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}
	bot.messageGrouper = NewMessageGrouper(bot, logger, 0, bot.processMessageGroup)

	userID := int64(123)
	chatID := int64(456)
	now := int(time.Now().Unix())
	voiceFileID := "voice_file_id_789"
	voiceBase64 := "ZmFrZV92b2ljZV9kYXRh" // base64 of "fake_voice_data"

	msg := &telegram.Message{
		MessageID: 1,
		From:      &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
		Chat:      &telegram.Chat{ID: chatID},
		Date:      now,
		Voice:     &telegram.Voice{FileID: voiceFileID, MimeType: "audio/ogg"},
	}

	// Mock expectations - now uses DownloadFileAsBase64 for native audio
	mockDownloader.On("DownloadFileAsBase64", mock.Anything, voiceFileID).Return(voiceBase64, nil)

	// Build expected content with voice marker
	voiceMarker := translator.Get("en", "bot.voice_message_marker")
	prefix := msg.BuildPrefix(translator, "en")
	expectedHistoryContent := fmt.Sprintf("%s: %s", prefix, voiceMarker)

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: expectedHistoryContent},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "user"
	})).Return(nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "assistant"
	})).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)

	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil)

	var capturedRequest openrouter.ChatCompletionRequest
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		capturedRequest = args.Get(1).(openrouter.ChatCompletionRequest)
	}).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{Role: "assistant", Content: "Test response"}},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{TotalTokens: 10},
	}, nil)

	// Execute - voice messages now go through processMessageGroup
	group := &MessageGroup{
		Messages: []*telegram.Message{msg},
		UserID:   userID,
	}
	bot.processMessageGroup(context.Background(), group)

	// Assertions
	mockDownloader.AssertCalled(t, "DownloadFileAsBase64", mock.Anything, voiceFileID)
	mockORClient.AssertCalled(t, "CreateChatCompletion", mock.Anything, mock.Anything)

	assert.Len(t, capturedRequest.Messages, 2) // System + User
	userContent := capturedRequest.Messages[1].Content
	contentParts, ok := userContent.([]interface{})
	assert.True(t, ok)
	assert.Len(t, contentParts, 2) // TextPart (instruction) + FilePart (audio)

	// Verify voice instruction is sent as TextPart
	textPart, ok := contentParts[0].(openrouter.TextPart)
	assert.True(t, ok, "expected TextPart for voice instruction")
	assert.Equal(t, "text", textPart.Type)
	assert.Contains(t, textPart.Text, "voice message")

	// Verify audio is sent as FilePart
	filePart, ok := contentParts[1].(openrouter.FilePart)
	assert.True(t, ok, "expected FilePart for voice message")
	assert.Equal(t, "file", filePart.Type)
	assert.Equal(t, "voice.ogg", filePart.File.FileName)
	assert.Contains(t, filePart.File.FileData, "data:audio/ogg;base64,")
	assert.Contains(t, filePart.File.FileData, voiceBase64)
}

func TestProcessUpdate(t *testing.T) {
	// Setup
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	mockStore := new(MockStorage)
	cfg := &config.Config{
		Bot: config.BotConfig{
			AllowedUserIDs: []int64{123},
		},
	}

	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		logRepo:         mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}
	// Initialize messageGrouper with a dummy callback
	bot.messageGrouper = NewMessageGrouper(bot, logger, 0, func(ctx context.Context, group *MessageGroup) {})

	update := &telegram.Update{
		UpdateID: 1,
		Message: &telegram.Message{
			MessageID: 1,
			From:      &telegram.User{ID: 123, Username: "user", FirstName: "Test", LastName: "User"},
			Chat:      &telegram.Chat{ID: 123},
			Text:      "Hello",
		},
	}

	// Expectations
	mockStore.On("UpsertUser", mock.MatchedBy(func(u storage.User) bool {
		return u.ID == 123 && u.Username == "user"
	})).Return(nil)

	// Execute
	bot.ProcessUpdate(context.Background(), update, "test")

	// Assertions
	mockStore.AssertExpectations(t)
}

func TestGetTieredCost(t *testing.T) {
	testTiers := []config.PriceTier{
		{UpToTokens: 200000, PromptCost: 1.25, CompletionCost: 10.0},
		{UpToTokens: 1048576, PromptCost: 2.50, CompletionCost: 15.0},
	}

	const requestCost = 0.01 // $0.01 per request for testing

	baseCfg := &config.Config{}
	baseCfg.OpenRouter.RequestCost = requestCost
	baseCfg.OpenRouter.PriceTiers = testTiers

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	testCases := []struct {
		name               string
		promptTokens       int
		completionTokens   int
		expectedCost       float64
		configTiers        []config.PriceTier
		expectWarning      bool
		expectedWarningMsg string
	}{
		{
			name:               "No Tiers Configured",
			promptTokens:       1000,
			completionTokens:   1000,
			expectedCost:       0.0,
			configTiers:        []config.PriceTier{},
			expectWarning:      true,
			expectedWarningMsg: "no price tiers configured",
		},
		{
			name:             "First Tier",
			promptTokens:     100000, // 100k tokens
			completionTokens: 10000,  // 10k tokens
			// (100000 / 1M) * 1.25 + (10000 / 1M) * 10.0 + 0.01
			// 0.1 * 1.25 + 0.01 * 10.0 + 0.01
			// 0.125 + 0.1 + 0.01 = 0.235
			expectedCost: 0.235,
			configTiers:  testTiers,
		},
		{
			name:             "Second Tier",
			promptTokens:     300000, // 300k tokens
			completionTokens: 20000,  // 20k tokens
			// (300000 / 1M) * 2.50 + (20000 / 1M) * 15.0 + 0.01
			// 0.3 * 2.50 + 0.02 * 15.0 + 0.01
			// 0.75 + 0.3 + 0.01 = 1.06
			expectedCost: 1.06,
			configTiers:  testTiers,
		},
		{
			name:             "Edge Case - Upper Boundary of First Tier",
			promptTokens:     200000, // Exactly 200k
			completionTokens: 1,
			// (200000 / 1M) * 1.25 + (1 / 1M) * 10.0 + 0.01
			// 0.2 * 1.25 + 0.000001 * 10.0 + 0.01
			// 0.25 + 0.00001 + 0.01 = 0.26001
			expectedCost: 0.26001,
			configTiers:  testTiers,
		},
		{
			name:             "Exceeding All Tiers",
			promptTokens:     2000000, // 2M tokens
			completionTokens: 100000,  // 100k tokens
			// Should use the highest tier's prices
			// (2000000 / 1M) * 2.50 + (100000 / 1M) * 15.0 + 0.01
			// 2 * 2.50 + 0.1 * 15.0 + 0.01
			// 5.0 + 1.5 + 0.01 = 6.51
			expectedCost: 6.51,
			configTiers:  testTiers,
		},
		{
			name:             "Zero Tokens",
			promptTokens:     0,
			completionTokens: 0,
			// Should only be the request cost
			expectedCost: requestCost,
			configTiers:  testTiers,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup logger to capture output if needed
			var logBuf bytes.Buffer
			var testLogger *slog.Logger
			if tc.expectWarning {
				testLogger = slog.New(slog.NewJSONHandler(&logBuf, nil))
			} else {
				testLogger = logger // Discard logs
			}

			// Create a temporary bot with the specific config for the test case
			testCfg := &config.Config{}
			testCfg.OpenRouter.RequestCost = requestCost
			testCfg.OpenRouter.PriceTiers = tc.configTiers
			testBot := &Bot{cfg: testCfg, logger: testLogger}

			cost := testBot.getTieredCost(tc.promptTokens, tc.completionTokens, testLogger)

			assert.InDelta(t, tc.expectedCost, cost, 0.000001, "Cost calculation is incorrect")

			if tc.expectWarning {
				assert.Contains(t, logBuf.String(), tc.expectedWarningMsg)
			}
		})
	}
}

func TestProcessMessageGroup_HistoryIntegration(t *testing.T) {
	// Setup
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	mockStore := new(MockStorage)
	mockORClient := new(MockOpenRouterClient)
	cfg := &config.Config{
		Bot: config.BotConfig{
			SystemPrompt: "System",
		},
		OpenRouter: config.OpenRouterConfig{
			Model: "test-model",
		},
	}
	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		logRepo:         mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(1)
	chatID := int64(1)
	now := int(time.Now().Unix())

	// History of messages
	history := []storage.Message{
		{Role: "user", Content: fmt.Sprintf("[User1 (@user1) (%s)]: Hello", time.Unix(int64(now-200), 0).Format("2006-01-02 15:04:05"))},
		{Role: "assistant", Content: "Hi there!"},
		{Role: "user", Content: fmt.Sprintf("[Переслано от Channel (@channel) пользователем User1 (@user1) в %s]: News", time.Unix(int64(now-100), 0).Format("2006-01-02 15:04:05"))},
		{Role: "assistant", Content: "Interesting news!"},
	}

	// New message to process
	newMessage := &telegram.Message{
		MessageID: 100, Text: "What was the first thing I said?",
		From: &telegram.User{ID: userID, FirstName: "User1", Username: "user1"},
		Chat: &telegram.Chat{ID: chatID}, Date: now,
	}

	// --- Mocking ---
	// 1. When we process the new message, it will be added to history
	newMessageContent := newMessage.BuildContent(translator, "en")
	mockStore.On("AddMessageToHistory", userID, storage.Message{Role: "user", Content: newMessageContent}).Return(nil)

	// 2. Then, the bot will fetch the full history
	fullHistory := append(history, storage.Message{Role: "user", Content: newMessageContent})
	mockStore.On("GetUnprocessedMessages", userID).Return(fullHistory, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// 3. The bot will save the assistant's response
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "assistant"
	})).Return(nil)

	// 4. Other mocks
	mockStore.On("AddStat", mock.Anything).Return(nil)
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil)

	// --- Capture the request to OpenRouter ---
	var capturedRequest openrouter.ChatCompletionRequest
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		capturedRequest = args.Get(1).(openrouter.ChatCompletionRequest)
	}).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{Role: "assistant", Content: "You said hello."}},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{TotalTokens: 100},
	}, nil)

	// --- Execution ---
	group := &MessageGroup{
		Messages: []*telegram.Message{newMessage},
		UserID:   userID,
	}
	bot.processMessageGroup(context.Background(), group)

	// --- Assertions ---
	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)

	// Check that the history sent to the model is correct and complete
	assert.Len(t, capturedRequest.Messages, len(fullHistory)+1) // +1 for system prompt

	// Check system prompt
	systemContent, ok := capturedRequest.Messages[0].Content.([]interface{})[0].(openrouter.TextPart)
	assert.True(t, ok)
	assert.Equal(t, "System", systemContent.Text)

	// Check that all historical messages were passed correctly
	for i, hMsg := range fullHistory {
		// The last message is the one with all the parts, others are simple text
		if i == len(fullHistory)-1 {
			// This is the current message being processed
			currentContent, ok := capturedRequest.Messages[i+1].Content.([]interface{})
			assert.True(t, ok)
			assert.Len(t, currentContent, 1)
			textPart, ok := currentContent[0].(openrouter.TextPart)
			assert.True(t, ok)
			assert.Equal(t, hMsg.Content, textPart.Text)
		} else {
			// These are old messages from history
			historicalContent, ok := capturedRequest.Messages[i+1].Content.([]interface{})[0].(openrouter.TextPart)
			assert.True(t, ok)
			assert.Equal(t, hMsg.Content, historicalContent.Text)
		}
	}
}

func TestSendResponses_Success(t *testing.T) {
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	cfg := &config.Config{}

	bot := &Bot{
		api:        mockAPI,
		cfg:        cfg,
		logger:     logger,
		translator: translator,
	}

	chatID := int64(123)
	responses := []telegram.SendMessageRequest{
		{ChatID: chatID, Text: "Hello", ParseMode: "MarkdownV2"},
		{ChatID: chatID, Text: "World", ParseMode: "MarkdownV2"},
	}

	mockAPI.On("SendMessage", mock.Anything, responses[0]).Return(&telegram.Message{}, nil)
	mockAPI.On("SendMessage", mock.Anything, responses[1]).Return(&telegram.Message{}, nil)

	bot.sendResponses(context.Background(), chatID, responses, logger)

	mockAPI.AssertExpectations(t)
}

func TestSendResponses_ParseError_RetrySuccess(t *testing.T) {
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	cfg := &config.Config{
		Bot: config.BotConfig{Language: "en"},
	}

	bot := &Bot{
		api:        mockAPI,
		cfg:        cfg,
		logger:     logger,
		translator: translator,
	}

	chatID := int64(123)
	responses := []telegram.SendMessageRequest{
		{ChatID: chatID, Text: "Hello *broken* markdown", ParseMode: "MarkdownV2"},
	}

	// First call fails with parse error
	mockAPI.On("SendMessage", mock.Anything, responses[0]).Return(nil, fmt.Errorf("can't parse entities")).Once()

	// Retry without ParseMode should succeed
	retryReq := telegram.SendMessageRequest{ChatID: chatID, Text: "Hello *broken* markdown", ParseMode: ""}
	mockAPI.On("SendMessage", mock.Anything, retryReq).Return(&telegram.Message{}, nil).Once()

	bot.sendResponses(context.Background(), chatID, responses, logger)

	mockAPI.AssertExpectations(t)
}

func TestSendResponses_OtherError_SendGenericError(t *testing.T) {
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	cfg := &config.Config{
		Bot: config.BotConfig{Language: "en"},
	}

	bot := &Bot{
		api:        mockAPI,
		cfg:        cfg,
		logger:     logger,
		translator: translator,
	}

	chatID := int64(123)
	responses := []telegram.SendMessageRequest{
		{ChatID: chatID, Text: "Hello", ParseMode: "MarkdownV2"},
	}

	// First call fails with network error
	mockAPI.On("SendMessage", mock.Anything, responses[0]).Return(nil, fmt.Errorf("network error")).Once()

	// Send generic error message
	mockAPI.On("SendMessage", mock.Anything, mock.MatchedBy(func(req telegram.SendMessageRequest) bool {
		return req.ChatID == chatID && req.Text == translator.Get("en", "bot.generic_error")
	})).Return(&telegram.Message{}, nil).Once()

	bot.sendResponses(context.Background(), chatID, responses, logger)

	mockAPI.AssertExpectations(t)
}

func TestSendTestMessage_Success(t *testing.T) {
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	mockStore := new(MockStorage)
	mockORClient := new(MockOpenRouterClient)

	cfg := &config.Config{
		Bot: config.BotConfig{
			SystemPrompt: "Test prompt",
			Language:     "en",
		},
		OpenRouter: config.OpenRouterConfig{
			Model: "test-model",
		},
		RAG: config.RAGConfig{
			Enabled: false, // Disable RAG for testing
		},
	}

	// Create a minimal RAG service with RAG disabled
	ragService := rag.NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockORClient, nil, translator)

	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		logRepo:         mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		ragService:      ragService,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)
	testMessage := "Hello, bot!"

	// Mock storage calls
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(msg storage.Message) bool {
		return msg.Role == "user" && msg.Content == testMessage
	})).Return(nil)
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: testMessage},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(msg storage.Message) bool {
		return msg.Role == "assistant"
	})).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)

	// Mock LLM call
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{Role: "assistant", Content: "Hello! How can I help you?"}},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{
			PromptTokens:     100,
			CompletionTokens: 20,
		},
	}, nil)

	result, err := bot.SendTestMessage(context.Background(), userID, testMessage, true)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "Hello! How can I help you?", result.Response)
	assert.Equal(t, 100, result.PromptTokens)
	assert.Equal(t, 20, result.CompletionTokens)
	assert.GreaterOrEqual(t, result.TimingTotal.Nanoseconds(), int64(0))
	assert.Equal(t, 0, result.TopicsMatched) // RAG disabled
	assert.Equal(t, 0, result.FactsInjected) // RAG disabled

	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

func TestSendTestMessage_SaveToHistoryFalse(t *testing.T) {
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	mockStore := new(MockStorage)
	mockORClient := new(MockOpenRouterClient)

	cfg := &config.Config{
		Bot: config.BotConfig{
			SystemPrompt: "Test prompt",
			Language:     "en",
		},
		OpenRouter: config.OpenRouterConfig{
			Model: "test-model",
		},
		RAG: config.RAGConfig{
			Enabled: false,
		},
	}

	ragService := rag.NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockORClient, nil, translator)

	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		logRepo:         mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		ragService:      ragService,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)
	testMessage := "Hello, bot!"

	// When saveToHistory is false, AddMessageToHistory should NOT be called
	// But we still need to mock the context building calls
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// Mock LLM call
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(openrouter.ChatCompletionResponse{
		Choices: []struct {
			Message struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			} `json:"message"`
		}{
			{Message: struct {
				Role             string                `json:"role"`
				Content          string                `json:"content"`
				ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
				ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
			}{Role: "assistant", Content: "Response without history"}},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{
			PromptTokens:     50,
			CompletionTokens: 10,
		},
	}, nil)

	result, err := bot.SendTestMessage(context.Background(), userID, testMessage, false)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "Response without history", result.Response)

	// Verify AddMessageToHistory was NOT called
	mockStore.AssertNotCalled(t, "AddMessageToHistory", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "AddStat", mock.Anything)
	mockStore.AssertNotCalled(t, "AddRAGLog", mock.Anything)
}

func TestSendTestMessage_OpenRouterError(t *testing.T) {
	translator := createTestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(MockBotAPI)
	mockStore := new(MockStorage)
	mockORClient := new(MockOpenRouterClient)

	cfg := &config.Config{
		Bot: config.BotConfig{
			SystemPrompt: "Test prompt",
			Language:     "en",
		},
		OpenRouter: config.OpenRouterConfig{
			Model: "test-model",
		},
		RAG: config.RAGConfig{
			Enabled: false,
		},
	}

	ragService := rag.NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockORClient, nil, translator)

	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		logRepo:         mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		ragService:      ragService,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)
	testMessage := "Hello, bot!"

	// Mock storage calls
	mockStore.On("AddMessageToHistory", userID, mock.Anything).Return(nil)
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: testMessage},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// Mock LLM call to return error
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		openrouter.ChatCompletionResponse{},
		fmt.Errorf("API rate limit exceeded"),
	)

	result, err := bot.SendTestMessage(context.Background(), userID, testMessage, true)

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to get completion")
}

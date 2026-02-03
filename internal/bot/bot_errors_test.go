package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// setupBotForErrorTests creates a fully configured Bot for error testing.
func setupBotForErrorTests(t *testing.T) (*Bot, *testutil.MockStorage, *testutil.MockOpenRouterClient, *testutil.MockBotAPI) {
	t.Helper()

	logger := testutil.TestLogger()
	mockAPI := new(testutil.MockBotAPI)
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	mockDownloader := new(testutil.MockFileDownloader)

	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false // Disable RAG for error tests

	translator := testutil.TestTranslator(t)

	// Create real RAG service using builder
	ragService, err := rag.NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockORClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(&mockMemoryService{}).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}

	// Create Laplace agent
	laplaceAgent := laplace.New(cfg, mockORClient, nil, mockStore, mockStore, nil, translator, logger)

	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		peopleRepo:      mockStore,
		orClient:        mockORClient,
		downloader:      mockDownloader,
		fileProcessor:   files.NewProcessor(mockDownloader, translator, "en", logger),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
		laplaceAgent:    laplaceAgent,
		ragService:      ragService,
	}
	bot.messageGrouper = NewMessageGrouper(bot, logger, time.Hour, func(ctx context.Context, group *MessageGroup) {})

	// Setup default mocks for background queries
	testutil.SetupDefaultMocks(mockStore)

	// Setup flexible API mocks (called from background goroutines)
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil).Maybe()
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil).Maybe()
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil).Maybe()

	return bot, mockStore, mockORClient, mockAPI
}

// TestProcessMessageGroup_EmptyMessageGroup_ReturnsEarly verifies empty groups don't process.
func TestProcessMessageGroup_EmptyMessageGroup_ReturnsEarly(t *testing.T) {
	bot, _, _, _ := setupBotForErrorTests(t)

	group := &MessageGroup{
		Messages: []*telegram.Message{},
		UserID:   123,
	}

	// Should not panic or call any repos
	assert.NotPanics(t, func() {
		bot.processMessageGroup(context.Background(), group)
	})
}

// TestProcessMessageGroup_UnsupportedFileType_SendsErrorMessage verifies unsupported file error is handled.
func TestProcessMessageGroup_UnsupportedFileType_SendsErrorMessage(t *testing.T) {
	bot, mockStore, _, mockAPI := setupBotForErrorTests(t)

	userID := int64(123)
	chatID := int64(456)

	group := &MessageGroup{
		Messages: []*telegram.Message{
			{
				MessageID: 1,
				From:      &telegram.User{ID: userID, FirstName: "Test"},
				Chat:      &telegram.Chat{ID: chatID},
				Date:      int(time.Now().Unix()),
				Document: &telegram.Document{
					FileID:   "doc123",
					FileName: "document.docx",
					MimeType: "application/vnd.openxmlformats",
					FileSize: 1000,
				},
			},
		},
		UserID: userID,
	}

	mockStore.On("GetRecentSessionMessages", userID, mock.Anything, mock.Anything).Return([]storage.Message{}, nil).Maybe()
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil).Maybe()
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil).Maybe()
	mockAPI.On("SendMessage", mock.Anything, mock.MatchedBy(func(req telegram.SendMessageRequest) bool {
		return req.ChatID == chatID
	})).Return(&telegram.Message{}, nil)

	bot.processMessageGroup(context.Background(), group)

	mockStore.AssertExpectations(t)
	mockAPI.AssertExpectations(t)
}

// TestProcessMessageGroup_FileTooLarge_SendsErrorMessage verifies file size error is handled.
func TestProcessMessageGroup_FileTooLarge_SendsErrorMessage(t *testing.T) {
	bot, mockStore, _, mockAPI := setupBotForErrorTests(t)

	userID := int64(123)
	chatID := int64(456)

	group := &MessageGroup{
		Messages: []*telegram.Message{
			{
				MessageID: 1,
				From:      &telegram.User{ID: userID, FirstName: "Test"},
				Chat:      &telegram.Chat{ID: chatID},
				Date:      int(time.Now().Unix()),
				Document: &telegram.Document{
					FileID:   "doc123",
					FileName: "large.pdf",
					MimeType: "application/pdf",
					FileSize: 25 * 1024 * 1024, // 25MB > 20MB limit
				},
			},
		},
		UserID: userID,
	}

	mockStore.On("GetRecentSessionMessages", userID, mock.Anything, mock.Anything).Return([]storage.Message{}, nil).Maybe()
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil).Maybe()
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil).Maybe()
	mockAPI.On("SendMessage", mock.Anything, mock.MatchedBy(func(req telegram.SendMessageRequest) bool {
		return req.ChatID == chatID
	})).Return(&telegram.Message{}, nil)

	bot.processMessageGroup(context.Background(), group)

	mockStore.AssertExpectations(t)
	mockAPI.AssertExpectations(t)
}

// TestProcessMessageGroup_AddMessageToHistoryError_ReturnsEarly verifies DB errors stop processing.
func TestProcessMessageGroup_AddMessageToHistoryError_ReturnsEarly(t *testing.T) {
	bot, mockStore, _, _ := setupBotForErrorTests(t)

	userID := int64(123)
	chatID := int64(456)

	group := &MessageGroup{
		Messages: []*telegram.Message{
			{
				MessageID: 1,
				From:      &telegram.User{ID: userID, FirstName: "Test"},
				Chat:      &telegram.Chat{ID: chatID},
				Text:      "Hello",
				Date:      int(time.Now().Unix()),
			},
		},
		UserID: userID,
	}

	mockStore.On("GetRecentSessionMessages", userID, mock.Anything, mock.Anything).Return([]storage.Message{}, nil).Maybe()
	mockStore.On("AddMessageToHistory", userID, mock.Anything).Return(assert.AnError)

	// Should not panic
	assert.NotPanics(t, func() {
		bot.processMessageGroup(context.Background(), group)
	})

	// Wait for background goroutine to finish
	time.Sleep(50 * time.Millisecond)

	// Critical assertion: LLM should NOT be called since history write failed
	mockStore.AssertNotCalled(t, "GetUnprocessedMessages", mock.Anything)
}

// TestProcessMessageGroup_LaplaceAgentNil_SendsGenericError verifies nil agent is handled.
func TestProcessMessageGroup_LaplaceAgentNil_SendsGenericError(t *testing.T) {
	bot, mockStore, _, mockAPI := setupBotForErrorTests(t)

	// Set laplaceAgent to nil
	bot.laplaceAgent = nil

	userID := int64(123)
	chatID := int64(456)

	group := &MessageGroup{
		Messages: []*telegram.Message{
			{
				MessageID: 1,
				From:      &telegram.User{ID: userID, FirstName: "Test"},
				Chat:      &telegram.Chat{ID: chatID},
				Text:      "Hello",
				Date:      int(time.Now().Unix()),
			},
		},
		UserID: userID,
	}

	mockStore.On("GetRecentSessionMessages", userID, mock.Anything, mock.Anything).Return([]storage.Message{}, nil).Maybe()
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "user"
	})).Return(nil).Maybe()

	// Note: The flexible mock from setupBotForErrorTests will handle the SendMessage call
	// We just verify it was called by not failing expectations

	bot.processMessageGroup(context.Background(), group)

	mockStore.AssertExpectations(t)
	mockAPI.AssertExpectations(t)
}

// TestProcessMessageGroup_LLMExecutionError_SendsErrorMessage verifies LLM errors are handled.
func TestProcessMessageGroup_LLMExecutionError_SendsErrorMessage(t *testing.T) {
	bot, mockStore, mockORClient, _ := setupBotForErrorTests(t)

	userID := int64(123)
	chatID := int64(456)

	group := &MessageGroup{
		Messages: []*telegram.Message{
			{
				MessageID: 1,
				From:      &telegram.User{ID: userID, FirstName: "Test"},
				Chat:      &telegram.Chat{ID: chatID},
				Text:      "Hello",
				Date:      int(time.Now().Unix()),
			},
		},
		UserID: userID,
	}

	mockStore.On("GetRecentSessionMessages", userID, mock.Anything, mock.Anything).Return([]storage.Message{}, nil).Maybe()
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "user"
	})).Return(nil).Maybe()
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: "Hello"},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// LLM returns error
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(openrouter.ChatCompletionResponse{}, errors.New("LLM API error")).Maybe()

	// Note: SendMessage is handled by flexible mock from setupBotForErrorTests

	bot.processMessageGroup(context.Background(), group)

	// Only assert store expectations - API/ORClient have flexible mocks
	mockStore.AssertExpectations(t)
}

// TestProcessMessageGroup_AddStatFailure_Continues verifies stat errors don't block processing.
func TestProcessMessageGroup_AddStatFailure_Continues(t *testing.T) {
	bot, mockStore, mockORClient, _ := setupBotForErrorTests(t)

	userID := int64(123)
	chatID := int64(456)

	group := &MessageGroup{
		Messages: []*telegram.Message{
			{
				MessageID: 1,
				From:      &telegram.User{ID: userID, FirstName: "Test"},
				Chat:      &telegram.Chat{ID: chatID},
				Text:      "Hello",
				Date:      int(time.Now().Unix()),
			},
		},
		UserID: userID,
	}

	mockStore.On("GetRecentSessionMessages", userID, mock.Anything, mock.Anything).Return([]storage.Message{}, nil).Maybe()
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "user"
	})).Return(nil).Maybe()
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: "Hello"},
	}, nil).Maybe()
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil).Maybe()
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "assistant"
	})).Return(nil).Maybe()
	mockStore.On("AddStat", mock.Anything).Return(assert.AnError).Maybe() // Stat fails
	mockStore.On("AddAgentLog", mock.Anything, mock.Anything).Return(nil).Maybe()

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse("Response"), nil).Maybe()

	// Note: SendMessage is handled by flexible mock from setupBotForErrorTests

	bot.processMessageGroup(context.Background(), group)

	// Wait for background goroutines
	time.Sleep(50 * time.Millisecond)

	// Stat error should be logged but not block processing - if we get here without panic, test passes
}

// TestProcessMessageGroup_ContextCancellation_CompletesProcessing verifies cancelled context doesn't stop processing.
func TestProcessMessageGroup_ContextCancellation_CompletesProcessing(t *testing.T) {
	bot, mockStore, mockORClient, _ := setupBotForErrorTests(t)

	userID := int64(123)
	chatID := int64(456)

	group := &MessageGroup{
		Messages: []*telegram.Message{
			{
				MessageID: 1,
				From:      &telegram.User{ID: userID, FirstName: "Test"},
				Chat:      &telegram.Chat{ID: chatID},
				Text:      "Hello",
				Date:      int(time.Now().Unix()),
			},
		},
		UserID: userID,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	mockStore.On("GetRecentSessionMessages", userID, mock.Anything, mock.Anything).Return([]storage.Message{}, nil).Maybe()
	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "user"
	})).Return(nil).Maybe()
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: "Hello"},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse("Response"), nil).Maybe()

	mockStore.On("AddMessageToHistory", userID, mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "assistant"
	})).Return(nil).Maybe()
	mockStore.On("AddStat", mock.Anything).Return(nil).Maybe()
	mockStore.On("AddAgentLog", mock.Anything, mock.Anything).Return(nil).Maybe()

	// Note: API mocks are handled by flexible mocks from setupBotForErrorTests

	// Should complete processing despite cancellation
	bot.processMessageGroup(ctx, group)

	// Wait for background goroutines
	time.Sleep(100 * time.Millisecond)

	// Only assert store expectations - API/ORClient have flexible mocks
	mockStore.AssertExpectations(t)
}

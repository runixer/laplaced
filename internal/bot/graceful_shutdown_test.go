package bot

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestProcessMessageGroup_CompletesOnContextCancel verifies that LLM generation
// completes and response is sent even when the parent context is cancelled.
// This is critical for graceful shutdown - users should receive responses
// for requests that were already being processed.
func TestProcessMessageGroup_CompletesOnContextCancel(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(testutil.MockBotAPI)
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false

	ragService := rag.NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockORClient, nil, translator)
	laplaceAgent := laplace.New(cfg, mockORClient, ragService, mockStore, mockStore, translator, logger)

	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		ragService:      ragService,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
		laplaceAgent:    laplaceAgent,
	}
	bot.messageGrouper = NewMessageGrouper(bot, logger, 0, bot.processMessageGroup)

	userID := int64(123)
	chatID := int64(456)

	messages := []*telegram.Message{
		{
			MessageID: 1,
			Text:      "Test message",
			From:      &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
			Chat:      &telegram.Chat{ID: chatID},
			Date:      1234567890,
		},
	}

	// Track when SendMessage is called
	var sendMessageCalled bool
	var sendMessageMu sync.Mutex

	// Mock expectations
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		sendMessageMu.Lock()
		sendMessageCalled = true
		sendMessageMu.Unlock()
	}).Return(&telegram.Message{MessageID: 2}, nil)

	expectedHistoryContent := messages[0].BuildContent(translator, "en")
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: expectedHistoryContent},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.Anything).Return(nil)
	mockStore.On("UpdateStats", mock.Anything).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)
	mockStore.On("Log", mock.Anything).Return(nil)

	// Simulate slow LLM response - this is the key part of the test
	// The context will be cancelled while waiting for this response
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			// Simulate LLM thinking time
			time.Sleep(100 * time.Millisecond)
		}).
		Return(testutil.MockChatResponse("Test response"), nil)

	// Create a context that will be cancelled shortly after starting
	ctx, cancel := context.WithCancel(context.Background())

	// Start processing in a goroutine
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		group := &MessageGroup{
			Messages: messages,
			UserID:   userID,
		}
		bot.processMessageGroup(ctx, group)
	}()

	// Cancel the context after a short delay (before LLM completes)
	time.Sleep(20 * time.Millisecond)
	cancel()

	// Wait for processing to complete
	wg.Wait()

	// Verify that SendMessage was called despite context cancellation
	sendMessageMu.Lock()
	assert.True(t, sendMessageCalled, "SendMessage should be called even after context cancellation")
	sendMessageMu.Unlock()

	mockORClient.AssertCalled(t, "CreateChatCompletion", mock.Anything, mock.Anything)
	mockAPI.AssertCalled(t, "SendMessage", mock.Anything, mock.Anything)
}

// TestProcessMessageGroup_LLMContextNotCancelled verifies that the context
// passed to CreateChatCompletion is not cancelled when parent context is cancelled.
func TestProcessMessageGroup_LLMContextNotCancelled(t *testing.T) {
	translator := testutil.TestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(testutil.MockBotAPI)
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)

	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false

	ragService := rag.NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockORClient, nil, translator)
	laplaceAgent := laplace.New(cfg, mockORClient, ragService, mockStore, mockStore, translator, logger)

	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		ragService:      ragService,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
		laplaceAgent:    laplaceAgent,
	}
	bot.messageGrouper = NewMessageGrouper(bot, logger, 0, bot.processMessageGroup)

	userID := int64(123)
	chatID := int64(456)

	messages := []*telegram.Message{
		{
			MessageID: 1,
			Text:      "Test message",
			From:      &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
			Chat:      &telegram.Chat{ID: chatID},
			Date:      1234567890,
		},
	}

	// Track the context state when LLM is called
	var llmContextCancelled bool
	var llmContextMu sync.Mutex

	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(&telegram.Message{MessageID: 2}, nil)

	expectedHistoryContent := messages[0].BuildContent(translator, "en")
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: expectedHistoryContent},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.Anything).Return(nil)
	mockStore.On("UpdateStats", mock.Anything).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)
	mockStore.On("Log", mock.Anything).Return(nil)

	// Check if context is cancelled when LLM is called
	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			ctx := args.Get(0).(context.Context)
			// Wait a bit to ensure parent context would be cancelled by now
			time.Sleep(50 * time.Millisecond)
			// Check if this context is cancelled
			llmContextMu.Lock()
			llmContextCancelled = ctx.Err() != nil
			llmContextMu.Unlock()
		}).
		Return(testutil.MockChatResponse("Test response"), nil)

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		group := &MessageGroup{
			Messages: messages,
			UserID:   userID,
		}
		bot.processMessageGroup(ctx, group)
	}()

	// Cancel parent context quickly
	time.Sleep(10 * time.Millisecond)
	cancel()

	wg.Wait()

	// The LLM context should NOT be cancelled (we use context.WithoutCancel)
	llmContextMu.Lock()
	assert.False(t, llmContextCancelled, "LLM context should not be cancelled when parent is cancelled")
	llmContextMu.Unlock()
}

// TestProcessMessageGroup_VoiceCompletesOnContextCancel verifies that voice message
// processing (download, LLM generation) completes even when the parent context
// is cancelled. This is critical for graceful shutdown.
func TestProcessMessageGroup_VoiceCompletesOnContextCancel(t *testing.T) {
	translator := testutil.TestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(testutil.MockBotAPI)
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	mockDownloader := new(testutil.MockFileDownloader)

	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false

	ragService := rag.NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockORClient, nil, translator)
	laplaceAgent := laplace.New(cfg, mockORClient, ragService, mockStore, mockStore, translator, logger)

	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		ragService:      ragService,
		downloader:      mockDownloader,
		fileProcessor:   files.NewProcessor(mockDownloader, translator, "en", logger),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
		laplaceAgent:    laplaceAgent,
	}
	bot.messageGrouper = NewMessageGrouper(bot, logger, 0, bot.processMessageGroup)

	userID := int64(123)
	chatID := int64(456)
	voiceFileID := "voice_file_id"

	msg := &telegram.Message{
		MessageID: 1,
		From:      &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
		Chat:      &telegram.Chat{ID: chatID},
		Date:      1234567890,
		Voice:     &telegram.Voice{FileID: voiceFileID, MimeType: "audio/ogg"},
	}

	// Track when SendMessage is called
	var sendMessageCalled bool
	var sendMessageMu sync.Mutex

	// Mock file download with delay - FileProcessor uses DownloadFile
	mockDownloader.On("DownloadFile", mock.Anything, voiceFileID).
		Run(func(args mock.Arguments) {
			time.Sleep(50 * time.Millisecond)
		}).
		Return([]byte("fake_voice_data"), nil)

	// Setup remaining mocks
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		sendMessageMu.Lock()
		sendMessageCalled = true
		sendMessageMu.Unlock()
	}).Return(&telegram.Message{MessageID: 2}, nil)

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.Anything).Return(nil)
	mockStore.On("UpdateStats", mock.Anything).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)
	mockStore.On("Log", mock.Anything).Return(nil)

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse("Test response"), nil)

	// Create cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		group := &MessageGroup{
			Messages: []*telegram.Message{msg},
			UserID:   userID,
		}
		bot.processMessageGroup(ctx, group)
	}()

	// Cancel context during download
	time.Sleep(25 * time.Millisecond)
	cancel()

	// Wait for processing to complete
	wg.Wait()

	// Verify that SendMessage was called despite context cancellation
	sendMessageMu.Lock()
	assert.True(t, sendMessageCalled, "SendMessage should be called even after context cancellation")
	sendMessageMu.Unlock()

	mockDownloader.AssertCalled(t, "DownloadFile", mock.Anything, voiceFileID)
	mockORClient.AssertCalled(t, "CreateChatCompletion", mock.Anything, mock.Anything)
}

// TestProcessMessageGroup_VoiceDownloadContextNotCancelled verifies that the context
// passed to file download is not cancelled when parent context is cancelled.
func TestProcessMessageGroup_VoiceDownloadContextNotCancelled(t *testing.T) {
	translator := testutil.TestTranslator(t)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	mockAPI := new(testutil.MockBotAPI)
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	mockDownloader := new(testutil.MockFileDownloader)

	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false

	ragService := rag.NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockORClient, nil, translator)
	laplaceAgent := laplace.New(cfg, mockORClient, ragService, mockStore, mockStore, translator, logger)

	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		ragService:      ragService,
		downloader:      mockDownloader,
		fileProcessor:   files.NewProcessor(mockDownloader, translator, "en", logger),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
		laplaceAgent:    laplaceAgent,
	}
	bot.messageGrouper = NewMessageGrouper(bot, logger, 0, bot.processMessageGroup)

	userID := int64(123)
	chatID := int64(456)
	voiceFileID := "voice_file_id"

	msg := &telegram.Message{
		MessageID: 1,
		From:      &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
		Chat:      &telegram.Chat{ID: chatID},
		Date:      1234567890,
		Voice:     &telegram.Voice{FileID: voiceFileID, MimeType: "audio/ogg"},
	}

	// Track context state when file download is called
	var downloadContextCancelled bool
	var downloadContextMu sync.Mutex

	// Check if context is cancelled when file download is called
	mockDownloader.On("DownloadFile", mock.Anything, voiceFileID).
		Run(func(args mock.Arguments) {
			ctx := args.Get(0).(context.Context)
			// Wait to ensure parent context would be cancelled by now
			time.Sleep(50 * time.Millisecond)
			downloadContextMu.Lock()
			downloadContextCancelled = ctx.Err() != nil
			downloadContextMu.Unlock()
		}).
		Return([]byte("fake_voice_data"), nil)

	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(&telegram.Message{MessageID: 2}, nil)

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.Anything).Return(nil)
	mockStore.On("UpdateStats", mock.Anything).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)
	mockStore.On("Log", mock.Anything).Return(nil)

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
		Return(testutil.MockChatResponse("Test response"), nil)

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		group := &MessageGroup{
			Messages: []*telegram.Message{msg},
			UserID:   userID,
		}
		bot.processMessageGroup(ctx, group)
	}()

	// Cancel parent context quickly
	time.Sleep(10 * time.Millisecond)
	cancel()

	wg.Wait()

	// The file download context should NOT be cancelled
	downloadContextMu.Lock()
	assert.False(t, downloadContextCancelled, "File download context should not be cancelled when parent is cancelled")
	downloadContextMu.Unlock()
}

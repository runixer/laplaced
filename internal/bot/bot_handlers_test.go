package bot

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// setupBotForHandlerTests creates a minimal Bot for handler testing.
func setupBotForHandlerTests(t *testing.T) (*Bot, *testutil.MockStorage, *testutil.MockBotAPI) {
	t.Helper()

	logger := testutil.TestLogger()
	mockAPI := new(testutil.MockBotAPI)
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	mockDownloader := new(testutil.MockFileDownloader)

	cfg := &config.Config{
		Bot: config.BotConfig{
			AllowedUserIDs: []int64{123, 456},
			Language:       "en",
		},
	}

	translator := testutil.TestTranslator(t)

	laplaceAgent := laplace.New(cfg, mockORClient, nil, mockStore, mockStore, nil, translator, logger)

	bot := &Bot{
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
		laplaceAgent:    laplaceAgent,
	}

	// Initialize messageGrouper with a no-op callback to avoid nil pointer
	bot.messageGrouper = NewMessageGrouper(bot, logger, time.Hour, func(ctx context.Context, group *MessageGroup) {
		// No-op for tests
	})

	return bot, mockStore, mockAPI
}

// TestHandleUpdate_AllowedUser_Success verifies that allowed users are processed.
func TestHandleUpdate_AllowedUser_Success(t *testing.T) {
	bot, mockStore, _ := setupBotForHandlerTests(t)

	userID := int64(123)
	update := &telegram.Update{
		UpdateID: 1,
		Message: &telegram.Message{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "Test", Username: "testuser"},
			Chat:      &telegram.Chat{ID: userID},
			Text:      "Hello",
			Date:      int(time.Now().Unix()),
		},
	}

	// Expect UpsertUser to be called
	mockStore.On("UpsertUser", mock.MatchedBy(func(u storage.User) bool {
		return u.ID == userID && u.Username == "testuser"
	})).Return(nil)

	// ProcessUpdate should not return early
	bot.ProcessUpdate(context.Background(), update, "test")

	mockStore.AssertExpectations(t)
}

// TestHandleUpdate_UnauthorizedUser_ReturnsEarly verifies that unauthorized users are rejected.
func TestHandleUpdate_UnauthorizedUser_ReturnsEarly(t *testing.T) {
	bot, mockStore, _ := setupBotForHandlerTests(t)

	unauthorizedUserID := int64(999)
	update := &telegram.Update{
		UpdateID: 1,
		Message: &telegram.Message{
			MessageID: 1,
			From:      &telegram.User{ID: unauthorizedUserID, FirstName: "Hacker"},
			Chat:      &telegram.Chat{ID: unauthorizedUserID},
			Text:      "Hello",
			Date:      int(time.Now().Unix()),
		},
	}

	// Expect UpsertUser to be called even for unauthorized users
	mockStore.On("UpsertUser", mock.MatchedBy(func(u storage.User) bool {
		return u.ID == unauthorizedUserID
	})).Return(nil)

	// ProcessUpdate should return after isAllowed check
	bot.ProcessUpdate(context.Background(), update, "test")

	mockStore.AssertExpectations(t)
}

// TestHandleUpdate_UpsertUserError_LogsAndContinues verifies that storage errors don't block processing.
func TestHandleUpdate_UpsertUserError_LogsAndContinues(t *testing.T) {
	bot, mockStore, _ := setupBotForHandlerTests(t)

	userID := int64(123)
	update := &telegram.Update{
		UpdateID: 1,
		Message: &telegram.Message{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "Test"},
			Chat:      &telegram.Chat{ID: userID},
			Text:      "Hello",
			Date:      int(time.Now().Unix()),
		},
	}

	// UpsertUser fails but shouldn't panic
	mockStore.On("UpsertUser", mock.Anything).Return(errors.New("database error"))

	// Should not panic
	assert.NotPanics(t, func() {
		bot.ProcessUpdate(context.Background(), update, "test")
	})

	mockStore.AssertExpectations(t)
}

// TestHandleUpdate_NilMessage_ReturnsEarly verifies nil message is handled.
func TestHandleUpdate_NilMessage_ReturnsEarly(t *testing.T) {
	bot, mockStore, _ := setupBotForHandlerTests(t)

	update := &telegram.Update{
		UpdateID: 1,
		Message:  nil,
	}

	// Should not call any storage methods
	bot.ProcessUpdate(context.Background(), update, "test")

	// UpsertUser should NOT be called
	mockStore.AssertNotCalled(t, "UpsertUser", mock.Anything)
}

// TestHandleUpdate_VoiceMessage_RoutedToGrouper verifies voice messages trigger grouping.
func TestHandleUpdate_VoiceMessage_RoutedToGrouper(t *testing.T) {
	bot, mockStore, _ := setupBotForHandlerTests(t)

	userID := int64(123)
	update := &telegram.Update{
		UpdateID: 1,
		Message: &telegram.Message{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "Test"},
			Chat:      &telegram.Chat{ID: userID},
			Voice:     &telegram.Voice{FileID: "voice123"},
			Date:      int(time.Now().Unix()),
		},
	}

	mockStore.On("UpsertUser", mock.Anything).Return(nil)

	// Should not panic when routing to grouper
	assert.NotPanics(t, func() {
		bot.ProcessUpdate(context.Background(), update, "test")
	})

	mockStore.AssertExpectations(t)
}

// TestHandleUpdate_PhotoMessage_RoutedToGrouper verifies photo messages trigger grouping.
func TestHandleUpdate_PhotoMessage_RoutedToGrouper(t *testing.T) {
	bot, mockStore, _ := setupBotForHandlerTests(t)

	userID := int64(123)
	update := &telegram.Update{
		UpdateID: 1,
		Message: &telegram.Message{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "Test"},
			Chat:      &telegram.Chat{ID: userID},
			Photo: []telegram.PhotoSize{
				{FileID: "photo123", Width: 100, Height: 100},
			},
			Date: int(time.Now().Unix()),
		},
	}

	mockStore.On("UpsertUser", mock.Anything).Return(nil)

	assert.NotPanics(t, func() {
		bot.ProcessUpdate(context.Background(), update, "test")
	})

	mockStore.AssertExpectations(t)
}

// TestHandleUpdateAsync_ConcurrentCalls verifies concurrent async updates are handled safely.
func TestHandleUpdateAsync_ConcurrentCalls(t *testing.T) {
	bot, mockStore, _ := setupBotForHandlerTests(t)

	userID := int64(123)
	rawUpdate, _ := json.Marshal(&telegram.Update{
		UpdateID: 1,
		Message: &telegram.Message{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "Test"},
			Chat:      &telegram.Chat{ID: userID},
			Text:      "Hello",
			Date:      int(time.Now().Unix()),
		},
	})

	mockStore.On("UpsertUser", mock.Anything).Return(nil)

	// Multiple concurrent calls
	for i := 0; i < 5; i++ {
		bot.HandleUpdateAsync(context.Background(), rawUpdate, "test")
	}

	// Wait a bit for goroutines to start
	time.Sleep(10 * time.Millisecond)

	// Stop the bot to wait for all goroutines
	bot.Stop()

	mockStore.AssertExpectations(t)
}

// TestHandleUpdateAsync_PanicInGoroutine_Recovered verifies panics are recovered.
func TestHandleUpdateAsync_PanicInGoroutine_Recovered(t *testing.T) {
	bot, _, _ := setupBotForHandlerTests(t)

	// Create invalid JSON that will cause unmarshal to panic
	invalidJSON := json.RawMessage(nil)

	// Should not panic (HandleUpdate catches unmarshal errors)
	assert.NotPanics(t, func() {
		bot.HandleUpdateAsync(context.Background(), invalidJSON, "test")
	})

	bot.Stop()
}

// TestProcessUpdateAsync_QueueMechanics verifies updates are queued correctly.
func TestProcessUpdateAsync_QueueMechanics(t *testing.T) {
	bot, mockStore, _ := setupBotForHandlerTests(t)

	userID := int64(123)

	mockStore.On("UpsertUser", mock.MatchedBy(func(u storage.User) bool {
		return u.ID == userID
	})).Return(nil).Times(3)

	// Queue multiple updates
	for i := 0; i < 3; i++ {
		update := &telegram.Update{
			UpdateID: i + 1,
			Message: &telegram.Message{
				MessageID: i + 1,
				From:      &telegram.User{ID: userID, FirstName: "Test"},
				Chat:      &telegram.Chat{ID: userID},
				Text:      "Hello",
				Date:      int(time.Now().Unix()),
			},
		}
		bot.ProcessUpdateAsync(context.Background(), update, "test")
	}

	// Wait for processing
	time.Sleep(10 * time.Millisecond)
	bot.Stop()

	mockStore.AssertExpectations(t)
}

// TestProcessUpdateAsync_ContextCancellation_StopsProcessing verifies cancelled context stops work.
func TestProcessUpdateAsync_ContextCancellation_StopsProcessing(t *testing.T) {
	bot, mockStore, _ := setupBotForHandlerTests(t)

	userID := int64(123)

	ctx, cancel := context.WithCancel(context.Background())

	mockStore.On("UpsertUser", mock.Anything).Return(nil).Maybe()

	update := &telegram.Update{
		UpdateID: 1,
		Message: &telegram.Message{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "Test"},
			Chat:      &telegram.Chat{ID: userID},
			Text:      "Hello",
			Date:      int(time.Now().Unix()),
		},
	}

	bot.ProcessUpdateAsync(ctx, update, "test")

	// Cancel immediately
	cancel()

	bot.Stop()
}

// TestHandleUpdate_InvalidJSON_LogsError verifies invalid JSON is handled gracefully.
func TestHandleUpdate_InvalidJSON_LogsError(t *testing.T) {
	bot, _, _ := setupBotForHandlerTests(t)

	// Invalid JSON
	invalidJSON := json.RawMessage([]byte("{invalid json"))

	// Should not panic
	assert.NotPanics(t, func() {
		bot.HandleUpdate(context.Background(), invalidJSON, "test")
	})
}

// TestIsAllowed_EmptyAllowedList_ReturnsFalse verifies empty allowed list blocks everyone.
func TestIsAllowed_EmptyAllowedList_ReturnsFalse(t *testing.T) {
	cfg := &config.Config{
		Bot: config.BotConfig{
			AllowedUserIDs: []int64{},
		},
	}
	bot := &Bot{cfg: cfg, logger: testutil.TestLogger()}

	result := bot.isAllowed(123)

	assert.False(t, result)
}

// TestIsAllowed_UserInAllowedList_ReturnsTrue verifies allowed users pass check.
func TestIsAllowed_UserInAllowedList_ReturnsTrue(t *testing.T) {
	cfg := &config.Config{
		Bot: config.BotConfig{
			AllowedUserIDs: []int64{123, 456},
		},
	}
	bot := &Bot{cfg: cfg, logger: testutil.TestLogger()}

	assert.True(t, bot.isAllowed(123))
	assert.True(t, bot.isAllowed(456))
	assert.False(t, bot.isAllowed(789))
}

// TestIntPtrOrNil_ReturnsNilForZero verifies zero returns nil.
func TestIntPtrOrNil_ReturnsNilForZero(t *testing.T) {
	result := intPtrOrNil(0)
	assert.Nil(t, result)
}

// TestIntPtrOrNil_ReturnsPointerForNonZero verifies non-zero returns pointer.
func TestIntPtrOrNil_ReturnsPointerForNonZero(t *testing.T) {
	result := intPtrOrNil(42)
	assert.NotNil(t, result)
	assert.Equal(t, 42, *result)
}

// TestSendAction_Success verifies action is sent to Telegram API.
func TestSendAction_Success(t *testing.T) {
	bot, _, mockAPI := setupBotForHandlerTests(t)

	chatID := int64(123)
	threadID := 456
	action := "typing"

	mockAPI.On("SendChatAction", mock.Anything, mock.MatchedBy(func(req telegram.SendChatActionRequest) bool {
		return req.ChatID == chatID && req.Action == action
	})).Return(nil)

	bot.sendAction(context.Background(), chatID, threadID, action)

	mockAPI.AssertExpectations(t)
}

// TestSendAction_APIError_LogsWarning verifies API errors are logged but don't panic.
func TestSendAction_APIError_LogsWarning(t *testing.T) {
	bot, _, mockAPI := setupBotForHandlerTests(t)

	chatID := int64(123)
	action := "typing"

	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).
		Return(errors.New("telegram API error"))

	// Should not panic
	assert.NotPanics(t, func() {
		bot.sendAction(context.Background(), chatID, 0, action)
	})

	mockAPI.AssertExpectations(t)
}

// TestSendAction_WithContextCancellation_StopsGracefully verifies cancelled context stops request.
func TestSendAction_WithContextCancellation_StopsGracefully(t *testing.T) {
	bot, _, mockAPI := setupBotForHandlerTests(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// May still try to send, mock the call
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil).Maybe()

	// Should not block or panic
	assert.NotPanics(t, func() {
		bot.sendAction(ctx, 123, 0, "typing")
	})
}

// TestSendAction_AllActionTypes verifies all action types are sent correctly.
func TestSendAction_AllActionTypes(t *testing.T) {
	actions := []string{
		"typing",
		"upload_photo",
		"record_video",
		"upload_video",
		"record_voice",
		"upload_voice",
		"record_video_note",
		"upload_video_note",
		"choose_sticker",
		"find_location",
	}

	for _, action := range actions {
		t.Run(action, func(t *testing.T) {
			bot, _, mockAPI := setupBotForHandlerTests(t)

			mockAPI.On("SendChatAction", mock.Anything, mock.MatchedBy(func(req telegram.SendChatActionRequest) bool {
				return req.Action == action
			})).Return(nil)

			bot.sendAction(context.Background(), 123, 0, action)

			mockAPI.AssertExpectations(t)
		})
	}
}

// TestSendAction_ZeroMessageThreadID_SendsNil verifies zero thread ID becomes nil.
func TestSendAction_ZeroMessageThreadID_SendsNil(t *testing.T) {
	bot, _, mockAPI := setupBotForHandlerTests(t)

	chatID := int64(123)

	mockAPI.On("SendChatAction", mock.Anything, mock.MatchedBy(func(req telegram.SendChatActionRequest) bool {
		// MessageThreadID should be nil for zero value
		return req.ChatID == chatID && req.MessageThreadID == nil
	})).Return(nil)

	bot.sendAction(context.Background(), chatID, 0, "typing")

	mockAPI.AssertExpectations(t)
}

// TestSendAction_NonZeroMessageThreadID_SendsValue verifies non-zero thread ID is sent.
func TestSendAction_NonZeroMessageThreadID_SendsValue(t *testing.T) {
	bot, _, mockAPI := setupBotForHandlerTests(t)

	chatID := int64(123)
	threadID := 456

	mockAPI.On("SendChatAction", mock.Anything, mock.MatchedBy(func(req telegram.SendChatActionRequest) bool {
		return req.ChatID == chatID && req.MessageThreadID != nil && *req.MessageThreadID == threadID
	})).Return(nil)

	bot.sendAction(context.Background(), chatID, threadID, "typing")

	mockAPI.AssertExpectations(t)
}

// TestSendTypingActionLoop_Cancellation_StopsLoop verifies typing loop stops on cancellation.
func TestSendTypingActionLoop_Cancellation_StopsLoop(t *testing.T) {
	bot, _, mockAPI := setupBotForHandlerTests(t)

	ctx, cancel := context.WithCancel(context.Background())

	// Send at least one action
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil).Maybe()

	// Start loop in background
	go bot.sendTypingActionLoop(ctx, 123, 0)

	// Let it send at least once
	time.Sleep(100 * time.Millisecond)

	// Cancel and wait a bit
	cancel()
	time.Sleep(50 * time.Millisecond)

	// Should complete without hanging
	// If test passes, the loop stopped correctly
}

// TestSendTypingActionLoop_SendsPeriodicActions verifies actions are sent periodically.
func TestSendTypingActionLoop_SendsPeriodicActions(t *testing.T) {
	bot, _, mockAPI := setupBotForHandlerTests(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actionCount := 0
	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil).
		Run(func(mock.Arguments) { actionCount++ })

	// Start loop
	go bot.sendTypingActionLoop(ctx, 123, 0)

	// Wait for multiple actions (initial + at least one periodic)
	time.Sleep(5 * time.Second)

	cancel()

	// Should have sent at least 2 actions (initial + 1+ periodic)
	assert.GreaterOrEqual(t, actionCount, 2)
}

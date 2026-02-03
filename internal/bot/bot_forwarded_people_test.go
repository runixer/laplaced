package bot

import (
	"context"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// setupBotForExtractedPeopleTests creates a Bot with mocked peopleRepo for extractForwardedPeople testing.
func setupBotForExtractedPeopleTests(t *testing.T) (*Bot, *testutil.MockStorage) {
	t.Helper()

	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)

	cfg := &config.Config{
		Bot: config.BotConfig{
			Language: "en",
		},
	}

	translator := testutil.TestTranslator(t)

	bot := &Bot{
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		peopleRepo:      mockStore,
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	return bot, mockStore
}

// TestExtractForwardedPeople_NewPerson_CreatesPerson verifies a new person is created from forward.
func TestExtractForwardedPeople_NewPerson_CreatesPerson(t *testing.T) {
	bot, mockStore := setupBotForExtractedPeopleTests(t)

	userID := int64(123)
	forwarderID := int64(789)
	forwarderUsername := "forwarded_user"

	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        forwarderID,
					FirstName: "Forwarded",
					LastName:  "User",
					Username:  forwarderUsername,
				},
			},
		},
	}

	// Not found by telegram_id
	mockStore.On("FindPersonByTelegramID", userID, forwarderID).Return(nil, nil)

	// Not found by username
	mockStore.On("FindPersonByUsername", userID, forwarderUsername).Return(nil, nil)

	// Not found by name
	mockStore.On("FindPersonByName", userID, "Forwarded User").Return(nil, nil)

	// Create new person
	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.UserID == userID &&
			p.DisplayName == "Forwarded User" &&
			p.TelegramID != nil && *p.TelegramID == forwarderID &&
			p.Username != nil && *p.Username == forwarderUsername &&
			p.Circle == "Other" &&
			p.MentionCount == 1
	})).Return(int64(1), nil)

	bot.extractForwardedPeople(context.Background(), userID, messages, testutil.TestLogger())

	mockStore.AssertExpectations(t)
}

// TestExtractForwardedPeople_ExistingPersonByTelegramID_UpdatesPerson verifies existing person is updated.
func TestExtractForwardedPeople_ExistingPersonByTelegramID_UpdatesPerson(t *testing.T) {
	bot, mockStore := setupBotForExtractedPeopleTests(t)

	userID := int64(123)
	forwarderID := int64(789)

	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        forwarderID,
					FirstName: "Forwarded",
					Username:  "fwd_user",
				},
			},
		},
	}

	// Found by telegram_id
	existingPerson := &storage.Person{
		ID:           1,
		UserID:       userID,
		DisplayName:  "Forwarded",
		TelegramID:   &forwarderID,
		Circle:       "Other",
		MentionCount: 5,
	}
	mockStore.On("FindPersonByTelegramID", userID, forwarderID).Return(existingPerson, nil)

	// Update with incremented mention_count and new username
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 &&
			p.MentionCount == 6 && // Incremented
			p.Username != nil && *p.Username == "fwd_user"
	})).Return(nil)

	bot.extractForwardedPeople(context.Background(), userID, messages, testutil.TestLogger())

	mockStore.AssertExpectations(t)
}

// TestExtractForwardedPerson_ExistingPersonByUsername_AddsTelegramID verifies username match adds telegram_id.
func TestExtractForwardedPerson_ExistingPersonByUsername_AddsTelegramID(t *testing.T) {
	bot, mockStore := setupBotForExtractedPeopleTests(t)

	userID := int64(123)
	forwarderID := int64(789)
	forwarderUsername := "fwd_user"

	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:       forwarderID,
					Username: forwarderUsername,
				},
			},
		},
	}

	// Not found by telegram_id
	mockStore.On("FindPersonByTelegramID", userID, forwarderID).Return(nil, nil)

	// Found by username
	existingPerson := &storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "fwd_user",
		Username:    &forwarderUsername,
		Circle:      "Other",
	}
	mockStore.On("FindPersonByUsername", userID, forwarderUsername).Return(existingPerson, nil)

	// Update with telegram_id
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 &&
			p.TelegramID != nil && *p.TelegramID == forwarderID &&
			p.MentionCount == 1
	})).Return(nil)

	bot.extractForwardedPeople(context.Background(), userID, messages, testutil.TestLogger())

	mockStore.AssertExpectations(t)
}

// TestExtractForwardedPerson_ExistingPersonByName_AddsTelegramID verifies name match adds telegram_id.
func TestExtractForwardedPerson_ExistingPersonByName_AddsTelegramID(t *testing.T) {
	bot, mockStore := setupBotForExtractedPeopleTests(t)

	userID := int64(123)
	forwarderID := int64(789)
	displayName := "John Doe"

	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        forwarderID,
					FirstName: "John",
					LastName:  "Doe",
				},
			},
		},
	}

	// Not found by telegram_id
	mockStore.On("FindPersonByTelegramID", userID, forwarderID).Return(nil, nil)

	// No username
	mockStore.On("FindPersonByUsername", userID, "").Return(nil, nil).Maybe()

	// Found by name
	existingPerson := &storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: displayName,
		Circle:      "Other",
	}
	mockStore.On("FindPersonByName", userID, displayName).Return(existingPerson, nil)

	// Update with telegram_id
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 &&
			p.TelegramID != nil && *p.TelegramID == forwarderID
	})).Return(nil)

	bot.extractForwardedPeople(context.Background(), userID, messages, testutil.TestLogger())

	mockStore.AssertExpectations(t)
}

// TestExtractForwardedPeople_ChannelForward_Ignored verifies channel forwards are ignored.
func TestExtractForwardedPeople_ChannelForward_Ignored(t *testing.T) {
	bot, mockStore := setupBotForExtractedPeopleTests(t)

	userID := int64(123)

	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "channel",
				Date: now - 3600,
				SenderChat: &telegram.Chat{
					ID:    456,
					Title: "Some Channel",
				},
			},
		},
	}

	// No repo calls should be made
	bot.extractForwardedPeople(context.Background(), userID, messages, testutil.TestLogger())

	// Assert no calls were made
	mockStore.AssertNotCalled(t, "FindPersonByTelegramID", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "AddPerson", mock.Anything)
}

// TestExtractForwardedPeople_BotForward_Ignored verifies bot forwards are ignored.
func TestExtractForwardedPeople_BotForward_Ignored(t *testing.T) {
	bot, mockStore := setupBotForExtractedPeopleTests(t)

	userID := int64(123)
	botID := int64(789)

	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        botID,
					IsBot:     true,
					FirstName: "Bot",
				},
			},
		},
	}

	// No repo calls should be made
	bot.extractForwardedPeople(context.Background(), userID, messages, testutil.TestLogger())

	mockStore.AssertNotCalled(t, "FindPersonByTelegramID", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "AddPerson", mock.Anything)
}

// TestExtractForwardedPeople_SelfForward_Ignored verifies self-forwards are ignored.
func TestExtractForwardedPeople_SelfForward_Ignored(t *testing.T) {
	bot, mockStore := setupBotForExtractedPeopleTests(t)

	userID := int64(123)

	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        userID, // Same as From.ID
					FirstName: "User",
				},
			},
		},
	}

	// No repo calls should be made
	bot.extractForwardedPeople(context.Background(), userID, messages, testutil.TestLogger())

	mockStore.AssertNotCalled(t, "FindPersonByTelegramID", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "AddPerson", mock.Anything)
}

// TestExtractForwardedPeople_NoForwardOrigin_Skips verifies messages without forward are skipped.
func TestExtractForwardedPeople_NoForwardOrigin_Skips(t *testing.T) {
	bot, mockStore := setupBotForExtractedPeopleTests(t)

	userID := int64(123)

	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Text:      "Regular message",
			Date:      now,
			// No ForwardOrigin
		},
	}

	// No repo calls should be made
	bot.extractForwardedPeople(context.Background(), userID, messages, testutil.TestLogger())

	mockStore.AssertNotCalled(t, "FindPersonByTelegramID", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "AddPerson", mock.Anything)
}

// TestExtractForwardedPeople_NilPeopleRepo_DoesNothing verifies nil repo is handled.
func TestExtractForwardedPeople_NilPeopleRepo_DoesNothing(t *testing.T) {
	logger := testutil.TestLogger()

	cfg := &config.Config{
		Bot: config.BotConfig{
			Language: "en",
		},
	}

	translator := testutil.TestTranslator(t)

	bot := &Bot{
		cfg:        cfg,
		logger:     logger,
		translator: translator,
		// peopleRepo is nil
	}

	userID := int64(123)
	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        789,
					FirstName: "Forwarded",
				},
			},
		},
	}

	// Should not panic
	assert.NotPanics(t, func() {
		bot.extractForwardedPeople(context.Background(), userID, messages, logger)
	})
}

// TestExtractForwardedPeople_DisplayNameFromUsername verifies username is used when no first name.
func TestExtractForwardedPeople_DisplayNameFromUsername(t *testing.T) {
	bot, mockStore := setupBotForExtractedPeopleTests(t)

	userID := int64(123)
	forwarderID := int64(789)
	forwarderUsername := "username_only"

	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:       forwarderID,
					Username: forwarderUsername,
					// No FirstName or LastName
				},
			},
		},
	}

	mockStore.On("FindPersonByTelegramID", userID, forwarderID).Return(nil, nil)
	mockStore.On("FindPersonByUsername", userID, forwarderUsername).Return(nil, nil)
	mockStore.On("FindPersonByName", userID, forwarderUsername).Return(nil, nil)

	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.DisplayName == forwarderUsername
	})).Return(int64(1), nil)

	bot.extractForwardedPeople(context.Background(), userID, messages, testutil.TestLogger())

	mockStore.AssertExpectations(t)
}

// TestExtractForwardedPeople_MultipleMessages_ProcessesAll verifies multiple messages are processed.
func TestExtractForwardedPeople_MultipleMessages_ProcessesAll(t *testing.T) {
	bot, mockStore := setupBotForExtractedPeopleTests(t)

	userID := int64(123)
	forwarder1ID := int64(789)
	forwarder2ID := int64(790)

	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        forwarder1ID,
					FirstName: "First",
				},
			},
		},
		{
			MessageID: 2,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        forwarder2ID,
					FirstName: "Second",
				},
			},
		},
	}

	// First forwarder (no username)
	mockStore.On("FindPersonByTelegramID", userID, forwarder1ID).Return(nil, nil)
	mockStore.On("FindPersonByName", userID, "First").Return(nil, nil)
	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.DisplayName == "First"
	})).Return(int64(1), nil)

	// Second forwarder (no username)
	mockStore.On("FindPersonByTelegramID", userID, forwarder2ID).Return(nil, nil)
	mockStore.On("FindPersonByName", userID, "Second").Return(nil, nil)
	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.DisplayName == "Second"
	})).Return(int64(2), nil)

	bot.extractForwardedPeople(context.Background(), userID, messages, testutil.TestLogger())

	mockStore.AssertExpectations(t)
}

// TestExtractForwardedPeople_DuplicateForwards_Deduplicates verifies same person in multiple messages updates once.
func TestExtractForwardedPeople_DuplicateForwards_Deduplicates(t *testing.T) {
	bot, mockStore := setupBotForExtractedPeopleTests(t)

	userID := int64(123)
	forwarderID := int64(789)

	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        forwarderID,
					FirstName: "Forwarded",
				},
			},
		},
		{
			MessageID: 2,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        forwarderID, // Same person
					FirstName: "Forwarded",
				},
			},
		},
	}

	// First message creates person
	mockStore.On("FindPersonByTelegramID", userID, forwarderID).Return(nil, nil).Once()
	mockStore.On("FindPersonByName", userID, "Forwarded").Return(nil, nil).Once()
	mockStore.On("AddPerson", mock.Anything).Return(int64(1), nil).Once()

	// Second message updates existing person
	existingPerson := &storage.Person{
		ID:           1,
		UserID:       userID,
		DisplayName:  "Forwarded",
		TelegramID:   &forwarderID,
		MentionCount: 1,
	}
	mockStore.On("FindPersonByTelegramID", userID, forwarderID).Return(existingPerson, nil).Once()
	mockStore.On("UpdatePerson", mock.Anything).Return(nil).Once()

	bot.extractForwardedPeople(context.Background(), userID, messages, testutil.TestLogger())

	mockStore.AssertExpectations(t)
}

// TestExtractForwardedPeople_UpdateError_Continues verifies errors don't stop processing.
func TestExtractForwardedPeople_UpdateError_Continues(t *testing.T) {
	bot, mockStore := setupBotForExtractedPeopleTests(t)

	userID := int64(123)
	forwarderID := int64(789)

	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: userID},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        forwarderID,
					FirstName: "Forwarded",
				},
			},
		},
	}

	existingPerson := &storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "Forwarded",
		TelegramID:  &forwarderID,
	}
	mockStore.On("FindPersonByTelegramID", userID, forwarderID).Return(existingPerson, nil)
	mockStore.On("UpdatePerson", mock.Anything).Return(assert.AnError) // Update fails

	// Should not panic
	assert.NotPanics(t, func() {
		bot.extractForwardedPeople(context.Background(), userID, messages, testutil.TestLogger())
	})

	mockStore.AssertExpectations(t)
}

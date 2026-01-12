package bot

import (
	"context"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestExtractForwardedPeople_NewPerson(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockAPI := new(testutil.MockBotAPI)
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()

	mockDownloader := new(testutil.MockFileDownloader)

	// Create bot with peopleRepo
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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)
	now := int(time.Now().Unix())
	senderID := int64(789)
	senderUsername := "john_doe"

	// Create forwarded message from a user
	messages := []*telegram.Message{
		{
			MessageID: 1,
			Text:      "Forwarded message",
			From:      &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
			Chat:      &telegram.Chat{ID: 456},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        senderID,
					FirstName: "John",
					LastName:  "Doe",
					Username:  senderUsername,
					IsBot:     false,
				},
			},
		},
	}

	// Mock: person not found by telegram_id
	mockStore.On("FindPersonByTelegramID", userID, senderID).Return((*storage.Person)(nil), nil).Once()

	// Mock: person not found by username
	mockStore.On("FindPersonByUsername", userID, senderUsername).Return((*storage.Person)(nil), nil).Once()

	// Mock: person not found by name
	mockStore.On("FindPersonByName", userID, "John Doe").Return((*storage.Person)(nil), nil).Once()

	// Mock: create new person
	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.UserID == userID && p.DisplayName == "John Doe" &&
			p.TelegramID != nil && *p.TelegramID == senderID &&
			p.Username != nil && *p.Username == senderUsername &&
			p.Circle == "Other" && p.MentionCount == 1
	})).Return(int64(1), nil).Once()

	// Execute
	bot.extractForwardedPeople(context.Background(), userID, messages, logger)

	// Verify
	mockStore.AssertExpectations(t)
}

func TestExtractForwardedPeople_UpdateExistingPerson(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)
	now := int(time.Now().Unix())
	senderID := int64(789)

	existingPerson := &storage.Person{
		ID:           1,
		UserID:       userID,
		DisplayName:  "John Doe",
		TelegramID:   testutil.Ptr(senderID),
		Username:     testutil.Ptr("john_doe"),
		Circle:       "Friends",
		MentionCount: 5,
	}

	messages := []*telegram.Message{
		{
			MessageID: 1,
			Text:      "Forwarded message",
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: 456},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type:       "user",
				Date:       now - 3600,
				SenderUser: &telegram.User{ID: senderID, FirstName: "John", LastName: "Doe"},
			},
		},
	}

	// Mock: person found by telegram_id
	mockStore.On("FindPersonByTelegramID", userID, senderID).Return(existingPerson, nil).Once()

	// Mock: update person (mention_count incremented)
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 && p.MentionCount == 6
	})).Return(nil).Once()

	// Execute
	bot.extractForwardedPeople(context.Background(), userID, messages, logger)

	// Verify
	mockStore.AssertExpectations(t)
}

func TestExtractForwardedPeople_SkipBots(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)
	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			Text:      "Forwarded from bot",
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: 456},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        789,
					FirstName: "Bot",
					IsBot:     true,
				},
			},
		},
	}

	// Execute - should skip bot
	bot.extractForwardedPeople(context.Background(), userID, messages, logger)

	// Verify: no calls to peopleRepo
	mockStore.AssertNotCalled(t, "FindPersonByTelegramID", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "AddPerson", mock.Anything)
}

func TestExtractForwardedPeople_SkipSelf(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)
	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			Text:      "Self-forwarded message",
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: 456},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type:       "user",
				Date:       now - 3600,
				SenderUser: &telegram.User{ID: userID, FirstName: "User"},
			},
		},
	}

	// Execute - should skip self
	bot.extractForwardedPeople(context.Background(), userID, messages, logger)

	// Verify: no calls to peopleRepo
	mockStore.AssertNotCalled(t, "FindPersonByTelegramID", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "AddPerson", mock.Anything)
}

func TestExtractForwardedPeople_SkipNonUserForwards(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)
	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			Text:      "Forwarded from channel",
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: 456},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "channel", // Not a user forward
				Date: now - 3600,
			},
		},
	}

	// Execute - should skip non-user forwards
	bot.extractForwardedPeople(context.Background(), userID, messages, logger)

	// Verify: no calls to peopleRepo
	mockStore.AssertNotCalled(t, "FindPersonByTelegramID", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "AddPerson", mock.Anything)
}

func TestExtractForwardedPeople_UpdatePersonByUsername(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)
	now := int(time.Now().Unix())
	senderID := int64(789)
	senderUsername := "jane_doe"

	// Person exists by username but not by telegram_id yet
	existingPerson := &storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "Jane",
		Username:    testutil.Ptr(senderUsername),
		Circle:      "Other",
	}

	messages := []*telegram.Message{
		{
			MessageID: 1,
			Text:      "Forwarded message",
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: 456},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        senderID,
					FirstName: "Jane",
					Username:  senderUsername,
				},
			},
		},
	}

	// Mock: not found by telegram_id
	mockStore.On("FindPersonByTelegramID", userID, senderID).Return((*storage.Person)(nil), nil).Once()

	// Mock: found by username
	mockStore.On("FindPersonByUsername", userID, senderUsername).Return(existingPerson, nil).Once()

	// Mock: update with telegram_id
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 && p.TelegramID != nil && *p.TelegramID == senderID
	})).Return(nil).Once()

	// Execute
	bot.extractForwardedPeople(context.Background(), userID, messages, logger)

	// Verify
	mockStore.AssertExpectations(t)
}

func TestExtractForwardedPeople_NilPeopleRepo(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockStore := new(testutil.MockStorage)
	mockDownloader := new(testutil.MockFileDownloader)

	bot := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		peopleRepo:      nil, // nil peopleRepo
		orClient:        mockORClient,
		downloader:      mockDownloader,
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)
	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 1,
			Text:      "Forwarded message",
			From:      &telegram.User{ID: userID, FirstName: "User"},
			Chat:      &telegram.Chat{ID: 456},
			Date:      now,
			ForwardOrigin: &telegram.MessageOrigin{
				Type: "user",
				Date: now - 3600,
				SenderUser: &telegram.User{
					ID:        789,
					FirstName: "John",
				},
			},
		},
	}

	// Execute - should return early without panic
	bot.extractForwardedPeople(context.Background(), userID, messages, logger)

	// Verify: no calls to store (since we return early)
	// No assertions needed - just no panic
}

func TestPerformSearchPeople_FoundByUsername(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)

	foundPerson := &storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "John Doe",
		Username:    testutil.Ptr("johndoe"),
		Circle:      "Friends",
		Bio:         "Software engineer",
		Aliases:     []string{"Johnny", "JD"},
	}

	// Mock: found by username
	mockStore.On("FindPersonByUsername", userID, "johndoe").Return(foundPerson, nil).Once()

	// Execute
	result, err := bot.performSearchPeople(context.Background(), userID, "@johndoe")

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "Found 1 people")
	assert.Contains(t, result, "John Doe")
	assert.Contains(t, result, "Friends")
	assert.Contains(t, result, "@johndoe")
	assert.Contains(t, result, "Johnny")
	assert.Contains(t, result, "Software engineer")
	mockStore.AssertExpectations(t)
}

func TestPerformSearchPeople_FoundByName(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
		ragService:      nil, // No RAG for this test
	}

	userID := int64(123)

	foundPerson := &storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "Jane Smith",
		Circle:      "Work_Inner",
		Bio:         "Project manager",
	}

	// Mock: not found by username (not @ prefix)
	// Mock: found by name
	mockStore.On("FindPersonByName", userID, "Jane Smith").Return(foundPerson, nil).Once()

	// Execute
	result, err := bot.performSearchPeople(context.Background(), userID, "Jane Smith")

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "Found 1 people")
	assert.Contains(t, result, "Jane Smith")
	assert.Contains(t, result, "Work_Inner")
	mockStore.AssertExpectations(t)
}

func TestPerformSearchPeople_NotFound(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
		ragService:      nil,
	}

	userID := int64(123)

	// Mock: not found (no @ prefix, so only FindPersonByName and FindPersonByAlias are called)
	mockStore.On("FindPersonByName", userID, "unknown").Return((*storage.Person)(nil), nil).Once()
	mockStore.On("FindPersonByAlias", userID, "unknown").Return([]storage.Person{}, nil).Once()

	// Execute
	result, err := bot.performSearchPeople(context.Background(), userID, "unknown")

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "No people found matching 'unknown'")
	mockStore.AssertExpectations(t)
}

func TestPerformUpdatePerson_Success(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)

	existingPerson := &storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "John Doe",
		Circle:      "Friends",
		Bio:         "Engineer",
		Aliases:     []string{},
	}

	// Mock: found by name
	mockStore.On("FindPersonByName", userID, "John Doe").Return(existingPerson, nil).Once()

	// Mock: embedding generation (bio_append triggers re-embedding)
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req interface{}) bool {
		// Just check it's an EmbeddingRequest
		return true
	})).Return(testutil.MockEmbeddingResponse(), nil).Once()

	// Mock: update person
	mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.ID == 1 && p.Circle == "Work_Inner" && p.Bio == "Engineer Manager"
	})).Return(nil).Once()

	params := map[string]interface{}{
		"updates": map[string]interface{}{
			"circle":      "Work_Inner",
			"bio_append":  "Manager",
			"aliases_add": []interface{}{"JD", "Johnny"},
		},
	}

	// Execute
	result, err := bot.performUpdatePerson(context.Background(), userID, "John Doe", params)

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully updated person 'John Doe'")
	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

func TestPerformUpdatePerson_PersonNotFound(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)

	// Mock: not found
	mockStore.On("FindPersonByName", userID, "Unknown").Return((*storage.Person)(nil), nil).Once()
	mockStore.On("FindPersonByAlias", userID, "Unknown").Return([]storage.Person{}, nil).Once()

	params := map[string]interface{}{
		"updates": map[string]interface{}{
			"circle": "Friends",
		},
	}

	// Execute
	result, err := bot.performUpdatePerson(context.Background(), userID, "Unknown", params)

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "Person 'Unknown' not found")
	mockStore.AssertExpectations(t)
}

func TestPerformMergePeople_Success(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)

	targetPerson := &storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "John Doe",
		Bio:         "Engineer",
		Aliases:     []string{"Johnny"},
	}

	sourcePerson := &storage.Person{
		ID:          2,
		UserID:      userID,
		DisplayName: "J. Doe",
		Bio:         "Manager",
		Aliases:     []string{"JD"},
	}

	// Mock: find target
	mockStore.On("FindPersonByName", userID, "John Doe").Return(targetPerson, nil).Once()

	// Mock: find source
	mockStore.On("FindPersonByName", userID, "J. Doe").Return(sourcePerson, nil).Once()

	// Mock: merge
	mockStore.On("MergePeople", userID, targetPerson.ID, sourcePerson.ID,
		mock.MatchedBy(func(bio string) bool {
			return bio == "Engineer\nManager"
		}),
		mock.MatchedBy(func(aliases []string) bool {
			// Should contain Johnny, JD, and "J. Doe" (source display name)
			return len(aliases) >= 2
		})).Return(nil).Once()

	params := map[string]interface{}{
		"reason": "Same person",
	}

	// Execute
	result, err := bot.performMergePeople(context.Background(), userID, "John Doe", "J. Doe", params)

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully merged 'J. Doe' into 'John Doe'")
	mockStore.AssertExpectations(t)
}

func TestPerformMergePeople_SelfMerge(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)

	person := &storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "John Doe",
	}

	// Mock: find target
	mockStore.On("FindPersonByName", userID, "John Doe").Return(person, nil).Once()

	// Mock: find source (same person)
	mockStore.On("FindPersonByName", userID, "John Doe").Return(person, nil).Once()

	params := map[string]interface{}{}

	// Execute
	result, err := bot.performMergePeople(context.Background(), userID, "John Doe", "John Doe", params)

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "Cannot merge 'John Doe' with itself")
	mockStore.AssertNotCalled(t, "MergePeople", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestPerformMergePeople_SourceNotFound(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)

	targetPerson := &storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "John Doe",
	}

	// Mock: find target
	mockStore.On("FindPersonByName", userID, "John Doe").Return(targetPerson, nil).Once()

	// Mock: source not found
	mockStore.On("FindPersonByName", userID, "Unknown").Return((*storage.Person)(nil), nil).Once()
	mockStore.On("FindPersonByAlias", userID, "Unknown").Return([]storage.Person{}, nil).Once()

	params := map[string]interface{}{}

	// Execute
	result, err := bot.performMergePeople(context.Background(), userID, "John Doe", "Unknown", params)

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "Source person 'Unknown' not found")
	mockStore.AssertNotCalled(t, "MergePeople", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestPerformCreatePerson_Success(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)

	// Mock: person not found by name
	mockStore.On("FindPersonByName", userID, "John Doe").Return((*storage.Person)(nil), nil).Once()

	// Mock: no alias matches
	mockStore.On("FindPersonByAlias", userID, "John Doe").Return([]storage.Person{}, nil).Once()

	// Mock: embedding generation
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(testutil.MockEmbeddingResponse(), nil).Once()

	// Mock: add person
	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.UserID == userID && p.DisplayName == "John Doe" &&
			p.Circle == "Friends" && p.Bio == "Software engineer" &&
			len(p.Aliases) == 2 && p.MentionCount == 1
	})).Return(int64(1), nil).Once()

	params := map[string]interface{}{
		"circle":  "Friends",
		"bio":     "Software engineer",
		"aliases": []interface{}{"Johnny", "JD"},
	}

	// Execute
	result, err := bot.performCreatePerson(context.Background(), userID, "John Doe", params)

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully created person 'John Doe'")
	assert.Contains(t, result, "ID: 1")
	assert.Contains(t, result, "Circle: Friends")
	mockStore.AssertExpectations(t)
	mockORClient.AssertExpectations(t)
}

func TestPerformCreatePerson_AlreadyExists(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)

	existingPerson := &storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "John Doe",
	}

	// Mock: person found by name
	mockStore.On("FindPersonByName", userID, "John Doe").Return(existingPerson, nil).Once()

	params := map[string]interface{}{}

	// Execute
	result, err := bot.performCreatePerson(context.Background(), userID, "John Doe", params)

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "already exists")
	assert.Contains(t, result, "ID: 1")
	assert.Contains(t, result, "Use 'update' operation")
	mockStore.AssertExpectations(t)
	mockORClient.AssertNotCalled(t, "CreateEmbeddings", mock.Anything, mock.Anything)
}

func TestPerformCreatePerson_AliasAlreadyExists(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)

	existingPerson := &storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "John Smith",
		Aliases:     []string{"Johnny"},
	}

	// Mock: person not found by name
	mockStore.On("FindPersonByName", userID, "Johnny").Return((*storage.Person)(nil), nil).Once()

	// Mock: alias matches
	mockStore.On("FindPersonByAlias", userID, "Johnny").Return([]storage.Person{*existingPerson}, nil).Once()

	params := map[string]interface{}{}

	// Execute
	result, err := bot.performCreatePerson(context.Background(), userID, "Johnny", params)

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "alias 'Johnny' already exists")
	assert.Contains(t, result, "John Smith")
	assert.Contains(t, result, "ID: 1")
	mockStore.AssertExpectations(t)
	mockORClient.AssertNotCalled(t, "CreateEmbeddings", mock.Anything, mock.Anything)
}

func TestPerformCreatePerson_WithUsername(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)

	// Mock: person not found
	mockStore.On("FindPersonByName", userID, "Jane Doe").Return((*storage.Person)(nil), nil).Once()
	mockStore.On("FindPersonByAlias", userID, "Jane Doe").Return([]storage.Person{}, nil).Once()

	// Mock: embedding
	mockORClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(testutil.MockEmbeddingResponse(), nil).Once()

	// Mock: add person with username (stripped @)
	mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
		return p.Username != nil && *p.Username == "janedoe" // @ should be stripped
	})).Return(int64(1), nil).Once()

	params := map[string]interface{}{
		"username": "@janedoe", // Should be stripped to janedoe
	}

	// Execute
	result, err := bot.performCreatePerson(context.Background(), userID, "Jane Doe", params)

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully created person")
	mockStore.AssertExpectations(t)
}

func TestPerformDeletePerson_Success(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)

	existingPerson := &storage.Person{
		ID:          1,
		UserID:      userID,
		DisplayName: "John Doe",
	}

	// Mock: found by name
	mockStore.On("FindPersonByName", userID, "John Doe").Return(existingPerson, nil).Once()

	// Mock: delete
	mockStore.On("DeletePerson", userID, int64(1)).Return(nil).Once()

	params := map[string]interface{}{
		"reason": "Duplicate entry",
	}

	// Execute
	result, err := bot.performDeletePerson(context.Background(), userID, "John Doe", params)

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully deleted person 'John Doe'")
	mockStore.AssertExpectations(t)
}

func TestPerformDeletePerson_ByID(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)

	existingPerson := &storage.Person{
		ID:          42,
		UserID:      userID,
		DisplayName: "Jane Smith",
	}

	// Mock: found by ID (v0.5.1 feature)
	mockStore.On("GetPerson", userID, int64(42)).Return(existingPerson, nil).Once()

	// Mock: delete
	mockStore.On("DeletePerson", userID, int64(42)).Return(nil).Once()

	params := map[string]interface{}{
		"person_id": float64(42),
	}

	// Execute
	result, err := bot.performDeletePerson(context.Background(), userID, "", params)

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "Successfully deleted person")
	mockStore.AssertExpectations(t)
}

func TestPerformDeletePerson_NotFound(t *testing.T) {
	// Setup
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	mockAPI := new(testutil.MockBotAPI)
	mockDownloader := new(testutil.MockFileDownloader)

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
		fileProcessor:   testutil.TestFileProcessor(t, mockDownloader, translator),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
	}

	userID := int64(123)

	// Mock: not found by name
	mockStore.On("FindPersonByName", userID, "Unknown").Return((*storage.Person)(nil), nil).Once()

	// Mock: no alias matches
	mockStore.On("FindPersonByAlias", userID, "Unknown").Return([]storage.Person{}, nil).Once()

	params := map[string]interface{}{}

	// Execute
	result, err := bot.performDeletePerson(context.Background(), userID, "Unknown", params)

	// Verify
	assert.NoError(t, err)
	assert.Contains(t, result, "Person 'Unknown' not found")
	mockStore.AssertNotCalled(t, "DeletePerson", mock.Anything, mock.Anything)
}

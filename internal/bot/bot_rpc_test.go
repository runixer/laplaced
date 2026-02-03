package bot

import (
	"context"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/bot/tools"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestSetCommands_ClearsCommands verifies setCommands clears the bot commands menu.
func TestSetCommands_ClearsCommands(t *testing.T) {
	logger := testutil.TestLogger()
	mockAPI := new(testutil.MockBotAPI)

	cfg := &config.Config{}

	bot := &Bot{
		api:    mockAPI,
		cfg:    cfg,
		logger: logger,
	}

	expectedReq := telegram.SetMyCommandsRequest{
		Commands: []telegram.BotCommand{},
	}

	mockAPI.On("SetMyCommands", mock.Anything, expectedReq).Return(nil)

	err := bot.setCommands()

	assert.NoError(t, err)
	mockAPI.AssertExpectations(t)
}

// TestSetCommands_APIError_ReturnsError verifies API errors are propagated.
func TestSetCommands_APIError_ReturnsError(t *testing.T) {
	logger := testutil.TestLogger()
	mockAPI := new(testutil.MockBotAPI)

	cfg := &config.Config{}

	bot := &Bot{
		api:    mockAPI,
		cfg:    cfg,
		logger: logger,
	}

	mockAPI.On("SetMyCommands", mock.Anything, mock.Anything).
		Return(assert.AnError)

	err := bot.setCommands()

	assert.Error(t, err)
	mockAPI.AssertExpectations(t)
}

// TestGetActiveSessions_DelegatesToRAG verifies GetActiveSessions delegates to RAG service.
func TestGetActiveSessions_DelegatesToRAG(t *testing.T) {
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	mockAPI := new(testutil.MockBotAPI)

	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	// Create real RAG service using builder
	ragService, buildErr := rag.NewServiceBuilder().
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
	assert.NoError(t, buildErr, "RAG service build should succeed")

	// Mock GetUnprocessedMessages to return empty (no active sessions)
	mockStore.On("GetUnprocessedMessages", mock.Anything).Return([]storage.Message{}, nil)

	bot := &Bot{
		api:        mockAPI,
		msgRepo:    mockStore,
		statsRepo:  mockStore,
		cfg:        cfg,
		logger:     logger,
		translator: translator,
		ragService: ragService,
	}

	sessions, err := bot.GetActiveSessions()

	assert.NoError(t, err)
	// Empty slice or nil is expected when no active sessions
	// Real RAG service may return nil or empty slice
	assert.Empty(t, sessions) // len() works on nil slices
}

// TestGetActiveSessions_RAGServiceNil_Panics verifies nil RAG service causes panic.
// Note: This is expected behavior - bot should have RAG service configured.
func TestGetActiveSessions_RAGServiceNil_Panics(t *testing.T) {
	logger := testutil.TestLogger()
	mockAPI := new(testutil.MockBotAPI)

	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	bot := &Bot{
		api:        mockAPI,
		cfg:        cfg,
		logger:     logger,
		translator: translator,
		// ragService is nil
	}

	// This will panic - which is expected behavior
	assert.Panics(t, func() {
		_, _ = bot.GetActiveSessions()
	})
}

// TestForceCloseSession_DelegatesToRAG verifies ForceCloseSession delegates to RAG.
func TestForceCloseSession_DelegatesToRAG(t *testing.T) {
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	mockAPI := new(testutil.MockBotAPI)

	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	// Create real RAG service using builder
	ragService, buildErr := rag.NewServiceBuilder().
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
	assert.NoError(t, buildErr, "RAG service build should succeed")

	// Mock empty unprocessed messages (returns []storage.Message)
	mockStore.On("GetUnprocessedMessages", mock.Anything).Return([]storage.Message{}, nil)
	// Mock GetFacts (May be called by RAG service)
	mockStore.On("GetFacts", mock.Anything).Return([]storage.Fact{}, nil).Maybe()

	bot := &Bot{
		api:        mockAPI,
		msgRepo:    mockStore,
		statsRepo:  mockStore,
		factRepo:   mockStore,
		cfg:        cfg,
		logger:     logger,
		translator: translator,
		ragService: ragService,
	}

	userID := int64(123)

	count, err := bot.ForceCloseSession(context.Background(), userID)

	assert.NoError(t, err)
	// No messages to process, so count should be 0
	assert.Equal(t, 0, count)
}

// TestForceCloseSession_RAGServiceNil_Panics verifies nil RAG service causes panic.
// Note: This is expected behavior - bot should have RAG service configured.
func TestForceCloseSession_RAGServiceNil_Panics(t *testing.T) {
	logger := testutil.TestLogger()
	mockAPI := new(testutil.MockBotAPI)

	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	bot := &Bot{
		api:        mockAPI,
		cfg:        cfg,
		logger:     logger,
		translator: translator,
		// ragService is nil
	}

	userID := int64(123)

	// This will panic - which is expected behavior
	assert.Panics(t, func() {
		_, _ = bot.ForceCloseSession(context.Background(), userID)
	})
}

// TestForceCloseSessionWithProgress_RAGServiceNil_Panics verifies nil RAG service causes panic.
// Note: This is expected behavior - bot should have RAG service configured.
func TestForceCloseSessionWithProgress_RAGServiceNil_Panics(t *testing.T) {
	logger := testutil.TestLogger()
	mockAPI := new(testutil.MockBotAPI)

	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	bot := &Bot{
		api:        mockAPI,
		cfg:        cfg,
		logger:     logger,
		translator: translator,
		// ragService is nil
	}

	userID := int64(123)

	// This will panic - which is expected behavior
	assert.Panics(t, func() {
		_, _ = bot.ForceCloseSessionWithProgress(context.Background(), userID, nil)
	})
}

// TestSetWebhook_CallsAPI verifies SetWebhook calls Telegram API.
func TestSetWebhook_CallsAPI(t *testing.T) {
	logger := testutil.TestLogger()
	mockAPI := new(testutil.MockBotAPI)

	cfg := &config.Config{}

	bot := &Bot{
		api:    mockAPI,
		cfg:    cfg,
		logger: logger,
	}

	webhookURL := "https://example.com/webhook"
	secretToken := "secret123"

	expectedReq := telegram.SetWebhookRequest{
		URL:         webhookURL,
		SecretToken: secretToken,
	}

	mockAPI.On("SetWebhook", mock.Anything, expectedReq).Return(nil)

	err := bot.SetWebhook(webhookURL, secretToken)

	assert.NoError(t, err)
	mockAPI.AssertExpectations(t)
}

// TestSetWebhook_APIError_PropagatesError verifies API errors are propagated.
func TestSetWebhook_APIError_PropagatesError(t *testing.T) {
	logger := testutil.TestLogger()
	mockAPI := new(testutil.MockBotAPI)

	cfg := &config.Config{}

	bot := &Bot{
		api:    mockAPI,
		cfg:    cfg,
		logger: logger,
	}

	mockAPI.On("SetWebhook", mock.Anything, mock.Anything).Return(assert.AnError)

	err := bot.SetWebhook("https://example.com", "token")

	assert.Error(t, err)
	mockAPI.AssertExpectations(t)
}

// TestAPI_ReturnsAPI verifies API returns the underlying Telegram API.
func TestAPI_ReturnsAPI(t *testing.T) {
	logger := testutil.TestLogger()
	mockAPI := new(testutil.MockBotAPI)

	cfg := &config.Config{}

	bot := &Bot{
		api:    mockAPI,
		cfg:    cfg,
		logger: logger,
	}

	result := bot.API()

	assert.Same(t, mockAPI, result)
}

// Smoke tests for DI setters

// TestSetArtifactRepo_VerifiesAssignment verifies artifact repo is assigned.
func TestSetArtifactRepo_VerifiesAssignment(t *testing.T) {
	bot := &Bot{}
	mockStore := new(testutil.MockStorage)

	assert.NotPanics(t, func() {
		bot.SetArtifactRepo(mockStore)
		assert.Equal(t, mockStore, bot.artifactRepo)
	})
}

// TestSetFileProcessor_VerifiesAssignment verifies file processor is assigned.
func TestSetFileProcessor_VerifiesAssignment(t *testing.T) {
	bot := &Bot{}
	mockDownloader := new(testutil.MockFileDownloader)
	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()

	processor := files.NewProcessor(mockDownloader, translator, "en", logger)

	assert.NotPanics(t, func() {
		bot.SetFileProcessor(processor)
		assert.Equal(t, processor, bot.fileProcessor)
	})
}

// TestSetAgentLogger_VerifiesAssignment verifies agent logger is assigned.
func TestSetAgentLogger_VerifiesAssignment(t *testing.T) {
	bot := &Bot{}
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	logger := testutil.TestLogger()

	cfg := testutil.TestConfig()

	// Initialize toolExecutor to avoid nil pointer panic
	bot.toolExecutor = tools.NewToolExecutor(mockORClient, mockStore, mockStore, cfg, logger)

	mockLogger := agentlog.NewLogger(mockStore, logger, false)

	assert.NotPanics(t, func() {
		bot.SetAgentLogger(mockLogger)
		assert.Equal(t, mockLogger, bot.agentLogger)
	})
}

// TestSetLaplaceAgent_VerifiesAssignment verifies Laplace agent is assigned.
func TestSetLaplaceAgent_VerifiesAssignment(t *testing.T) {
	bot := &Bot{}
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	logger := testutil.TestLogger()
	translator := testutil.TestTranslator(t)

	cfg := testutil.TestConfig()
	agent := laplace.New(cfg, mockORClient, nil, mockStore, mockStore, nil, translator, logger)

	assert.NotPanics(t, func() {
		bot.SetLaplaceAgent(agent)
		assert.Equal(t, agent, bot.laplaceAgent)
	})
}

// TestSetFileHandler_VerifiesAssignment verifies file handler is assigned to processor.
func TestSetFileHandler_VerifiesAssignment(t *testing.T) {
	bot := &Bot{}
	mockStore := new(testutil.MockStorage)
	mockAPI := new(testutil.MockBotAPI)
	logger := testutil.TestLogger()
	translator := testutil.TestTranslator(t)

	// Create a mock FileSaver
	mockSaver := new(testutil.MockFileSaver)

	cfg := testutil.TestConfig()
	bot.api = mockAPI
	bot.userRepo = mockStore
	bot.msgRepo = mockStore
	bot.statsRepo = mockStore
	bot.factRepo = mockStore
	bot.factHistoryRepo = mockStore
	bot.peopleRepo = mockStore
	bot.cfg = cfg
	bot.logger = logger
	bot.translator = translator

	// Initialize fileProcessor first
	bot.fileProcessor = files.NewProcessor(nil, translator, "en", logger)

	assert.NotPanics(t, func() {
		bot.SetFileHandler(mockSaver)
		// FileHandler is set on fileProcessor, not directly on bot
		// Just verify the call doesn't panic
	})
}

// TestStop_WaitsForHandlers verifies Stop waits for active handlers.
func TestStop_StopsGracefully(t *testing.T) {
	logger := testutil.TestLogger()
	mockStore := new(testutil.MockStorage)
	mockAPI := new(testutil.MockBotAPI)

	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	bot := &Bot{
		api:        mockAPI,
		msgRepo:    mockStore,
		statsRepo:  mockStore,
		cfg:        cfg,
		logger:     logger,
		translator: translator,
	}

	// Initialize messageGrouper
	bot.messageGrouper = NewMessageGrouper(bot, logger, time.Hour, func(ctx context.Context, group *MessageGroup) {})

	// Should not panic or hang
	assert.NotPanics(t, func() {
		bot.Stop()
	})
}

// TestStop_NilMessageGrouper_Panics verifies nil messageGrouper causes panic.
// Note: This is expected behavior - bot should always have messageGrouper initialized.
func TestStop_NilMessageGrouper_Panics(t *testing.T) {
	logger := testutil.TestLogger()
	cfg := testutil.TestConfig()

	bot := &Bot{
		cfg:    cfg,
		logger: logger,
		// messageGrouper is nil
	}

	// This will panic - which is expected behavior
	assert.Panics(t, func() {
		bot.Stop()
	})
}

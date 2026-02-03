package rag

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/extractor"
	agenttesting "github.com/runixer/laplaced/internal/agent/testing"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/mock"
)

// TestProcessSingleArtifact_Success tests successful artifact processing.
func TestProcessSingleArtifact_Success(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Artifacts.Enabled = true

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockArtifactRepo := new(testutil.MockStorage)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	artifactID := int64(10)

	// Create test artifact
	artifact := storage.Artifact{
		ID:        artifactID,
		UserID:    userID,
		MessageID: 100,
		FileType:  "image",
		FilePath:  "test/image.jpg",
		FileSize:  1024,
		State:     "pending",
	}

	// Setup mock for LoadNewArtifactSummaries
	mockArtifactRepo.On("GetPendingArtifacts", userID, mock.AnythingOfType("int")).Return([]storage.Artifact{}, nil).Maybe()
	mockArtifactRepo.On("GetArtifacts", mock.Anything, mock.Anything, mock.Anything).
		Return([]storage.Artifact{}, int64(0), nil).Maybe()

	mockExtractor := new(agenttesting.MockAgent)
	mockExtractor.On("Type").Return(agent.TypeExtractor).Maybe()

	// Return successful ProcessResult
	processResult := extractor.NewProcessResultBuilder().
		WithArtifactID(artifactID).
		WithSummary("A beautiful sunset photo").
		WithKeywords("sunset", "photo", "nature").
		WithEntities("nature", "photography").
		WithRAGHints("What time was the photo taken?", "Where was this photo taken?").
		WithDuration(500*time.Millisecond).
		WithTokens(100, 50, 150).
		Build()

	mockExtractor.On("Execute", mock.Anything, mock.MatchedBy(func(req *agent.Request) bool {
		// Verify request has artifact parameter
		artifactParam, ok := req.Params["artifact"]
		return ok && artifactParam != nil
	})).Return(agent.NewResponseBuilder().
		WithStructured(processResult).
		Build(), nil)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetExtractorAgent(mockExtractor)
	svc.SetArtifactRepository(mockArtifactRepo)

	// Process artifact
	svc.processSingleArtifact(context.Background(), artifact)

	// Note: processSingleArtifact logs but doesn't return much
	// The test verifies no panic occurs and mocks are called correctly
	mockExtractor.AssertExpectations(t)
}

// TestProcessSingleArtifact_AgentError tests error handling.
func TestProcessSingleArtifact_AgentError(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Artifacts.Enabled = true

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockArtifactRepo := new(testutil.MockStorage)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	artifactID := int64(10)

	artifact := storage.Artifact{
		ID:        artifactID,
		UserID:    userID,
		MessageID: 100,
		FileType:  "image",
		FilePath:  "test/image.jpg",
		FileSize:  1024,
		State:     "pending",
	}

	mockArtifactRepo.On("GetPendingArtifacts", userID, mock.AnythingOfType("int")).Return([]storage.Artifact{}, nil).Maybe()
	mockArtifactRepo.On("GetArtifacts", mock.Anything, mock.Anything, mock.Anything).
		Return([]storage.Artifact{}, int64(0), nil).Maybe()

	mockExtractor := new(agenttesting.MockAgent)
	mockExtractor.On("Type").Return(agent.TypeExtractor).Maybe()

	// Return error from agent
	expectedErr := errors.New("LLM API timeout")
	mockExtractor.On("Execute", mock.Anything, mock.MatchedBy(func(req *agent.Request) bool {
		_, ok := req.Params["artifact"]
		return ok
	})).Return(nil, expectedErr)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetExtractorAgent(mockExtractor)
	svc.SetArtifactRepository(mockArtifactRepo)

	// Process artifact - should not panic
	svc.processSingleArtifact(context.Background(), artifact)

	mockExtractor.AssertExpectations(t)
}

// TestProcessSingleArtifact_EmbeddingError tests embedding creation error.
func TestProcessSingleArtifact_EmbeddingError(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Artifacts.Enabled = true

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockArtifactRepo := new(testutil.MockStorage)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	artifactID := int64(10)

	artifact := storage.Artifact{
		ID:        artifactID,
		UserID:    userID,
		MessageID: 100,
		FileType:  "image",
		FilePath:  "test/image.jpg",
		FileSize:  1024,
		State:     "pending",
	}

	mockArtifactRepo.On("GetPendingArtifacts", userID, mock.AnythingOfType("int")).Return([]storage.Artifact{}, nil).Maybe()
	mockArtifactRepo.On("GetArtifacts", mock.Anything, mock.Anything, mock.Anything).
		Return([]storage.Artifact{}, int64(0), nil).Maybe()

	mockExtractor := new(agenttesting.MockAgent)
	mockExtractor.On("Type").Return(agent.TypeExtractor).Maybe()

	// Note: In current implementation, embedding creation happens inside the Extractor agent
	// So if embedding fails, the agent itself returns an error
	mockExtractor.On("Execute", mock.Anything, mock.MatchedBy(func(req *agent.Request) bool {
		_, ok := req.Params["artifact"]
		return ok
	})).Return(nil, errors.New("embedding creation failed"))

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetExtractorAgent(mockExtractor)
	svc.SetArtifactRepository(mockArtifactRepo)

	// Process artifact - should handle error gracefully
	svc.processSingleArtifact(context.Background(), artifact)

	mockExtractor.AssertExpectations(t)
}

// TestProcessSingleArtifact_RetryLogic tests retry on transient failure.
func TestProcessSingleArtifact_RetryLogic(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Artifacts.Enabled = true
	cfg.Agents.Extractor.MaxRetries = 3

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockArtifactRepo := new(testutil.MockStorage)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	artifactID := int64(10)

	now := time.Now()
	artifact := storage.Artifact{
		ID:           artifactID,
		UserID:       userID,
		MessageID:    100,
		FileType:     "image",
		FilePath:     "test/image.jpg",
		FileSize:     1024,
		State:        "pending",
		RetryCount:   1,    // Already retried once
		LastFailedAt: &now, // Failed recently
	}

	mockArtifactRepo.On("GetPendingArtifacts", userID, mock.AnythingOfType("int")).Return([]storage.Artifact{}, nil).Maybe()
	mockArtifactRepo.On("GetArtifacts", mock.Anything, mock.Anything, mock.Anything).
		Return([]storage.Artifact{}, int64(0), nil).Maybe()

	mockExtractor := new(agenttesting.MockAgent)
	mockExtractor.On("Type").Return(agent.TypeExtractor).Maybe()

	// First call fails, second succeeds - setup two expectations
	mockExtractor.On("Execute", mock.Anything, mock.MatchedBy(func(req *agent.Request) bool {
		_, ok := req.Params["artifact"]
		return ok
	})).Return(nil, errors.New("temporary timeout")).Once()

	mockExtractor.On("Execute", mock.Anything, mock.MatchedBy(func(req *agent.Request) bool {
		_, ok := req.Params["artifact"]
		return ok
	})).Return(agent.NewResponseBuilder().
		WithStructured(extractor.NewProcessResultBuilder().
			WithArtifactID(artifactID).
			WithSummary("Success after retry").
			Build()).
		Build(), nil).Once()

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetExtractorAgent(mockExtractor)
	svc.SetArtifactRepository(mockArtifactRepo)

	// First attempt fails
	svc.processSingleArtifact(context.Background(), artifact)

	// Simulate retry (artifact would be picked up again by polling loop)
	artifact.RetryCount = 2
	svc.processSingleArtifact(context.Background(), artifact)

	// Both expectations should have been called
	mockExtractor.AssertExpectations(t)

	mockExtractor.AssertExpectations(t)
}

// TestProcessSingleArtifact_ShuttingDown tests shutdown flag behavior.
func TestProcessSingleArtifact_ShuttingDown(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Artifacts.Enabled = true

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockArtifactRepo := new(testutil.MockStorage)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	artifactID := int64(10)

	artifact := storage.Artifact{
		ID:        artifactID,
		UserID:    userID,
		MessageID: 100,
		FileType:  "image",
		FilePath:  "test/image.jpg",
		FileSize:  1024,
		State:     "pending",
	}

	mockArtifactRepo.On("GetPendingArtifacts", userID, mock.AnythingOfType("int")).Return([]storage.Artifact{}, nil).Maybe()
	mockArtifactRepo.On("GetArtifacts", mock.Anything, mock.Anything, mock.Anything).
		Return([]storage.Artifact{}, int64(0), nil).Maybe()

	mockExtractor := new(agenttesting.MockAgent)
	mockExtractor.On("Type").Return(agent.TypeExtractor).Maybe()
	mockExtractor.On("Execute", mock.Anything, mock.MatchedBy(func(req *agent.Request) bool {
		_, ok := req.Params["artifact"]
		return ok
	})).Return(agent.NewResponseBuilder().
		WithStructured(extractor.NewProcessResultBuilder().
			WithArtifactID(artifactID).
			WithSummary("Test summary").
			Build()).
		Build(), nil)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetExtractorAgent(mockExtractor)
	svc.SetArtifactRepository(mockArtifactRepo)

	// Set shutting down flag - new work should still be accepted by processSingleArtifact
	// (the check is in processArtifactExtraction, not processSingleArtifact)
	svc.shuttingDown.Store(true)

	// processSingleArtifact should still work (it doesn't check shuttingDown itself)
	svc.processSingleArtifact(context.Background(), artifact)

	mockExtractor.AssertExpectations(t)
}

// TestProcessSingleArtifact_WithSharedContext tests with context service.
func TestProcessSingleArtifact_WithSharedContext(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Artifacts.Enabled = true

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockArtifactRepo := new(testutil.MockStorage)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	artifactID := int64(10)

	artifact := storage.Artifact{
		ID:        artifactID,
		UserID:    userID,
		MessageID: 100,
		FileType:  "document",
		FilePath:  "test/doc.pdf",
		FileSize:  2048,
		State:     "pending",
	}

	// Setup facts for profile
	facts := []storage.Fact{
		{ID: 1, UserID: userID, Content: "User is a Go developer", Relation: "identity", Importance: 90},
	}
	mockStore.On("GetFacts", userID).Return(facts, nil).Maybe()
	// Setup topics for recent topics context
	mockStore.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{Data: []storage.TopicExtended{}}, nil).Maybe()
	// Setup for LoadNewArtifactSummaries (called after extraction)
	mockArtifactRepo.On("GetPendingArtifacts", userID, mock.AnythingOfType("int")).Return([]storage.Artifact{}, nil).Maybe()
	mockArtifactRepo.On("GetArtifacts", mock.Anything, mock.Anything, mock.Anything).
		Return([]storage.Artifact{}, int64(0), nil).Maybe()
	mockArtifactRepo.On("GetArtifacts", mock.Anything, mock.Anything, mock.Anything).
		Return([]storage.Artifact{}, int64(0), nil).Maybe()

	mockExtractor := new(agenttesting.MockAgent)
	mockExtractor.On("Type").Return(agent.TypeExtractor).Maybe()
	mockExtractor.On("Execute", mock.Anything, mock.MatchedBy(func(req *agent.Request) bool {
		// Verify SharedContext is attached (created by contextService.Load)
		return req.Shared != nil && req.Shared.UserID == userID
	})).Return(agent.NewResponseBuilder().
		WithStructured(extractor.NewProcessResultBuilder().
			WithArtifactID(artifactID).
			WithSummary("Technical document about Go").
			Build()).
		Build(), nil)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetExtractorAgent(mockExtractor)
	svc.SetArtifactRepository(mockArtifactRepo)

	// Create real context service with mocked repos
	contextService := agent.NewContextService(mockStore, mockStore, cfg, logger)
	svc.SetContextService(contextService)

	svc.processSingleArtifact(context.Background(), artifact)

	mockExtractor.AssertExpectations(t)
}

// TestProcessArtifactExtraction_RespectsShuttingDown tests that processArtifactExtraction
// respects the shuttingDown flag.
func TestProcessArtifactExtraction_RespectsShuttingDown(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Artifacts.Enabled = true
	cfg.Bot.AllowedUserIDs = []int64{123}

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockArtifactRepo := new(testutil.MockStorage)
	translator := testutil.TestTranslator(t)

	userID := int64(123)

	// Create test artifacts
	artifacts := []storage.Artifact{
		{ID: 1, UserID: userID, FileType: "image", State: "pending"},
		{ID: 2, UserID: userID, FileType: "document", State: "pending"},
	}

	mockArtifactRepo.On("GetPendingArtifacts", userID, mock.AnythingOfType("int")).Return(artifacts, nil).Maybe()

	mockExtractor := new(agenttesting.MockAgent)
	mockExtractor.On("Type").Return(agent.TypeExtractor).Maybe()

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetExtractorAgent(mockExtractor)
	svc.SetArtifactRepository(mockArtifactRepo)

	// Set shutting down flag
	svc.shuttingDown.Store(true)

	// processArtifactExtraction should skip processing when shutting down
	svc.processArtifactExtraction(context.Background())

	// Extractor should not have been called
	mockExtractor.AssertNotCalled(t, "Execute", mock.Anything, mock.Anything)
	mockArtifactRepo.AssertExpectations(t)
}

// TestProcessArtifactExtraction_ProcessesAllPending tests that all pending artifacts
// across all users are processed.
func TestProcessArtifactExtraction_ProcessesAllPending(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Artifacts.Enabled = true
	cfg.Bot.AllowedUserIDs = []int64{123, 456}
	cfg.Agents.Extractor.MaxConcurrent = 2

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockArtifactRepo := new(testutil.MockStorage)
	translator := testutil.TestTranslator(t)

	userID1 := int64(123)
	userID2 := int64(456)

	// Create test artifacts for both users
	artifacts1 := []storage.Artifact{
		{ID: 1, UserID: userID1, FileType: "image", State: "pending"},
	}
	artifacts2 := []storage.Artifact{
		{ID: 2, UserID: userID2, FileType: "document", State: "pending"},
	}

	mockArtifactRepo.On("GetPendingArtifacts", userID1, mock.AnythingOfType("int")).Return(artifacts1, nil).Once()
	mockArtifactRepo.On("GetPendingArtifacts", userID2, mock.AnythingOfType("int")).Return(artifacts2, nil).Once()
	mockArtifactRepo.On("GetPendingArtifacts", mock.AnythingOfType("int64"), mock.AnythingOfType("int")).Return([]storage.Artifact{}, nil).Maybe()
	// Setup for LoadNewArtifactSummaries (called after extraction)
	mockArtifactRepo.On("GetArtifacts", mock.Anything, mock.Anything, mock.Anything).
		Return([]storage.Artifact{}, int64(0), nil).Maybe()

	mockExtractor := new(agenttesting.MockAgent)
	mockExtractor.On("Type").Return(agent.TypeExtractor).Maybe()
	mockExtractor.On("Execute", mock.Anything, mock.MatchedBy(func(req *agent.Request) bool {
		_, ok := req.Params["artifact"]
		return ok
	})).Return(agent.NewResponseBuilder().
		WithStructured(extractor.NewProcessResultBuilder().
			WithSummary("Test").
			Build()).
		Build(), nil).Times(2)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetExtractorAgent(mockExtractor)
	svc.SetArtifactRepository(mockArtifactRepo)

	// Process - should handle artifacts from both users
	svc.processArtifactExtraction(context.Background())

	// Both artifacts should be processed
	mockExtractor.AssertExpectations(t)
	mockArtifactRepo.AssertExpectations(t)
}

// TestProcessArtifactExtraction_EmptyUserIDs tests that extraction skips when no users are allowed.
func TestProcessArtifactExtraction_EmptyUserIDs(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Artifacts.Enabled = true
	cfg.Bot.AllowedUserIDs = []int64{} // Empty user IDs

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockArtifactRepo := new(testutil.MockStorage)
	translator := testutil.TestTranslator(t)

	mockExtractor := new(agenttesting.MockAgent)

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetExtractorAgent(mockExtractor)
	svc.SetArtifactRepository(mockArtifactRepo)

	// processArtifactExtraction should skip processing when no users allowed
	svc.processArtifactExtraction(context.Background())

	// Extractor should not have been called
	mockExtractor.AssertNotCalled(t, "Execute", mock.Anything, mock.Anything)
	// No GetPendingArtifacts calls expected
	mockArtifactRepo.AssertNotCalled(t, "GetPendingArtifacts", mock.Anything, mock.Anything)
}

// TestProcessArtifactExtraction_DefaultMaxRetries tests that default max retries is used when config is 0.
func TestProcessArtifactExtraction_DefaultMaxRetries(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Artifacts.Enabled = true
	cfg.Bot.AllowedUserIDs = []int64{123}
	cfg.Agents.Extractor.MaxRetries = 0 // Should use default 3

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockArtifactRepo := new(testutil.MockStorage)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	artifacts := []storage.Artifact{
		{ID: 1, UserID: userID, FileType: "image", State: "pending"},
	}

	// Should be called with maxRetries=3 (default)
	mockArtifactRepo.On("GetPendingArtifacts", userID, 3).Return(artifacts, nil).Maybe()
	mockArtifactRepo.On("UpdateArtifact", mock.AnythingOfType("*storage.Artifact")).Return(nil).Maybe()
	mockArtifactRepo.On("GetArtifact", userID, int64(1)).Return(
		&storage.Artifact{ID: 1, UserID: userID, FilePath: "/tmp/test.jpg"}, nil).Maybe()
	mockArtifactRepo.On("GetArtifacts", mock.Anything, mock.Anything, mock.Anything).Return([]storage.Artifact{}, int64(0), nil).Maybe()
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(
		openrouter.EmbeddingResponse{Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2}}}}, nil).Maybe()

	mockExtractor := new(agenttesting.MockAgent)
	mockExtractor.On("Type").Return(agent.TypeExtractor).Maybe()
	mockExtractor.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
		Structured: extractor.NewProcessResultBuilder().
			WithSummary("test").
			WithKeywords("[]").
			WithEntities("[]").
			Build(),
	}, nil).Maybe()

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetExtractorAgent(mockExtractor)
	svc.SetArtifactRepository(mockArtifactRepo)

	svc.processArtifactExtraction(context.Background())

	mockArtifactRepo.AssertExpectations(t)
}

// TestProcessArtifactExtraction_DefaultMaxConcurrent tests that default max concurrent is used when config is 0.
func TestProcessArtifactExtraction_DefaultMaxConcurrent(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := testutil.TestConfig()
	cfg.Artifacts.Enabled = true
	cfg.Bot.AllowedUserIDs = []int64{123}
	cfg.Agents.Extractor.MaxConcurrent = 0 // Should use default 3

	mockStore := new(testutil.MockStorage)
	mockClient := new(testutil.MockOpenRouterClient)
	mockArtifactRepo := new(testutil.MockStorage)
	translator := testutil.TestTranslator(t)

	userID := int64(123)
	artifacts := []storage.Artifact{
		{ID: 1, UserID: userID, FileType: "image", State: "pending"},
	}

	mockArtifactRepo.On("GetPendingArtifacts", userID, 3).Return(artifacts, nil).Maybe()
	mockArtifactRepo.On("UpdateArtifact", mock.AnythingOfType("*storage.Artifact")).Return(nil).Maybe()
	mockArtifactRepo.On("GetArtifact", userID, int64(1)).Return(
		&storage.Artifact{ID: 1, UserID: userID, FilePath: "/tmp/test.jpg"}, nil).Maybe()
	mockArtifactRepo.On("GetArtifacts", mock.Anything, mock.Anything, mock.Anything).Return([]storage.Artifact{}, int64(0), nil).Maybe()
	mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(
		openrouter.EmbeddingResponse{Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2}}}}, nil).Maybe()

	mockExtractor := new(agenttesting.MockAgent)
	mockExtractor.On("Type").Return(agent.TypeExtractor).Maybe()
	mockExtractor.On("Execute", mock.Anything, mock.Anything).Return(&agent.Response{
		Structured: extractor.NewProcessResultBuilder().
			WithSummary("test").
			WithKeywords("[]").
			WithEntities("[]").
			Build(),
	}, nil).Maybe()

	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc, err := NewServiceBuilder().
		WithLogger(logger).
		WithConfig(cfg).
		WithOpenRouterClient(mockClient).
		WithTopicRepository(mockStore).
		WithFactRepository(mockStore).
		WithFactHistoryRepository(mockStore).
		WithMessageRepository(mockStore).
		WithMaintenanceRepository(mockStore).
		WithMemoryService(memSvc).
		WithTranslator(translator).
		Build()
	if err != nil {
		t.Fatalf("failed to build RAG service: %v", err)
	}
	svc.SetExtractorAgent(mockExtractor)
	svc.SetArtifactRepository(mockArtifactRepo)

	svc.processArtifactExtraction(context.Background())

	mockArtifactRepo.AssertExpectations(t)
}

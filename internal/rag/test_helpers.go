package rag

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/mock"
)

// TestRAGConfig returns a test configuration for RAG service with custom options.
func TestRAGConfig(opts ...func(*config.Config)) *config.Config {
	cfg := testutil.TestConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// TestRAGService creates a fully configured RAG service for testing.
// It handles all the boilerplate of creating mocks, services, and calling Start().
//
// Usage:
//
//	mockStore := new(testutil.MockStorage)
//	mockClient := new(testutil.MockOpenRouterClient)
//
//	svc := TestRAGService(t, mockStore, mockClient)
//	// Use svc for testing...
func TestRAGService(t *testing.T, store *testutil.MockStorage, client *testutil.MockOpenRouterClient, opts ...func(*config.Config)) *Service {
	t.Helper()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := TestRAGConfig(opts...)
	translator := testutil.TestTranslator(t)

	memSvc := memory.NewService(logger, cfg, store, store, store, client, translator)
	svc := NewService(logger, cfg, store, store, store, store, store, client, memSvc, translator)

	// Setup common mocks before starting to avoid unexpected method calls
	SetupCommonRAGMocks(store)

	err := svc.Start(context.Background())
	if err != nil {
		t.Fatalf("failed to start RAG service: %v", err)
	}

	return svc
}

// TestRAGServiceWithAgents creates a RAG service with mock agents for testing.
// It sets up mock enricher and reranker agents.
func TestRAGServiceWithAgents(
	t *testing.T,
	store *testutil.MockStorage,
	client *testutil.MockOpenRouterClient,
	enricherAgent agent.Agent,
	rerankerAgent agent.Agent,
	opts ...func(*config.Config),
) *Service {
	t.Helper()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := TestRAGConfig(opts...)
	translator := testutil.TestTranslator(t)

	memSvc := memory.NewService(logger, cfg, store, store, store, client, translator)
	svc := NewService(logger, cfg, store, store, store, store, store, client, memSvc, translator)

	if enricherAgent != nil {
		svc.SetEnricherAgent(enricherAgent)
	}
	if rerankerAgent != nil {
		svc.SetRerankerAgent(rerankerAgent)
	}

	err := svc.Start(context.Background())
	if err != nil {
		t.Fatalf("failed to start RAG service: %v", err)
	}

	return svc
}

// TestRAGServiceNoStart creates a RAG service without starting it.
// Use this when testing methods that don't require background loops.
func TestRAGServiceNoStart(t *testing.T, store *testutil.MockStorage, client *testutil.MockOpenRouterClient, opts ...func(*config.Config)) *Service {
	t.Helper()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := TestRAGConfig(opts...)
	translator := testutil.TestTranslator(t)

	memSvc := memory.NewService(logger, cfg, store, store, store, client, translator)
	svc := NewService(logger, cfg, store, store, store, store, store, client, memSvc, translator)

	return svc
}

// TestRAGServiceWithSetup creates a RAG service and calls a setup function before starting it.
// Use this when you need custom setup like setting agents or configuring mock expectations.
func TestRAGServiceWithSetup(
	t *testing.T,
	store *testutil.MockStorage,
	client *testutil.MockOpenRouterClient,
	setup func(*Service, *memory.Service),
	opts ...func(*config.Config),
) *Service {
	t.Helper()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := TestRAGConfig(opts...)
	translator := testutil.TestTranslator(t)

	memSvc := memory.NewService(logger, cfg, store, store, store, client, translator)
	svc := NewService(logger, cfg, store, store, store, store, store, client, memSvc, translator)

	// Setup common mocks before starting to avoid unexpected method calls
	SetupCommonRAGMocks(store)

	if setup != nil {
		setup(svc, memSvc)
	}

	err := svc.Start(context.Background())
	if err != nil {
		t.Fatalf("failed to start RAG service: %v", err)
	}

	return svc
}

// SetupCommonRAGMocks sets up the most common mock expectations for RAG tests.
// Call this before creating the RAG service to avoid "Unexpected method call" errors.
func SetupCommonRAGMocks(store *testutil.MockStorage) {
	// These methods are called during RAG service operations
	store.On("GetAllTopics", mock.Anything).Return([]storage.Topic{}, nil).Maybe()
	store.On("GetAllFacts", mock.Anything).Return([]storage.Fact{}, nil).Maybe()
	store.On("GetAllPeople", mock.Anything).Return([]storage.Person{}, nil).Maybe()
}

// SetupMergeCandidatesMock sets up mock expectations for merge candidate operations.
func SetupMergeCandidatesMock(store *testutil.MockStorage, userID int64, candidates []storage.MergeCandidate) {
	store.On("GetMergeCandidates", userID, mock.Anything).Return(candidates, nil).Maybe()
	store.On("SetTopicConsolidationChecked", userID, mock.Anything, true).Return(nil).Maybe()
}

// SetupTopicsPendingFactsMock sets up mock expectations for fact extraction operations.
func SetupTopicsPendingFactsMock(store *testutil.MockStorage, userID int64, topics []storage.Topic, msgs []storage.Message) {
	store.On("GetTopicsPendingFacts", userID).Return(topics, nil).Maybe()
	for _, topic := range topics {
		store.On("GetMessagesByTopicID", mock.Anything, topic.ID).Return(msgs, nil).Maybe()
		store.On("SetTopicFactsExtracted", topic.UserID, topic.ID, true).Return(nil).Maybe()
	}
}

// MockCandidate creates a test merge candidate with two topics for testing.
func MockCandidate(id1, id2, userID int64, similarity float32) storage.MergeCandidate {
	return storage.MergeCandidate{
		Topic1: storage.Topic{ID: id1, UserID: userID, Embedding: []float32{1.0, 0.0, 0.0}},
		Topic2: storage.Topic{ID: id2, UserID: userID, Embedding: []float32{similarity, 1.0 - similarity, 0.0}},
	}
}

// MockTopic creates a test topic with minimal required fields.
func MockTopic(id, userID int64, embedding []float32) storage.Topic {
	return storage.Topic{
		ID:         id,
		UserID:     userID,
		Summary:    "Test topic",
		StartMsgID: id * 10,
		EndMsgID:   id*10 + 5,
		Embedding:  embedding,
	}
}

// MockFact creates a test fact with minimal required fields.
func MockFact(id, userID int64, embedding []float32) storage.Fact {
	return storage.Fact{
		ID:          id,
		UserID:      userID,
		Relation:    "test_relation",
		Category:    "test",
		Content:     "Test fact content",
		Type:        "context",
		Importance:  50,
		Embedding:   embedding,
		TopicID:     nil,
		CreatedAt:   time.Now(),
		LastUpdated: time.Now(),
	}
}

// MockPerson creates a test person with minimal required fields.
func MockPerson(id, userID int64, displayName string, embedding []float32) storage.Person {
	username := fmt.Sprintf("@user%d", id)
	return storage.Person{
		ID:           id,
		UserID:       userID,
		DisplayName:  displayName,
		Username:     &username,
		Circle:       "Other",
		Embedding:    embedding,
		FirstSeen:    time.Now(),
		LastSeen:     time.Now(),
		MentionCount: 1,
	}
}

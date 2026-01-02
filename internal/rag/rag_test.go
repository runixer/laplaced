package rag

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// --- Mocks ---

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
	if args.Get(0) == nil {
		return []storage.Topic{}, args.Error(1)
	}
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
func (m *MockStorage) UpdateFact(fact storage.Fact) error {
	args := m.Called(fact)
	return args.Error(0)
}
func (m *MockStorage) UpdateFactTopic(oldTopicID, newTopicID int64) error {
	args := m.Called(oldTopicID, newTopicID)
	return args.Error(0)
}
func (m *MockStorage) DeleteFact(userID, factID int64) error {
	args := m.Called(userID, factID)
	return args.Error(0)
}
func (m *MockStorage) AddFact(fact storage.Fact) (int64, error) {
	args := m.Called(fact)
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
func (m *MockStorage) GetAllFacts() ([]storage.Fact, error) {
	// Special handling for unexpected calls during initialization
	// This avoids panics when we forget to mock it in some tests
	// as calling m.Called() will panic if expectation is missing.
	// But we can't easily check for expectation without reflection hacks or trying Called.
	// We will attempt Called, but since it panics, we are stuck.
	// The standard way is to just mock it everywhere or make loadVectors strict.
	// But since I can't easily update all tests in one go, I will assume empty if not set up?
	// No, Called(args...) records the call.
	// Let's rely on the previous fix being applied but maybe I missed one instance?
	// Ah, I see I reverted `GetAllFacts` in a previous step to `return args.Get...`.
	// Let's add the nil check back properly.
	args := m.Called()
	if args.Get(0) == nil {
		return []storage.Fact{}, args.Error(1)
	}
	return args.Get(0).([]storage.Fact), args.Error(1)
}
func (m *MockStorage) GetFactsAfterID(minID int64) ([]storage.Fact, error) {
	args := m.Called(minID)
	if args.Get(0) == nil {
		return []storage.Fact{}, args.Error(1)
	}
	return args.Get(0).([]storage.Fact), args.Error(1)
}
func (m *MockStorage) GetFactStats() (storage.FactStats, error) {
	args := m.Called()
	return args.Get(0).(storage.FactStats), args.Error(1)
}
func (m *MockStorage) GetFactStatsByUser(userID int64) (storage.FactStats, error) {
	args := m.Called(userID)
	return args.Get(0).(storage.FactStats), args.Error(1)
}
func (m *MockStorage) GetFacts(userID int64) ([]storage.Fact, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return []storage.Fact{}, args.Error(1)
	}
	return args.Get(0).([]storage.Fact), args.Error(1)
}
func (m *MockStorage) GetFactsByIDs(ids []int64) ([]storage.Fact, error) {
	args := m.Called(ids)
	return args.Get(0).([]storage.Fact), args.Error(1)
}
func (m *MockStorage) GetUnprocessedMessages(userID int64) ([]storage.Message, error) {
	args := m.Called(userID)
	return args.Get(0).([]storage.Message), args.Error(1)
}
func (m *MockStorage) GetTopicExtractionLogs(limit, offset int) ([]storage.RAGLog, int, error) {
	args := m.Called(limit, offset)
	return args.Get(0).([]storage.RAGLog), args.Int(1), args.Error(2)
}
func (m *MockStorage) GetMergeCandidates(userID int64) ([]storage.MergeCandidate, error) {
	args := m.Called(userID)
	return args.Get(0).([]storage.MergeCandidate), args.Error(1)
}

type MockClient struct {
	mock.Mock
}

func (m *MockClient) CreateChatCompletion(ctx context.Context, req openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(openrouter.ChatCompletionResponse), args.Error(1)
}
func (m *MockClient) CreateEmbeddings(ctx context.Context, req openrouter.EmbeddingRequest) (openrouter.EmbeddingResponse, error) {
	args := m.Called(ctx, req)
	return args.Get(0).(openrouter.EmbeddingResponse), args.Error(1)
}

// --- Tests ---

func TestRetrieve_TopicsGrouping(t *testing.T) {
	// 1. Setup
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.RAG.SimilarityThreshold = 0.5
	cfg.RAG.RetrievedTopicsCount = 5
	cfg.RAG.EmbeddingModel = "test-model"
	cfg.RAG.QueryModel = "test-model"

	mockStore := new(MockStorage)
	mockClient := new(MockClient)

	// User ID
	userID := int64(123)

	// 2. Data
	// Topics
	topicA := storage.Topic{
		ID:         1,
		UserID:     userID,
		Summary:    "Topic A",
		StartMsgID: 10,
		EndMsgID:   12,
		Embedding:  []float32{1.0, 0.0, 0.0}, // Matches [1,0,0] perfectly
	}
	topicB := storage.Topic{
		ID:         2,
		UserID:     userID,
		Summary:    "Topic B",
		StartMsgID: 20,
		EndMsgID:   21,
		Embedding:  []float32{0.0, 1.0, 0.0}, // Matches [1,0,0] poorly (0.0)
	}
	topicC := storage.Topic{
		ID:         3,
		UserID:     userID,
		Summary:    "Topic C",
		StartMsgID: 30,
		EndMsgID:   32,
		Embedding:  []float32{0.9, 0.1, 0.0}, // Matches [1,0,0] well (0.9ish)
	}

	topics := []storage.Topic{topicA, topicB, topicC}

	// Messages
	msgsA := []storage.Message{
		{ID: 10, Role: "user", Content: "A1"},
		{ID: 11, Role: "assistant", Content: "A2"},
		{ID: 12, Role: "user", Content: "A3"},
	}
	msgsC := []storage.Message{
		{ID: 30, Role: "user", Content: "C1"},
		{ID: 31, Role: "assistant", Content: "C2"},
		{ID: 32, Role: "user", Content: "C3"},
	}

	// 3. Expectations

	// Init: loadTopicVectors calls GetAllTopics
	mockStore.On("GetAllTopics").Return(topics, nil)
	mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)

	// Background loops expectations
	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{}, nil).Maybe()
	mockStore.On("GetTopicsPendingFacts", userID).Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetTopics", userID).Return([]storage.Topic{}, nil).Maybe()
	mockStore.On("GetMergeCandidates", userID).Return([]storage.MergeCandidate{}, nil).Maybe()
	mockStore.On("GetAllUsers").Return([]storage.User{}, nil).Maybe()

	// Retrieve:
	// 1. EnrichQuery (mock ChatCompletion)
	mockClient.On("CreateChatCompletion", mock.Anything, mock.MatchedBy(func(req openrouter.ChatCompletionRequest) bool {
		// Assume enrichment prompt
		return true // Simplified
	})).Return(openrouter.ChatCompletionResponse{
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
			}{Role: "assistant", Content: "Enriched Query"}},
		},
	}, nil)

	// 2. CreateEmbeddings for "Enriched Query" -> Returns [1, 0, 0]
	mockClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
		return len(req.Input) > 0 && req.Input[0] == "Enriched Query"
	})).Return(openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{
			{Embedding: []float32{1.0, 0.0, 0.0}},
		},
	}, nil)

	// 3. GetTopicsByIDs (called inside Retrieve to fetch matched topics)
	mockStore.On("GetTopicsByIDs", mock.MatchedBy(func(ids []int64) bool {
		// Should request IDs of matching topics (A and C)
		return len(ids) > 0
	})).Return(topics, nil)

	// 4. GetMessagesInRange for matching topics
	// Topic A matches (similarity 1.0 > 0.5)
	mockStore.On("GetMessagesInRange", mock.Anything, userID, int64(10), int64(12)).Return(msgsA, nil)
	// Topic B matches (similarity 0.0 < 0.5) -> Should NOT be called
	// Topic C matches (similarity 0.9 > 0.5)
	mockStore.On("GetMessagesInRange", mock.Anything, userID, int64(30), int64(32)).Return(msgsC, nil)

	// Create dummy translator
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("key: value"), 0644)
	translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

	// 4. Run Logic
	memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
	err := svc.Start(context.Background())
	assert.NoError(t, err)

	results, _, err := svc.Retrieve(context.Background(), userID, "Test Query", nil)
	assert.NoError(t, err)

	// 5. Assertions
	assert.Len(t, results, 2, "Should return 2 topics (A and C)")

	// Sort order is by score descending in implementation
	assert.Equal(t, int64(1), results[0].Topic.ID) // Topic A (1.0)
	assert.Len(t, results[0].Messages, 3)

	assert.Equal(t, int64(3), results[1].Topic.ID) // Topic C (0.9)
	assert.Len(t, results[1].Messages, 3)

	mockStore.AssertExpectations(t)
	mockClient.AssertExpectations(t)
}

func TestRetrieveFacts(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.RAG.EmbeddingModel = "test-model"
	cfg.RAG.MinSafetyThreshold = 0.5

	userID := int64(123)

	t.Run("RAG disabled", func(t *testing.T) {
		disabledCfg := &config.Config{}
		disabledCfg.RAG.Enabled = false

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		// Init mocks
		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("key: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, disabledCfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, disabledCfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		facts, err := svc.RetrieveFacts(context.Background(), userID, "query")
		assert.NoError(t, err)
		assert.Nil(t, facts)
	})

	t.Run("success with matching facts", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		// Facts for user
		facts := []storage.Fact{
			{ID: 1, UserID: userID, Content: "User likes coffee", Embedding: []float32{0.9, 0.1, 0.0}},
			{ID: 2, UserID: userID, Content: "User works at Google", Embedding: []float32{0.0, 0.9, 0.1}},
		}

		// Init mocks
		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return(facts, nil)

		// Query embedding
		mockClient.On("CreateEmbeddings", mock.Anything, mock.MatchedBy(func(req openrouter.EmbeddingRequest) bool {
			return len(req.Input) > 0 && req.Input[0] == "coffee query"
		})).Return(openrouter.EmbeddingResponse{
			Data: []openrouter.EmbeddingObject{{Embedding: []float32{1.0, 0.0, 0.0}}},
		}, nil)

		// GetFacts for fetching full fact data
		mockStore.On("GetFacts", userID).Return(facts, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("key: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		result, err := svc.RetrieveFacts(context.Background(), userID, "coffee query")
		assert.NoError(t, err)
		// Fact 1 should match better (0.9 similarity vs 0.0)
		assert.Len(t, result, 1)
		assert.Equal(t, "User likes coffee", result[0].Content)
	})

	t.Run("embedding error", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{}, assert.AnError)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("key: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		_, err := svc.RetrieveFacts(context.Background(), userID, "query")
		assert.Error(t, err)
	})

	t.Run("empty embedding response", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(
			openrouter.EmbeddingResponse{Data: []openrouter.EmbeddingObject{}}, nil,
		)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("key: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		_, err := svc.RetrieveFacts(context.Background(), userID, "query")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no embedding returned")
	})
}

func TestFindSimilarFacts(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true

	userID := int64(123)

	t.Run("no similar facts", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		// Facts with low similarity
		facts := []storage.Fact{
			{ID: 1, UserID: userID, Content: "unrelated", Embedding: []float32{0.0, 0.0, 1.0}},
		}

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return(facts, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("key: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		// Query embedding that doesn't match
		result, err := svc.FindSimilarFacts(context.Background(), userID, []float32{1.0, 0.0, 0.0}, 0.85)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("finds similar facts", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		facts := []storage.Fact{
			{ID: 1, UserID: userID, Content: "similar fact", Embedding: []float32{0.95, 0.05, 0.0}},
			{ID: 2, UserID: userID, Content: "unrelated", Embedding: []float32{0.0, 0.0, 1.0}},
		}

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return(facts, nil)
		mockStore.On("GetFactsByIDs", []int64{1}).Return([]storage.Fact{facts[0]}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("key: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		result, err := svc.FindSimilarFacts(context.Background(), userID, []float32{1.0, 0.0, 0.0}, 0.85)
		assert.NoError(t, err)
		assert.Len(t, result, 1)
		assert.Equal(t, "similar fact", result[0].Content)
	})
}

func TestFindMergeCandidates(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.RAG.ConsolidationSimilarityThreshold = 0.75

	userID := int64(123)

	t.Run("filters out low similarity candidates", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		// Candidate with low similarity
		candidates := []storage.MergeCandidate{
			{
				Topic1: storage.Topic{ID: 1, UserID: userID, Embedding: []float32{1.0, 0.0, 0.0}},
				Topic2: storage.Topic{ID: 2, UserID: userID, Embedding: []float32{0.0, 1.0, 0.0}}, // 0.0 similarity
			},
		}

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetMergeCandidates", userID).Return(candidates, nil)
		mockStore.On("SetTopicConsolidationChecked", int64(1), true).Return(nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("key: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		result, err := svc.findMergeCandidates(userID)
		assert.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("returns high similarity candidates", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		// Candidate with high similarity
		candidates := []storage.MergeCandidate{
			{
				Topic1: storage.Topic{ID: 1, UserID: userID, Embedding: []float32{1.0, 0.0, 0.0}},
				Topic2: storage.Topic{ID: 2, UserID: userID, Embedding: []float32{0.95, 0.05, 0.0}}, // ~0.95 similarity
			},
		}

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetMergeCandidates", userID).Return(candidates, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("key: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		result, err := svc.findMergeCandidates(userID)
		assert.NoError(t, err)
		assert.Len(t, result, 1)
	})

	t.Run("skips candidates without embeddings", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		// Candidate without embeddings
		candidates := []storage.MergeCandidate{
			{
				Topic1: storage.Topic{ID: 1, UserID: userID},
				Topic2: storage.Topic{ID: 2, UserID: userID},
			},
		}

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetMergeCandidates", userID).Return(candidates, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("key: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		result, err := svc.findMergeCandidates(userID)
		assert.NoError(t, err)
		assert.Empty(t, result)
	})
}

func TestVerifyMerge(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.RAG.TopicModel = "test-model"

	t.Run("should merge", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)

		// LLM returns should_merge: true
		mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(openrouter.ChatCompletionResponse{
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
				}{Content: `{"should_merge": true, "new_summary": "Combined summary"}`}},
			},
		}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("rag.topic_consolidation_prompt: test %s %s"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{Summary: "Topic 1"},
			Topic2: storage.Topic{Summary: "Topic 2"},
		}

		shouldMerge, newSummary, _, err := svc.verifyMerge(context.Background(), candidate)
		assert.NoError(t, err)
		assert.True(t, shouldMerge)
		assert.Equal(t, "Combined summary", newSummary)
	})

	t.Run("should not merge", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)

		mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(openrouter.ChatCompletionResponse{
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
				}{Content: `{"should_merge": false}`}},
			},
		}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("rag.topic_consolidation_prompt: test %s %s"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{Summary: "Topic 1"},
			Topic2: storage.Topic{Summary: "Topic 2"},
		}

		shouldMerge, _, _, err := svc.verifyMerge(context.Background(), candidate)
		assert.NoError(t, err)
		assert.False(t, shouldMerge)
	})

	t.Run("LLM error", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(openrouter.ChatCompletionResponse{}, assert.AnError)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("rag.topic_consolidation_prompt: test %s %s"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{Summary: "Topic 1"},
			Topic2: storage.Topic{Summary: "Topic 2"},
		}

		_, _, _, err := svc.verifyMerge(context.Background(), candidate)
		assert.Error(t, err)
	})
}

func TestMergeTopics(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := &config.Config{}
	cfg.RAG.Enabled = true
	cfg.RAG.EmbeddingModel = "test-embedding"

	t.Run("successful merge", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)

		// GetMessagesInRange for building embedding input
		mockStore.On("GetMessagesInRange", mock.Anything, int64(123), int64(1), int64(20)).Return([]storage.Message{
			{ID: 1, Role: "user", Content: "Hello"},
			{ID: 10, Role: "assistant", Content: "Hi there"},
		}, nil)

		// CreateEmbeddings for new topic
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
			Data: []openrouter.EmbeddingObject{
				{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
			},
		}, nil)

		// AddTopic for new merged topic
		mockStore.On("AddTopic", mock.Anything).Return(int64(100), nil)

		// Update fact references
		mockStore.On("UpdateFactTopic", int64(1), int64(100)).Return(nil)
		mockStore.On("UpdateFactTopic", int64(2), int64(100)).Return(nil)

		// Update fact history references
		mockStore.On("UpdateFactHistoryTopic", int64(1), int64(100)).Return(nil)
		mockStore.On("UpdateFactHistoryTopic", int64(2), int64(100)).Return(nil)

		// Delete old topics
		mockStore.On("DeleteTopicCascade", int64(1)).Return(nil)
		mockStore.On("DeleteTopicCascade", int64(2)).Return(nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 10},
			Topic2: storage.Topic{ID: 2, UserID: 123, StartMsgID: 11, EndMsgID: 20},
		}

		_, err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
		assert.NoError(t, err)
		mockStore.AssertExpectations(t)
	})

	t.Run("GetMessagesInRange error", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetMessagesInRange", mock.Anything, int64(123), int64(1), int64(20)).Return([]storage.Message{}, assert.AnError)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 10},
			Topic2: storage.Topic{ID: 2, UserID: 123, StartMsgID: 11, EndMsgID: 20},
		}

		_, err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
		assert.Error(t, err)
	})

	t.Run("CreateEmbeddings error", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetMessagesInRange", mock.Anything, int64(123), int64(1), int64(20)).Return([]storage.Message{
			{ID: 1, Role: "user", Content: "Hello"},
		}, nil)
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{}, assert.AnError)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 10},
			Topic2: storage.Topic{ID: 2, UserID: 123, StartMsgID: 11, EndMsgID: 20},
		}

		_, err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
		assert.Error(t, err)
	})

	t.Run("no embedding returned", func(t *testing.T) {
		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetMessagesInRange", mock.Anything, int64(123), int64(1), int64(20)).Return([]storage.Message{
			{ID: 1, Role: "user", Content: "Hello"},
		}, nil)
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
			Data: []openrouter.EmbeddingObject{},
		}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		candidate := storage.MergeCandidate{
			Topic1: storage.Topic{ID: 1, UserID: 123, StartMsgID: 1, EndMsgID: 10},
			Topic2: storage.Topic{ID: 2, UserID: 123, StartMsgID: 11, EndMsgID: 20},
		}

		_, err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no embedding returned")
	})
}

func TestGetActiveSessions(t *testing.T) {
	parseTime := func(s string) time.Time {
		t, _ := time.Parse(time.RFC3339, s)
		return t
	}

	t.Run("returns sessions for users with unprocessed messages", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123, 456}},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		// User 123 has unprocessed messages
		user123Messages := []storage.Message{
			{ID: 1, UserID: 123, Role: "user", Content: "Hello", CreatedAt: parseTime("2026-01-02T10:00:00Z")},
			{ID: 2, UserID: 123, Role: "assistant", Content: "Hi", CreatedAt: parseTime("2026-01-02T10:01:00Z")},
			{ID: 3, UserID: 123, Role: "user", Content: "How are you?", CreatedAt: parseTime("2026-01-02T10:02:00Z")},
		}
		mockStore.On("GetUnprocessedMessages", int64(123)).Return(user123Messages, nil)

		// User 456 has no unprocessed messages
		mockStore.On("GetUnprocessedMessages", int64(456)).Return([]storage.Message{}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		sessions, err := svc.GetActiveSessions()

		assert.NoError(t, err)
		assert.Len(t, sessions, 1)
		assert.Equal(t, int64(123), sessions[0].UserID)
		assert.Equal(t, 3, sessions[0].MessageCount)
		assert.Equal(t, parseTime("2026-01-02T10:00:00Z"), sessions[0].FirstMessageTime)
		assert.Equal(t, parseTime("2026-01-02T10:02:00Z"), sessions[0].LastMessageTime)
		// "Hello" (5) + "Hi" (2) + "How are you?" (12) = 19 chars
		assert.Equal(t, 19, sessions[0].ContextSize)
	})

	t.Run("returns empty when no users have unprocessed messages", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		sessions, err := svc.GetActiveSessions()

		assert.NoError(t, err)
		assert.Empty(t, sessions)
	})
}

func TestProcessingStats_AddChatUsage(t *testing.T) {
	t.Run("adds tokens and cost to empty stats", func(t *testing.T) {
		stats := &ProcessingStats{}
		cost := 0.01

		stats.AddChatUsage(100, 50, &cost)

		assert.Equal(t, 100, stats.PromptTokens)
		assert.Equal(t, 50, stats.CompletionTokens)
		assert.NotNil(t, stats.TotalCost)
		assert.Equal(t, 0.01, *stats.TotalCost)
	})

	t.Run("accumulates tokens and cost", func(t *testing.T) {
		initialCost := 0.01
		stats := &ProcessingStats{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalCost:        &initialCost,
		}
		additionalCost := 0.02

		stats.AddChatUsage(200, 100, &additionalCost)

		assert.Equal(t, 300, stats.PromptTokens)
		assert.Equal(t, 150, stats.CompletionTokens)
		assert.Equal(t, 0.03, *stats.TotalCost)
	})

	t.Run("handles nil cost", func(t *testing.T) {
		stats := &ProcessingStats{}

		stats.AddChatUsage(100, 50, nil)

		assert.Equal(t, 100, stats.PromptTokens)
		assert.Equal(t, 50, stats.CompletionTokens)
		assert.Nil(t, stats.TotalCost)
	})

	t.Run("adds cost when stats cost is nil", func(t *testing.T) {
		stats := &ProcessingStats{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalCost:        nil,
		}
		cost := 0.05

		stats.AddChatUsage(0, 0, &cost)

		assert.NotNil(t, stats.TotalCost)
		assert.Equal(t, 0.05, *stats.TotalCost)
	})
}

func TestProcessingStats_AddEmbeddingUsage(t *testing.T) {
	t.Run("adds tokens and cost to empty stats", func(t *testing.T) {
		stats := &ProcessingStats{}
		cost := 0.005

		stats.AddEmbeddingUsage(1000, &cost)

		assert.Equal(t, 1000, stats.EmbeddingTokens)
		assert.NotNil(t, stats.TotalCost)
		assert.Equal(t, 0.005, *stats.TotalCost)
	})

	t.Run("accumulates tokens and cost", func(t *testing.T) {
		initialCost := 0.01
		stats := &ProcessingStats{
			EmbeddingTokens: 500,
			TotalCost:       &initialCost,
		}
		additionalCost := 0.005

		stats.AddEmbeddingUsage(1000, &additionalCost)

		assert.Equal(t, 1500, stats.EmbeddingTokens)
		assert.Equal(t, 0.015, *stats.TotalCost)
	})

	t.Run("handles nil cost", func(t *testing.T) {
		stats := &ProcessingStats{}

		stats.AddEmbeddingUsage(1000, nil)

		assert.Equal(t, 1000, stats.EmbeddingTokens)
		assert.Nil(t, stats.TotalCost)
	})

	t.Run("combined chat and embedding usage", func(t *testing.T) {
		stats := &ProcessingStats{}
		chatCost := 0.01
		embCost := 0.005

		stats.AddChatUsage(100, 50, &chatCost)
		stats.AddEmbeddingUsage(1000, &embCost)

		assert.Equal(t, 100, stats.PromptTokens)
		assert.Equal(t, 50, stats.CompletionTokens)
		assert.Equal(t, 1000, stats.EmbeddingTokens)
		assert.Equal(t, 0.015, *stats.TotalCost)
	})
}

func TestCosineSimilarity(t *testing.T) {
	t.Run("returns 0 for different length vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{1, 2}

		result := cosineSimilarity(a, b)

		assert.Equal(t, float32(0), result)
	})

	t.Run("returns 0 when first vector is zero", func(t *testing.T) {
		a := []float32{0, 0, 0}
		b := []float32{1, 2, 3}

		result := cosineSimilarity(a, b)

		assert.Equal(t, float32(0), result)
	})

	t.Run("returns 0 when second vector is zero", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{0, 0, 0}

		result := cosineSimilarity(a, b)

		assert.Equal(t, float32(0), result)
	})

	t.Run("returns 1 for identical vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{1, 2, 3}

		result := cosineSimilarity(a, b)

		assert.InDelta(t, float32(1), result, 0.0001)
	})

	t.Run("returns -1 for opposite vectors", func(t *testing.T) {
		a := []float32{1, 2, 3}
		b := []float32{-1, -2, -3}

		result := cosineSimilarity(a, b)

		assert.InDelta(t, float32(-1), result, 0.0001)
	})

	t.Run("returns correct similarity for orthogonal vectors", func(t *testing.T) {
		a := []float32{1, 0}
		b := []float32{0, 1}

		result := cosineSimilarity(a, b)

		assert.Equal(t, float32(0), result)
	})
}

func TestEnrichQuery(t *testing.T) {
	t.Run("returns original query when model is empty", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{
				Enabled:    true,
				QueryModel: "", // empty model
			},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		result, prompt, tokens, err := svc.enrichQuery(context.Background(), int64(123), "test query", nil)

		assert.NoError(t, err)
		assert.Equal(t, "test query", result)
		assert.Empty(t, prompt)
		assert.Equal(t, 0, tokens)
	})

	t.Run("handles API error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{
				Enabled:    true,
				QueryModel: "test-model",
			},
			Bot: config.BotConfig{Language: "en"},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
			openrouter.ChatCompletionResponse{}, assert.AnError,
		)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("rag.enrichment_prompt: 'History: %s\\nQuery: %s'"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		_, _, _, err := svc.enrichQuery(context.Background(), int64(123), "test query", nil)

		assert.Error(t, err)
	})

	t.Run("handles empty response", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{
				Enabled:    true,
				QueryModel: "test-model",
			},
			Bot: config.BotConfig{Language: "en"},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
			openrouter.ChatCompletionResponse{
				Choices: []struct {
					Message struct {
						Role             string                `json:"role"`
						Content          string                `json:"content"`
						ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
						ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
					} `json:"message"`
				}{},
			}, nil,
		)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("rag.enrichment_prompt: 'History: %s\\nQuery: %s'"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		_, _, _, err := svc.enrichQuery(context.Background(), int64(123), "test query", nil)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "empty response")
	})

	t.Run("enriches query with history", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{
				Enabled:    true,
				QueryModel: "test-model",
			},
			Bot: config.BotConfig{Language: "en"},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
			openrouter.ChatCompletionResponse{
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
					}{
						Role:    "assistant",
						Content: "enriched search terms",
					}},
				},
				Usage: struct {
					PromptTokens     int      `json:"prompt_tokens"`
					CompletionTokens int      `json:"completion_tokens"`
					TotalTokens      int      `json:"total_tokens"`
					Cost             *float64 `json:"cost,omitempty"`
				}{PromptTokens: 10, CompletionTokens: 5},
			}, nil,
		)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("rag.enrichment_prompt: 'History: %s\\nQuery: %s'"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		history := []storage.Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there"},
		}

		result, _, tokens, err := svc.enrichQuery(context.Background(), int64(123), "test query", history)

		assert.NoError(t, err)
		assert.Equal(t, "enriched search terms", result)
		assert.Equal(t, 15, tokens)
	})

	t.Run("truncates long history messages", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{
				Enabled:    true,
				QueryModel: "test-model",
			},
			Bot: config.BotConfig{Language: "en"},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		var capturedReq openrouter.ChatCompletionRequest
		mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			capturedReq = args.Get(1).(openrouter.ChatCompletionRequest)
		}).Return(
			openrouter.ChatCompletionResponse{
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
					}{
						Content: "enriched",
					}},
				},
			}, nil,
		)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("rag.enrichment_prompt: 'History: %s\\nQuery: %s'"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		// Create a very long message (>500 chars)
		longContent := strings.Repeat("a", 600)
		history := []storage.Message{
			{Role: "user", Content: longContent},
		}

		_, _, _, err := svc.enrichQuery(context.Background(), int64(123), "test query", history)

		assert.NoError(t, err)
		// The prompt should contain truncated content with "..."
		prompt := capturedReq.Messages[0].Content.(string)
		assert.Contains(t, prompt, "...")
	})
}

func TestLoadNewVectors(t *testing.T) {
	t.Run("loads new topics and facts", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		embedding := []float32{0.1, 0.2, 0.3}
		mockStore.On("GetTopicsAfterID", int64(0)).Return([]storage.Topic{
			{ID: 1, UserID: 123, Summary: "Topic 1", Embedding: embedding},
			{ID: 2, UserID: 123, Summary: "Topic 2", Embedding: embedding},
		}, nil)
		mockStore.On("GetFactsAfterID", int64(0)).Return([]storage.Fact{
			{ID: 1, UserID: 123, Content: "Fact 1", Embedding: embedding},
		}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		err := svc.LoadNewVectors()

		assert.NoError(t, err)
		mockStore.AssertExpectations(t)
	})

	t.Run("returns early when no new data", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetTopicsAfterID", int64(0)).Return([]storage.Topic{}, nil)
		mockStore.On("GetFactsAfterID", int64(0)).Return([]storage.Fact{}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		err := svc.LoadNewVectors()

		assert.NoError(t, err)
		mockStore.AssertExpectations(t)
	})

	t.Run("handles topic load error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetTopicsAfterID", int64(0)).Return([]storage.Topic{}, assert.AnError)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		err := svc.LoadNewVectors()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "load new topics")
	})

	t.Run("handles fact load error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetTopicsAfterID", int64(0)).Return([]storage.Topic{}, nil)
		mockStore.On("GetFactsAfterID", int64(0)).Return([]storage.Fact{}, assert.AnError)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		err := svc.LoadNewVectors()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "load new facts")
	})

	t.Run("skips topics without embeddings", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		embedding := []float32{0.1, 0.2, 0.3}
		mockStore.On("GetTopicsAfterID", int64(0)).Return([]storage.Topic{
			{ID: 1, UserID: 123, Summary: "Topic with embedding", Embedding: embedding},
			{ID: 2, UserID: 123, Summary: "Topic without embedding", Embedding: nil},
		}, nil)
		mockStore.On("GetFactsAfterID", int64(0)).Return([]storage.Fact{}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		err := svc.LoadNewVectors()

		assert.NoError(t, err)
		mockStore.AssertExpectations(t)
	})
}

func TestProcessConsolidation(t *testing.T) {
	t.Run("handles context cancellation", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		// Create cancelled context
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// Should exit immediately due to cancelled context
		svc.processConsolidation(ctx)

		// No mock calls expected since context was cancelled
	})

	t.Run("handles findMergeCandidates error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		// Return error for GetMergeCandidates
		mockStore.On("GetMergeCandidates", int64(123)).Return([]storage.MergeCandidate{}, assert.AnError)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processConsolidation(context.Background())

		mockStore.AssertExpectations(t)
	})

	t.Run("skips when no merge candidates", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		// Return empty merge candidates
		mockStore.On("GetMergeCandidates", int64(123)).Return([]storage.MergeCandidate{}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processConsolidation(context.Background())

		mockStore.AssertExpectations(t)
	})
}

func TestRetrieve_SkipEnrichment(t *testing.T) {
	t.Run("skips enrichment when option is set", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{
				Enabled:        true,
				EmbeddingModel: "test-model",
				QueryModel:     "query-model", // Would normally trigger enrichment
			},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetTopicsByIDs", mock.Anything).Return([]storage.Topic{}, nil)

		// Embedding call for the query
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(
			openrouter.EmbeddingResponse{
				Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2, 0.3}}},
			}, nil,
		)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		opts := &RetrievalOptions{SkipEnrichment: true}
		results, debugInfo, err := svc.Retrieve(context.Background(), 123, "test query", opts)

		assert.NoError(t, err)
		assert.Empty(t, results)                               // No topics loaded
		assert.Equal(t, "test query", debugInfo.EnrichedQuery) // Query should not be enriched
		assert.Empty(t, debugInfo.EnrichmentPrompt)            // No enrichment prompt used
	})
}

func TestProcessChunk(t *testing.T) {
	t.Run("returns nil for empty chunk", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		err := svc.processChunk(context.Background(), 123, []storage.Message{})

		assert.NoError(t, err)
	})
}

func TestRetrieve_NilOptions(t *testing.T) {
	t.Run("handles nil options", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{
				Enabled:        true,
				EmbeddingModel: "test-model",
			},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetAllTopics").Return([]storage.Topic{}, nil)
		mockStore.On("GetAllFacts").Return([]storage.Fact{}, nil)
		mockStore.On("GetTopicsByIDs", mock.Anything).Return([]storage.Topic{}, nil)

		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(
			openrouter.EmbeddingResponse{
				Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2, 0.3}}},
			}, nil,
		)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)
		_ = svc.Start(context.Background())

		// Call with nil options - should not panic
		_, _, err := svc.Retrieve(context.Background(), 123, "test query", nil)

		assert.NoError(t, err)
	})
}

func TestForceProcessUserWithProgress(t *testing.T) {
	t.Run("returns empty stats when no unprocessed messages", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		var events []ProgressEvent
		callback := func(e ProgressEvent) {
			events = append(events, e)
		}

		stats, err := svc.ForceProcessUserWithProgress(context.Background(), 123, callback)

		assert.NoError(t, err)
		assert.NotNil(t, stats)
		assert.Equal(t, 0, stats.MessagesProcessed)
		assert.Len(t, events, 1)
		assert.Equal(t, "complete", events[0].Stage)
		assert.True(t, events[0].Complete)
	})

	t.Run("handles GetUnprocessedMessages error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{}, assert.AnError)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		callback := func(e ProgressEvent) {}

		_, err := svc.ForceProcessUserWithProgress(context.Background(), 123, callback)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch unprocessed messages")
	})
}

func TestForceProcessUser(t *testing.T) {
	t.Run("returns 0 when no unprocessed messages", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		count, err := svc.ForceProcessUser(context.Background(), 123)

		assert.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("handles GetUnprocessedMessages error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{}, assert.AnError)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		_, err := svc.ForceProcessUser(context.Background(), 123)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to fetch unprocessed messages")
	})
}

func TestProcessAllUsers(t *testing.T) {
	t.Run("handles context cancellation", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123, 456}},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		svc.processAllUsers(ctx)
		// Should return immediately without processing any users
	})

	t.Run("processes all users", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		// Mock GetUnprocessedMessages returning empty (no work to do)
		mockStore.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processAllUsers(context.Background())

		mockStore.AssertExpectations(t)
	})
}

func TestProcessTopicChunking(t *testing.T) {
	t.Run("handles GetUnprocessedMessages error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{}, assert.AnError)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processTopicChunking(context.Background(), 123)

		mockStore.AssertExpectations(t)
	})

	t.Run("returns early when no unprocessed messages", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processTopicChunking(context.Background(), 123)

		mockStore.AssertExpectations(t)
	})

	t.Run("uses default chunk interval on invalid config", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{
				Enabled:       true,
				ChunkInterval: "invalid-duration",
			},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetUnprocessedMessages", int64(123)).Return([]storage.Message{}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		// Should not panic with invalid duration
		svc.processTopicChunking(context.Background(), 123)

		mockStore.AssertExpectations(t)
	})
}

func TestProcessFactExtraction(t *testing.T) {
	t.Run("handles context cancellation", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		// Create cancelled context
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// Should exit immediately due to cancelled context
		svc.processFactExtraction(ctx)

		// No mock calls expected since context was cancelled
	})

	t.Run("handles GetTopicsPendingFacts error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetTopicsPendingFacts", int64(123)).Return([]storage.Topic{}, assert.AnError)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processFactExtraction(context.Background())

		mockStore.AssertExpectations(t)
	})

	t.Run("skips topics not checked for consolidation", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		// Return topic that hasn't been checked for consolidation
		mockStore.On("GetTopicsPendingFacts", int64(123)).Return([]storage.Topic{
			{ID: 1, UserID: 123, ConsolidationChecked: false},
		}, nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processFactExtraction(context.Background())

		mockStore.AssertExpectations(t)
		// Verify no further processing happened (no GetMessagesInRange call)
	})

	t.Run("handles empty messages for topic", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
			Bot: config.BotConfig{AllowedUserIDs: []int64{123}},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetTopicsPendingFacts", int64(123)).Return([]storage.Topic{
			{ID: 1, UserID: 123, ConsolidationChecked: true, StartMsgID: 1, EndMsgID: 10},
		}, nil)
		mockStore.On("GetMessagesInRange", mock.Anything, int64(123), int64(1), int64(10)).Return([]storage.Message{}, nil)
		mockStore.On("SetTopicFactsExtracted", int64(1), true).Return(nil)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		svc.processFactExtraction(context.Background())

		mockStore.AssertExpectations(t)
	})
}

func TestProcessChunkWithStats(t *testing.T) {
	t.Run("empty chunk returns nil", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		stats := &ProcessingStats{}
		topicIDs, err := svc.processChunkWithStats(context.Background(), 123, []storage.Message{}, stats)

		assert.NoError(t, err)
		assert.Nil(t, topicIDs)
	})

	t.Run("extractTopics error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true, EmbeddingModel: "test-embed"},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		// Mock extractTopics to fail via CreateChatCompletion error
		mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(openrouter.ChatCompletionResponse{}, assert.AnError)

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		chunk := []storage.Message{
			{ID: 1, Role: "user", Content: "Hello", CreatedAt: time.Now()},
		}
		stats := &ProcessingStats{}
		_, err := svc.processChunkWithStats(context.Background(), 123, chunk, stats)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "extract topics")
	})

	t.Run("successful processing with topics", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true, EmbeddingModel: "test-embed"},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		now := time.Now()
		chunk := []storage.Message{
			{ID: 1, Role: "user", Content: "Hello", CreatedAt: now},
			{ID: 2, Role: "assistant", Content: "Hi there", CreatedAt: now},
		}

		// Mock extractTopics response
		mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(openrouter.ChatCompletionResponse{
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
				}{
					Role:    "assistant",
					Content: `{"topics":[{"summary": "Test topic", "start_msg_id": 1, "end_msg_id": 2}]}`,
				}},
			},
			Usage: struct {
				PromptTokens     int      `json:"prompt_tokens"`
				CompletionTokens int      `json:"completion_tokens"`
				TotalTokens      int      `json:"total_tokens"`
				Cost             *float64 `json:"cost,omitempty"`
			}{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}, nil).Once()

		// Mock embeddings
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{
			Data: []openrouter.EmbeddingObject{
				{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
			},
			Usage: struct {
				PromptTokens int      `json:"prompt_tokens"`
				TotalTokens  int      `json:"total_tokens"`
				Cost         *float64 `json:"cost,omitempty"`
			}{TotalTokens: 10},
		}, nil)

		// Mock AddTopic
		mockStore.On("AddTopic", mock.Anything).Return(int64(1), nil)

		// Mock for LoadNewVectors
		mockStore.On("GetTopicsAfterID", mock.Anything).Return([]storage.Topic{}, nil).Maybe()
		mockStore.On("GetFactsAfterID", mock.Anything).Return([]storage.Fact{}, nil).Maybe()

		// Mock RAG logging
		mockStore.On("AddRAGLog", mock.Anything).Return(nil).Maybe()

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		stats := &ProcessingStats{}
		topicIDs, err := svc.processChunkWithStats(context.Background(), 123, chunk, stats)

		assert.NoError(t, err)
		assert.Len(t, topicIDs, 1)
		assert.Greater(t, stats.PromptTokens, 0)
		mockClient.AssertExpectations(t)
		mockStore.AssertExpectations(t)
	})

	t.Run("embedding error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true, EmbeddingModel: "test-embed"},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		now := time.Now()
		chunk := []storage.Message{
			{ID: 1, Role: "user", Content: "Hello", CreatedAt: now},
		}

		// Mock extractTopics response
		mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(openrouter.ChatCompletionResponse{
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
				}{
					Role:    "assistant",
					Content: `{"topics":[{"summary": "Test topic", "start_msg_id": 1, "end_msg_id": 1}]}`,
				}},
			},
			Usage: struct {
				PromptTokens     int      `json:"prompt_tokens"`
				CompletionTokens int      `json:"completion_tokens"`
				TotalTokens      int      `json:"total_tokens"`
				Cost             *float64 `json:"cost,omitempty"`
			}{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}, nil).Once()

		// Mock embeddings error
		mockClient.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(openrouter.EmbeddingResponse{}, assert.AnError)

		// Mock RAG logging
		mockStore.On("AddRAGLog", mock.Anything).Return(nil).Maybe()

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		stats := &ProcessingStats{}
		_, err := svc.processChunkWithStats(context.Background(), 123, chunk, stats)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "create embeddings")
	})
}

func TestRunConsolidationSync(t *testing.T) {
	t.Run("no merge candidates", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true, SimilarityThreshold: 0.85},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		// No candidates
		mockStore.On("GetMergeCandidates", int64(123), mock.Anything).Return([]storage.MergeCandidate{}, nil)
		mockStore.On("SetTopicConsolidationChecked", mock.Anything, true).Return(nil).Maybe()

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		stats := &ProcessingStats{}
		count := svc.runConsolidationSync(context.Background(), 123, []int64{1, 2}, stats)

		assert.Equal(t, 0, count)
		mockStore.AssertExpectations(t)
	})

	t.Run("findMergeCandidates error", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true, SimilarityThreshold: 0.85},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		mockStore.On("GetMergeCandidates", int64(123), mock.Anything).Return([]storage.MergeCandidate{}, assert.AnError)
		mockStore.On("SetTopicConsolidationChecked", mock.Anything, true).Return(nil).Maybe()

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		stats := &ProcessingStats{}
		count := svc.runConsolidationSync(context.Background(), 123, []int64{1}, stats)

		assert.Equal(t, 0, count)
	})

	t.Run("context cancellation during verification", func(t *testing.T) {
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
		cfg := &config.Config{
			RAG: config.RAGConfig{Enabled: true, SimilarityThreshold: 0.85},
		}

		mockStore := new(MockStorage)
		mockClient := new(MockClient)

		topic1 := storage.Topic{ID: 1, UserID: 123}
		topic2 := storage.Topic{ID: 2, UserID: 123}
		candidates := []storage.MergeCandidate{{Topic1: topic1, Topic2: topic2}}

		mockStore.On("GetMergeCandidates", int64(123), mock.Anything).Return(candidates, nil)
		mockStore.On("SetTopicConsolidationChecked", mock.Anything, true).Return(nil).Maybe()

		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte("test: value"), 0644)
		translator, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")

		memSvc := memory.NewService(logger, cfg, mockStore, mockStore, mockStore, mockClient, translator)
		svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockStore, mockStore, mockClient, memSvc, translator)

		// Cancel context immediately
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		stats := &ProcessingStats{}
		count := svc.runConsolidationSync(ctx, 123, []int64{1}, stats)

		assert.Equal(t, 0, count)
	})
}

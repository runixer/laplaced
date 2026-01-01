package rag

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

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

		shouldMerge, newSummary, err := svc.verifyMerge(context.Background(), candidate)
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

		shouldMerge, _, err := svc.verifyMerge(context.Background(), candidate)
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

		_, _, err := svc.verifyMerge(context.Background(), candidate)
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

		err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
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

		err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
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

		err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
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

		err := svc.mergeTopics(context.Background(), candidate, "Merged summary")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no embedding returned")
	})
}

package memory

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockStorage
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
func (m *MockStorage) GetTopics(userID int64) ([]storage.Topic, error) {
	args := m.Called(userID)
	return args.Get(0).([]storage.Topic), args.Error(1)
}
func (m *MockStorage) GetMessagesInRange(ctx context.Context, userID int64, startID, endID int64) ([]storage.Message, error) {
	args := m.Called(ctx, userID, startID, endID)
	return args.Get(0).([]storage.Message), args.Error(1)
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
func (m *MockStorage) GetTopicsPendingFacts(userID int64) ([]storage.Topic, error) {
	args := m.Called(userID)
	return args.Get(0).([]storage.Topic), args.Error(1)
}
func (m *MockStorage) GetTopicsExtended(filter storage.TopicFilter, limit, offset int, sortBy, sortDir string) (storage.TopicResult, error) {
	args := m.Called(filter, limit, offset, sortBy, sortDir)
	return args.Get(0).(storage.TopicResult), args.Error(1)
}
func (m *MockStorage) GetMemoryBank(userID int64) (string, error) {
	args := m.Called(userID)
	return args.String(0), args.Error(1)
}
func (m *MockStorage) UpdateMemoryBank(userID int64, content string) error {
	args := m.Called(userID, content)
	return args.Error(0)
}
func (m *MockStorage) AddFact(fact storage.Fact) (int64, error) {
	args := m.Called(fact)
	return args.Get(0).(int64), args.Error(1)
}
func (m *MockStorage) GetFacts(userID int64) ([]storage.Fact, error) {
	args := m.Called(userID)
	return args.Get(0).([]storage.Fact), args.Error(1)
}
func (m *MockStorage) GetFactsByIDs(ids []int64) ([]storage.Fact, error) {
	args := m.Called(ids)
	return args.Get(0).([]storage.Fact), args.Error(1)
}
func (m *MockStorage) GetAllFacts() ([]storage.Fact, error) {
	args := m.Called()
	return args.Get(0).([]storage.Fact), args.Error(1)
}
func (m *MockStorage) GetFactStats() (storage.FactStats, error) {
	args := m.Called()
	return args.Get(0).(storage.FactStats), args.Error(1)
}
func (m *MockStorage) UpdateFact(fact storage.Fact) error {
	args := m.Called(fact)
	return args.Error(0)
}
func (m *MockStorage) UpdateFactTopic(oldTopicID, newTopicID int64) error {
	args := m.Called(oldTopicID, newTopicID)
	return args.Error(0)
}
func (m *MockStorage) DeleteFact(userID, id int64) error {
	args := m.Called(userID, id)
	return args.Error(0)
}
func (m *MockStorage) GetUnprocessedMessages(userID int64) ([]storage.Message, error) {
	args := m.Called(userID)
	return args.Get(0).([]storage.Message), args.Error(1)
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
func (m *MockStorage) GetTopicExtractionLogs(limit, offset int) ([]storage.RAGLog, int, error) {
	args := m.Called(limit, offset)
	return args.Get(0).([]storage.RAGLog), args.Int(1), args.Error(2)
}
func (m *MockStorage) ResetUserData(userID int64) error {
	args := m.Called(userID)
	return args.Error(0)
}

// MockOpenRouterClient
type MockOpenRouterClient struct {
	mock.Mock
}

func (m *MockOpenRouterClient) CreateChatCompletion(ctx context.Context, req openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return openrouter.ChatCompletionResponse{}, args.Error(1)
	}
	return *args.Get(0).(*openrouter.ChatCompletionResponse), args.Error(1)
}

func (m *MockOpenRouterClient) CreateEmbeddings(ctx context.Context, req openrouter.EmbeddingRequest) (openrouter.EmbeddingResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return openrouter.EmbeddingResponse{}, args.Error(1)
	}
	return *args.Get(0).(*openrouter.EmbeddingResponse), args.Error(1)
}

func TestAddFactWithHistory(t *testing.T) {
	tests := []struct {
		name        string
		debugMode   bool
		addFactErr  error
		wantHistory bool
		wantErr     bool
	}{
		{
			name:        "success with debug mode",
			debugMode:   true,
			addFactErr:  nil,
			wantHistory: true,
			wantErr:     false,
		},
		{
			name:        "success without debug mode",
			debugMode:   false,
			addFactErr:  nil,
			wantHistory: false,
			wantErr:     false,
		},
		{
			name:        "AddFact error",
			debugMode:   true,
			addFactErr:  assert.AnError,
			wantHistory: false,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore := new(MockStorage)
			logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
			cfg := &config.Config{}
			cfg.Server.DebugMode = tt.debugMode
			translator, _ := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")

			svc := NewService(logger, cfg, mockStore, mockStore, mockStore, nil, translator)

			fact := storage.Fact{
				UserID:     123,
				Entity:     "User",
				Content:    "Test fact",
				Category:   "test",
				Relation:   "related_to",
				Importance: 50,
			}
			topicID := int64(10)

			mockStore.On("AddFact", mock.Anything).Return(int64(1), tt.addFactErr)
			if tt.wantHistory {
				mockStore.On("AddFactHistory", mock.MatchedBy(func(h storage.FactHistory) bool {
					return h.FactID == 1 && h.Action == "add" && h.TopicID != nil && *h.TopicID == topicID
				})).Return(nil)
			}

			id, err := svc.addFactWithHistory(fact, "test reason", &topicID, "input")

			if tt.wantErr {
				assert.Error(t, err)
				assert.Equal(t, int64(0), id)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, int64(1), id)
			}
			mockStore.AssertExpectations(t)
		})
	}
}

func TestProcessSession_AddFact_RecordsHistory(t *testing.T) {
	// Arrange
	mockStore := new(MockStorage)
	mockOR := new(MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Server.DebugMode = true
	translator, err := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")
	if err != nil {
		t.Fatalf("failed to create translator: %v", err)
	}

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)

	userID := int64(123)
	messages := []storage.Message{{Role: "user", Content: "My name is John"}}
	refDate := time.Now()

	// Mock GetFacts
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)

	// Mock GetAllUsers
	mockStore.On("GetAllUsers").Return([]storage.User{}, nil)

	// Mock LLM Response for extractMemoryUpdate
	update := MemoryUpdate{
		Added: []struct {
			Entity     string `json:"entity"`
			Relation   string `json:"relation"`
			Content    string `json:"content"`
			Category   string `json:"category"`
			Type       string `json:"type"`
			Importance int    `json:"importance"`
			Reason     string `json:"reason"`
		}{
			{
				Entity:     "User",
				Relation:   "name",
				Content:    "Name is John",
				Category:   "bio",
				Type:       "identity",
				Importance: 100,
				Reason:     "User stated name",
			},
		},
	}
	updateJSON, _ := json.Marshal(update)

	// Construct complex response structure
	resp := openrouter.ChatCompletionResponse{}
	// We can't easily construct the anonymous struct slice literal without defining the type.
	// But we can use JSON unmarshal to create it!
	respJSON := map[string]interface{}{
		"choices": []map[string]interface{}{
			{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": string(updateJSON),
				},
			},
		},
	}
	respBytes, _ := json.Marshal(respJSON)
	_ = json.Unmarshal(respBytes, &resp)

	mockOR.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(&resp, nil)

	// Mock Embedding
	embResp := openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2}}},
	}
	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(&embResp, nil)

	// Mock AddFact
	mockStore.On("AddFact", mock.Anything).Return(int64(1), nil)

	// Mock AddFactHistory - THIS IS WHAT WE WANT TO VERIFY
	mockStore.On("AddFactHistory", mock.MatchedBy(func(h storage.FactHistory) bool {
		return h.FactID == 1 && h.Action == "add" && h.Reason == "User stated name" && h.Relation == "name"
	})).Return(nil)

	// Act
	err = svc.ProcessSession(context.Background(), userID, messages, refDate, 0)

	// Assert
	assert.NoError(t, err)
	mockStore.AssertExpectations(t)
}

func TestProcessSession_UpdateFact_RecordsHistory(t *testing.T) {
	// Arrange
	mockStore := new(MockStorage)
	mockOR := new(MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Server.DebugMode = true
	translator, err := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")
	if err != nil {
		t.Fatalf("failed to create translator: %v", err)
	}

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)

	userID := int64(123)
	messages := []storage.Message{{Role: "user", Content: "My name is actually Bob"}}
	refDate := time.Now()

	existingFact := storage.Fact{
		ID:      1,
		UserID:  userID,
		Content: "Name is John",
		Type:    "identity",
	}

	// Mock GetFacts
	mockStore.On("GetFacts", userID).Return([]storage.Fact{existingFact}, nil)

	// Mock GetAllUsers
	mockStore.On("GetAllUsers").Return([]storage.User{}, nil)

	// Mock LLM Response
	update := MemoryUpdate{
		Updated: []struct {
			ID         int64  `json:"id"`
			Content    string `json:"content"`
			Type       string `json:"type,omitempty"`
			Importance int    `json:"importance"`
			Reason     string `json:"reason"`
		}{
			{
				ID:         1,
				Content:    "Name is Bob",
				Importance: 100,
				Reason:     "Correction",
			},
		},
	}
	updateJSON, _ := json.Marshal(update)

	resp := openrouter.ChatCompletionResponse{}
	respJSON := map[string]interface{}{
		"choices": []map[string]interface{}{
			{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": string(updateJSON),
				},
			},
		},
	}
	respBytes, _ := json.Marshal(respJSON)
	_ = json.Unmarshal(respBytes, &resp)

	mockOR.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(&resp, nil)

	// Mock Embedding
	embResp := openrouter.EmbeddingResponse{
		Data: []openrouter.EmbeddingObject{{Embedding: []float32{0.1, 0.2}}},
	}
	mockOR.On("CreateEmbeddings", mock.Anything, mock.Anything).Return(&embResp, nil)

	// Mock UpdateFact
	mockStore.On("UpdateFact", mock.Anything).Return(nil)

	// Mock AddFactHistory
	mockStore.On("AddFactHistory", mock.MatchedBy(func(h storage.FactHistory) bool {
		return h.FactID == 1 && h.Action == "update" && h.OldContent == "Name is John" && h.NewContent == "Name is Bob" && h.Reason == "Correction"
	})).Return(nil)

	// Act
	err = svc.ProcessSession(context.Background(), userID, messages, refDate, 0)

	// Assert
	assert.NoError(t, err)
	mockStore.AssertExpectations(t)
}

func TestProcessSession_RemoveFact_RecordsHistory(t *testing.T) {
	// Arrange
	mockStore := new(MockStorage)
	mockOR := new(MockOpenRouterClient)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.Server.DebugMode = true
	translator, err := i18n.NewTranslatorFromFS(os.DirFS("testdata/locales"), "en")
	if err != nil {
		t.Fatalf("failed to create translator: %v", err)
	}

	svc := NewService(logger, cfg, mockStore, mockStore, mockStore, mockOR, translator)

	userID := int64(123)
	messages := []storage.Message{{Role: "user", Content: "Forget my name"}}
	refDate := time.Now()

	existingFact := storage.Fact{
		ID:      1,
		UserID:  userID,
		Content: "Name is John",
	}

	// Mock GetFacts
	mockStore.On("GetFacts", userID).Return([]storage.Fact{existingFact}, nil)

	// Mock GetAllUsers
	mockStore.On("GetAllUsers").Return([]storage.User{}, nil)

	// Mock LLM Response
	update := MemoryUpdate{
		Removed: []struct {
			ID     int64  `json:"id"`
			Reason string `json:"reason"`
		}{
			{
				ID:     1,
				Reason: "User request",
			},
		},
	}
	updateJSON, _ := json.Marshal(update)

	resp := openrouter.ChatCompletionResponse{}
	respJSON := map[string]interface{}{
		"choices": []map[string]interface{}{
			{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": string(updateJSON),
				},
			},
		},
	}
	respBytes, _ := json.Marshal(respJSON)
	_ = json.Unmarshal(respBytes, &resp)

	mockOR.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(&resp, nil)

	// Mock DeleteFact
	mockStore.On("DeleteFact", userID, int64(1)).Return(nil)

	// Mock AddFactHistory
	mockStore.On("AddFactHistory", mock.MatchedBy(func(h storage.FactHistory) bool {
		return h.FactID == 1 && h.Action == "delete" && h.OldContent == "Name is John" && h.Reason == "User request"
	})).Return(nil)

	// Act
	err = svc.ProcessSession(context.Background(), userID, messages, refDate, 0)

	// Assert
	assert.NoError(t, err)
	mockStore.AssertExpectations(t)
}

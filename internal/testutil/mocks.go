// Package testutil provides centralized test mocks, fixtures, and helpers.
// All test files should import mocks from here instead of defining their own.
package testutil

import (
	"context"

	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/stretchr/testify/mock"
)

// MockBotAPI implements telegram.BotAPI for tests.
type MockBotAPI struct {
	mock.Mock
}

func (m *MockBotAPI) SendMessage(ctx context.Context, req telegram.SendMessageRequest) (*telegram.Message, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*telegram.Message), args.Error(1)
}

func (m *MockBotAPI) SetMyCommands(ctx context.Context, req telegram.SetMyCommandsRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockBotAPI) SetWebhook(ctx context.Context, req telegram.SetWebhookRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockBotAPI) SendChatAction(ctx context.Context, req telegram.SendChatActionRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockBotAPI) GetFile(ctx context.Context, req telegram.GetFileRequest) (*telegram.File, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*telegram.File), args.Error(1)
}

func (m *MockBotAPI) SetMessageReaction(ctx context.Context, req telegram.SetMessageReactionRequest) error {
	args := m.Called(ctx, req)
	return args.Error(0)
}

func (m *MockBotAPI) GetToken() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockBotAPI) GetUpdates(ctx context.Context, req telegram.GetUpdatesRequest) ([]telegram.Update, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]telegram.Update), args.Error(1)
}

// MockStorage implements all storage repository interfaces for tests.
// This is a composite mock that covers MessageRepository, UserRepository,
// TopicRepository, FactRepository, FactHistoryRepository, StatsRepository,
// MemoryBankRepository, MaintenanceRepository, and AgentLogRepository.
type MockStorage struct {
	mock.Mock
}

// MessageRepository methods

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
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) GetMessagesByIDs(userID int64, ids []int64) ([]storage.Message, error) {
	args := m.Called(userID, ids)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) ClearHistory(userID int64) error {
	args := m.Called(userID)
	return args.Error(0)
}

func (m *MockStorage) GetMessagesInRange(ctx context.Context, userID int64, startID, endID int64) ([]storage.Message, error) {
	args := m.Called(ctx, userID, startID, endID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) GetMessagesByTopicID(ctx context.Context, topicID int64) ([]storage.Message, error) {
	args := m.Called(ctx, topicID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) UpdateMessageTopic(userID int64, messageID, topicID int64) error {
	args := m.Called(userID, messageID, topicID)
	return args.Error(0)
}

func (m *MockStorage) UpdateMessagesTopicInRange(ctx context.Context, userID, startMsgID, endMsgID, topicID int64) error {
	args := m.Called(ctx, userID, startMsgID, endMsgID, topicID)
	return args.Error(0)
}

func (m *MockStorage) GetUnprocessedMessages(userID int64) ([]storage.Message, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return []storage.Message{}, args.Error(1)
	}
	return args.Get(0).([]storage.Message), args.Error(1)
}

// UserRepository methods

func (m *MockStorage) UpsertUser(user storage.User) error {
	args := m.Called(user)
	return args.Error(0)
}

func (m *MockStorage) GetAllUsers() ([]storage.User, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.User), args.Error(1)
}

func (m *MockStorage) ResetUserData(userID int64) error {
	args := m.Called(userID)
	return args.Error(0)
}

// TopicRepository methods

func (m *MockStorage) AddTopic(topic storage.Topic) (int64, error) {
	args := m.Called(topic)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) AddTopicWithoutMessageUpdate(topic storage.Topic) (int64, error) {
	args := m.Called(topic)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) CreateTopic(topic storage.Topic) (int64, error) {
	args := m.Called(topic)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) DeleteTopic(userID int64, id int64) error {
	args := m.Called(userID, id)
	return args.Error(0)
}

func (m *MockStorage) DeleteTopicCascade(userID int64, id int64) error {
	args := m.Called(userID, id)
	return args.Error(0)
}

func (m *MockStorage) GetLastTopicEndMessageID(userID int64) (int64, error) {
	args := m.Called(userID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) GetAllTopics() ([]storage.Topic, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopicsAfterID(minID int64) ([]storage.Topic, error) {
	args := m.Called(minID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopicsByIDs(userID int64, ids []int64) ([]storage.Topic, error) {
	args := m.Called(userID, ids)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopics(userID int64) ([]storage.Topic, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) SetTopicFactsExtracted(userID int64, topicID int64, extracted bool) error {
	args := m.Called(userID, topicID, extracted)
	return args.Error(0)
}

func (m *MockStorage) SetTopicConsolidationChecked(userID int64, topicID int64, checked bool) error {
	args := m.Called(userID, topicID, checked)
	return args.Error(0)
}

func (m *MockStorage) GetTopicsPendingFacts(userID int64) ([]storage.Topic, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopicsExtended(filter storage.TopicFilter, limit, offset int, sortBy, sortDir string) (storage.TopicResult, error) {
	args := m.Called(filter, limit, offset, sortBy, sortDir)
	return args.Get(0).(storage.TopicResult), args.Error(1)
}

func (m *MockStorage) GetMergeCandidates(userID int64) ([]storage.MergeCandidate, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.MergeCandidate), args.Error(1)
}

// FactRepository methods

func (m *MockStorage) AddFact(fact storage.Fact) (int64, error) {
	args := m.Called(fact)
	if args.Get(0) == nil {
		return 0, args.Error(1)
	}
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) GetFacts(userID int64) ([]storage.Fact, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Fact), args.Error(1)
}

func (m *MockStorage) GetFactsByIDs(userID int64, ids []int64) ([]storage.Fact, error) {
	args := m.Called(userID, ids)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Fact), args.Error(1)
}

func (m *MockStorage) GetFactsByTopicID(userID int64, topicID int64) ([]storage.Fact, error) {
	args := m.Called(userID, topicID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Fact), args.Error(1)
}

func (m *MockStorage) GetAllFacts() ([]storage.Fact, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Fact), args.Error(1)
}

func (m *MockStorage) GetFactsAfterID(minID int64) ([]storage.Fact, error) {
	args := m.Called(minID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
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

func (m *MockStorage) UpdateFact(fact storage.Fact) error {
	args := m.Called(fact)
	return args.Error(0)
}

func (m *MockStorage) UpdateFactsTopic(userID int64, oldTopicID, newTopicID int64) error {
	args := m.Called(userID, oldTopicID, newTopicID)
	return args.Error(0)
}

func (m *MockStorage) DeleteFact(userID, id int64) error {
	args := m.Called(userID, id)
	return args.Error(0)
}

// FactHistoryRepository methods

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
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.FactHistory), args.Error(1)
}

func (m *MockStorage) GetFactHistoryExtended(filter storage.FactHistoryFilter, limit, offset int, sortBy, sortDir string) (storage.FactHistoryResult, error) {
	args := m.Called(filter, limit, offset, sortBy, sortDir)
	return args.Get(0).(storage.FactHistoryResult), args.Error(1)
}

// StatsRepository methods

func (m *MockStorage) AddStat(stat storage.Stat) error {
	args := m.Called(stat)
	return args.Error(0)
}

func (m *MockStorage) GetStats() (map[int64]storage.Stat, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[int64]storage.Stat), args.Error(1)
}

func (m *MockStorage) GetDashboardStats(userID int64) (*storage.DashboardStats, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.DashboardStats), args.Error(1)
}

// MemoryBankRepository methods

func (m *MockStorage) GetMemoryBank(userID int64) (string, error) {
	args := m.Called(userID)
	return args.String(0), args.Error(1)
}

func (m *MockStorage) UpdateMemoryBank(userID int64, content string) error {
	args := m.Called(userID, content)
	return args.Error(0)
}

// MaintenanceRepository methods

func (m *MockStorage) GetDBSize() (int64, error) {
	args := m.Called()
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) GetTableSizes() ([]storage.TableSize, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.TableSize), args.Error(1)
}

func (m *MockStorage) CleanupFactHistory(keepPerUser int) (int64, error) {
	args := m.Called(keepPerUser)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) CleanupAgentLogs(keepPerUserPerAgent int) (int64, error) {
	args := m.Called(keepPerUserPerAgent)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) CountAgentLogs() (int64, error) {
	args := m.Called()
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) CountFactHistory() (int64, error) {
	args := m.Called()
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) CountOrphanedTopics(userID int64) (int, error) {
	args := m.Called(userID)
	return args.Get(0).(int), args.Error(1)
}

func (m *MockStorage) GetOrphanedTopicIDs(userID int64) ([]int64, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]int64), args.Error(1)
}

func (m *MockStorage) CountOverlappingTopics(userID int64) (int, error) {
	args := m.Called(userID)
	return args.Get(0).(int), args.Error(1)
}

func (m *MockStorage) GetOverlappingTopics(userID int64) ([]storage.OverlappingPair, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.OverlappingPair), args.Error(1)
}

func (m *MockStorage) CountFactsOnOrphanedTopics(userID int64) (int, error) {
	args := m.Called(userID)
	return args.Get(0).(int), args.Error(1)
}

func (m *MockStorage) RecalculateTopicRanges(userID int64) (int, error) {
	args := m.Called(userID)
	return args.Get(0).(int), args.Error(1)
}

func (m *MockStorage) RecalculateTopicSizes(userID int64) (int, error) {
	args := m.Called(userID)
	return args.Get(0).(int), args.Error(1)
}

func (m *MockStorage) GetContaminatedTopics(userID int64) ([]storage.ContaminatedTopic, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.ContaminatedTopic), args.Error(1)
}

func (m *MockStorage) CountContaminatedTopics(userID int64) (int, error) {
	args := m.Called(userID)
	return args.Get(0).(int), args.Error(1)
}

func (m *MockStorage) FixContaminatedTopics(userID int64) (int64, error) {
	args := m.Called(userID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) Checkpoint() error {
	args := m.Called()
	return args.Error(0)
}

// AgentLogRepository methods

func (m *MockStorage) AddAgentLog(log storage.AgentLog) error {
	args := m.Called(log)
	return args.Error(0)
}

func (m *MockStorage) GetAgentLogs(agentType string, userID int64, limit int) ([]storage.AgentLog, error) {
	args := m.Called(agentType, userID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.AgentLog), args.Error(1)
}

func (m *MockStorage) GetAgentLogsExtended(filter storage.AgentLogFilter, limit, offset int) (storage.AgentLogResult, error) {
	args := m.Called(filter, limit, offset)
	return args.Get(0).(storage.AgentLogResult), args.Error(1)
}

// PeopleRepository methods

func (m *MockStorage) AddPerson(person storage.Person) (int64, error) {
	args := m.Called(person)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) UpdatePerson(person storage.Person) error {
	args := m.Called(person)
	return args.Error(0)
}

func (m *MockStorage) DeletePerson(userID, personID int64) error {
	args := m.Called(userID, personID)
	return args.Error(0)
}

func (m *MockStorage) GetPerson(userID, personID int64) (*storage.Person, error) {
	args := m.Called(userID, personID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Person), args.Error(1)
}

func (m *MockStorage) GetPeople(userID int64) ([]storage.Person, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Person), args.Error(1)
}

func (m *MockStorage) GetPeopleByIDs(userID int64, ids []int64) ([]storage.Person, error) {
	args := m.Called(userID, ids)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Person), args.Error(1)
}

func (m *MockStorage) GetAllPeople() ([]storage.Person, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Person), args.Error(1)
}

func (m *MockStorage) GetPeopleAfterID(minID int64) ([]storage.Person, error) {
	args := m.Called(minID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Person), args.Error(1)
}

func (m *MockStorage) FindPersonByTelegramID(userID, telegramID int64) (*storage.Person, error) {
	args := m.Called(userID, telegramID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Person), args.Error(1)
}

func (m *MockStorage) FindPersonByUsername(userID int64, username string) (*storage.Person, error) {
	args := m.Called(userID, username)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Person), args.Error(1)
}

func (m *MockStorage) FindPersonByAlias(userID int64, alias string) ([]storage.Person, error) {
	args := m.Called(userID, alias)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Person), args.Error(1)
}

func (m *MockStorage) FindPersonByName(userID int64, name string) (*storage.Person, error) {
	args := m.Called(userID, name)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Person), args.Error(1)
}

func (m *MockStorage) MergePeople(userID, targetID, sourceID int64, newBio string, newAliases []string, newUsername *string, newTelegramID *int64) error {
	args := m.Called(userID, targetID, sourceID, newBio, newAliases, newUsername, newTelegramID)
	return args.Error(0)
}

func (m *MockStorage) GetPeopleExtended(filter storage.PersonFilter, limit, offset int, sortBy, sortDir string) (storage.PersonResult, error) {
	args := m.Called(filter, limit, offset, sortBy, sortDir)
	return args.Get(0).(storage.PersonResult), args.Error(1)
}

func (m *MockStorage) CountPeopleWithoutEmbedding(userID int64) (int, error) {
	args := m.Called(userID)
	return args.Get(0).(int), args.Error(1)
}

func (m *MockStorage) GetPeopleWithoutEmbedding(userID int64) ([]storage.Person, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Person), args.Error(1)
}

// MockOpenRouterClient implements openrouter.Client for tests.
type MockOpenRouterClient struct {
	mock.Mock
}

func (m *MockOpenRouterClient) CreateChatCompletion(ctx context.Context, req openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return openrouter.ChatCompletionResponse{}, args.Error(1)
	}
	// Handle both pointer and value returns for compatibility
	if resp, ok := args.Get(0).(*openrouter.ChatCompletionResponse); ok {
		return *resp, args.Error(1)
	}
	return args.Get(0).(openrouter.ChatCompletionResponse), args.Error(1)
}

func (m *MockOpenRouterClient) CreateEmbeddings(ctx context.Context, req openrouter.EmbeddingRequest) (openrouter.EmbeddingResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return openrouter.EmbeddingResponse{}, args.Error(1)
	}
	// Handle both pointer and value returns for compatibility
	if resp, ok := args.Get(0).(*openrouter.EmbeddingResponse); ok {
		return *resp, args.Error(1)
	}
	return args.Get(0).(openrouter.EmbeddingResponse), args.Error(1)
}

// MockFileDownloader implements telegram.FileDownloader for tests.
type MockFileDownloader struct {
	mock.Mock
}

func (m *MockFileDownloader) DownloadFile(ctx context.Context, fileID string) ([]byte, error) {
	args := m.Called(ctx, fileID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockFileDownloader) DownloadFileAsBase64(ctx context.Context, fileID string) (string, error) {
	args := m.Called(ctx, fileID)
	return args.String(0), args.Error(1)
}

// MockYandexClient implements yandex.SpeechKitClient for tests.
type MockYandexClient struct {
	mock.Mock
}

func (m *MockYandexClient) Recognize(ctx context.Context, audioData []byte) (string, error) {
	args := m.Called(ctx, audioData)
	return args.String(0), args.Error(1)
}

// MockVectorSearcher implements rag.VectorSearcher for tests.
type MockVectorSearcher struct {
	mock.Mock
}

func (m *MockVectorSearcher) FindSimilarFacts(ctx context.Context, userID int64, embedding []float32, threshold float32) ([]storage.Fact, error) {
	args := m.Called(ctx, userID, embedding, threshold)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Fact), args.Error(1)
}

// MockAgentLogger implements agentlog.Logger for tests.
type MockAgentLogger struct {
	mock.Mock
}

// Log mock for agent log entry.
func (m *MockAgentLogger) Log(ctx context.Context, entry interface{}) {
	m.Called(ctx, entry)
}

// NewMockAgentLogger creates a new MockAgentLogger.
func NewMockAgentLogger() *MockAgentLogger {
	return &MockAgentLogger{}
}

// SetupDefaultMocks configures mocks with safe defaults for background operations.
// Call this when testing code that may trigger background loops.
func SetupDefaultMocks(s *MockStorage) {
	// Background loops may call these methods - return empty results
	s.On("GetFacts", mock.Anything).Return([]storage.Fact{}, nil).Maybe()
	s.On("GetTopicsExtended", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(storage.TopicResult{}, nil).Maybe()
	s.On("GetAllUsers").Return([]storage.User{}, nil).Maybe()
	s.On("GetTopics", mock.Anything).Return([]storage.Topic{}, nil).Maybe()
	s.On("GetTopicsPendingFacts", mock.Anything).Return([]storage.Topic{}, nil).Maybe()
	s.On("GetUnprocessedMessages", mock.Anything).Return([]storage.Message{}, nil).Maybe()
	s.On("GetRecentHistory", mock.Anything, mock.Anything).Return([]storage.Message{}, nil).Maybe()
}

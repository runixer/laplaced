// Package testutil provides centralized test mocks, fixtures, and helpers.
// All test files should import mocks from here instead of defining their own.
package testutil

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/runixer/laplaced/internal/llm"
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

func (m *MockBotAPI) EditMessageText(ctx context.Context, req telegram.EditMessageTextRequest) (*telegram.Message, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*telegram.Message), args.Error(1)
}

func (m *MockBotAPI) SendPhoto(ctx context.Context, req telegram.SendPhotoRequest) (*telegram.Message, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*telegram.Message), args.Error(1)
}

func (m *MockBotAPI) SendDocument(ctx context.Context, req telegram.SendDocumentRequest) (*telegram.Message, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*telegram.Message), args.Error(1)
}

func (m *MockBotAPI) SendMediaGroup(ctx context.Context, req telegram.SendMediaGroupRequest) ([]telegram.Message, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]telegram.Message), args.Error(1)
}

func (m *MockBotAPI) SendMediaGroupDocuments(ctx context.Context, req telegram.SendMediaGroupDocumentsRequest) ([]telegram.Message, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]telegram.Message), args.Error(1)
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

func (m *MockStorage) AddMessageToHistory(userID storage.ScopeID, message storage.Message) error {
	args := m.Called(userID, message)
	return args.Error(0)
}

func (m *MockStorage) ImportMessage(userID storage.ScopeID, message storage.Message) error {
	args := m.Called(userID, message)
	return args.Error(0)
}

func (m *MockStorage) GetRecentHistory(userID storage.ScopeID, limit int) ([]storage.Message, error) {
	args := m.Called(userID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) GetMessagesByIDs(userID storage.ScopeID, ids []int64) ([]storage.Message, error) {
	args := m.Called(userID, ids)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) ClearHistory(userID storage.ScopeID) error {
	args := m.Called(userID)
	return args.Error(0)
}

func (m *MockStorage) GetMessagesInRange(ctx context.Context, userID storage.ScopeID, startID, endID int64) ([]storage.Message, error) {
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

func (m *MockStorage) UpdateMessageTopic(userID storage.ScopeID, messageID, topicID int64) error {
	args := m.Called(userID, messageID, topicID)
	return args.Error(0)
}

func (m *MockStorage) UpdateMessagesTopicInRange(ctx context.Context, userID storage.ScopeID, startMsgID, endMsgID, topicID int64) error {
	args := m.Called(ctx, userID, startMsgID, endMsgID, topicID)
	return args.Error(0)
}

func (m *MockStorage) GetUnprocessedMessages(userID storage.ScopeID) ([]storage.Message, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return []storage.Message{}, args.Error(1)
	}
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) GetRecentSessionMessages(ctx context.Context, userID storage.ScopeID, limit int, excludeIDs []int64) ([]storage.Message, error) {
	args := m.Called(ctx, userID, limit, excludeIDs)
	if args.Get(0) == nil {
		return []storage.Message{}, args.Error(1)
	}
	return args.Get(0).([]storage.Message), args.Error(1)
}

func (m *MockStorage) SetReplyTransportID(userID storage.ScopeID, transportMsgID string) error {
	// Back-fill on the response-send path (bad-response-flag feature) — most tests
	// don't care, so auto-install a permissive default. Tests that DO care can
	// override; testify matches specific-args expectations first.
	if _, loaded := setReplyTransportIDDefaultInstalled.LoadOrStore(m, struct{}{}); !loaded {
		m.On("SetReplyTransportID", mock.Anything, mock.Anything).Return(nil).Maybe()
	}
	args := m.Called(userID, transportMsgID)
	return args.Error(0)
}

var setReplyTransportIDDefaultInstalled sync.Map // *MockStorage -> struct{}

func (m *MockStorage) GetReplyByTransportID(userID storage.ScopeID, transportMsgID string) (*storage.Message, error) {
	args := m.Called(userID, transportMsgID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Message), args.Error(1)
}

// FlagRepository methods

func (m *MockStorage) AddFlag(flag storage.Flag) error {
	args := m.Called(flag)
	return args.Error(0)
}

func (m *MockStorage) GetFlags(userID storage.ScopeID, limit int) ([]storage.Flag, error) {
	args := m.Called(userID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Flag), args.Error(1)
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

func (m *MockStorage) ResetUserData(userID storage.ScopeID) error {
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

func (m *MockStorage) DeleteTopic(userID storage.ScopeID, id int64) error {
	args := m.Called(userID, id)
	return args.Error(0)
}

func (m *MockStorage) DeleteTopicCascade(userID storage.ScopeID, id int64) error {
	args := m.Called(userID, id)
	return args.Error(0)
}

func (m *MockStorage) GetLastTopicEndMessageID(userID storage.ScopeID) (int64, error) {
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

func (m *MockStorage) GetTopicsByIDs(userID storage.ScopeID, ids []int64) ([]storage.Topic, error) {
	args := m.Called(userID, ids)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) GetTopics(userID storage.ScopeID) ([]storage.Topic, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Topic), args.Error(1)
}

func (m *MockStorage) SetTopicFactsExtracted(userID storage.ScopeID, topicID int64, extracted bool) error {
	args := m.Called(userID, topicID, extracted)
	return args.Error(0)
}

func (m *MockStorage) SetTopicConsolidationChecked(userID storage.ScopeID, topicID int64, checked bool) error {
	args := m.Called(userID, topicID, checked)
	return args.Error(0)
}

func (m *MockStorage) GetTopicsPendingFacts(userID storage.ScopeID) ([]storage.Topic, error) {
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

func (m *MockStorage) GetMergeCandidates(userID storage.ScopeID) ([]storage.MergeCandidate, error) {
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

func (m *MockStorage) GetFacts(userID storage.ScopeID) ([]storage.Fact, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Fact), args.Error(1)
}

func (m *MockStorage) GetFactsByIDs(userID storage.ScopeID, ids []int64) ([]storage.Fact, error) {
	args := m.Called(userID, ids)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Fact), args.Error(1)
}

func (m *MockStorage) GetFactsByTopicID(userID storage.ScopeID, topicID int64) ([]storage.Fact, error) {
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

func (m *MockStorage) GetFactStatsByUser(userID storage.ScopeID) (storage.FactStats, error) {
	args := m.Called(userID)
	return args.Get(0).(storage.FactStats), args.Error(1)
}

func (m *MockStorage) UpdateFact(fact storage.Fact) error {
	args := m.Called(fact)
	return args.Error(0)
}

func (m *MockStorage) UpdateFactsTopic(userID storage.ScopeID, oldTopicID, newTopicID int64) error {
	args := m.Called(userID, oldTopicID, newTopicID)
	return args.Error(0)
}

func (m *MockStorage) DeleteFact(userID storage.ScopeID, id int64) error {
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

func (m *MockStorage) GetFactHistory(userID storage.ScopeID, limit int) ([]storage.FactHistory, error) {
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

func (m *MockStorage) GetStats() (map[storage.ScopeID]storage.Stat, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[storage.ScopeID]storage.Stat), args.Error(1)
}

func (m *MockStorage) GetDashboardStats(userID storage.ScopeID) (*storage.DashboardStats, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.DashboardStats), args.Error(1)
}

// MemoryBankRepository methods

func (m *MockStorage) GetMemoryBank(userID storage.ScopeID) (string, error) {
	args := m.Called(userID)
	return args.String(0), args.Error(1)
}

func (m *MockStorage) UpdateMemoryBank(userID storage.ScopeID, content string) error {
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

func (m *MockStorage) CountOrphanedTopics(userID storage.ScopeID) (int, error) {
	args := m.Called(userID)
	return args.Get(0).(int), args.Error(1)
}

func (m *MockStorage) GetOrphanedTopicIDs(userID storage.ScopeID) ([]int64, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]int64), args.Error(1)
}

func (m *MockStorage) CountOverlappingTopics(userID storage.ScopeID) (int, error) {
	args := m.Called(userID)
	return args.Get(0).(int), args.Error(1)
}

func (m *MockStorage) GetOverlappingTopics(userID storage.ScopeID) ([]storage.OverlappingPair, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.OverlappingPair), args.Error(1)
}

func (m *MockStorage) CountFactsOnOrphanedTopics(userID storage.ScopeID) (int, error) {
	args := m.Called(userID)
	return args.Get(0).(int), args.Error(1)
}

func (m *MockStorage) RecalculateTopicRanges(userID storage.ScopeID) (int, error) {
	args := m.Called(userID)
	return args.Get(0).(int), args.Error(1)
}

func (m *MockStorage) RecalculateTopicSizes(userID storage.ScopeID) (int, error) {
	args := m.Called(userID)
	return args.Get(0).(int), args.Error(1)
}

func (m *MockStorage) GetContaminatedTopics(userID storage.ScopeID) ([]storage.ContaminatedTopic, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.ContaminatedTopic), args.Error(1)
}

func (m *MockStorage) CountContaminatedTopics(userID storage.ScopeID) (int, error) {
	args := m.Called(userID)
	return args.Get(0).(int), args.Error(1)
}

func (m *MockStorage) FixContaminatedTopics(userID storage.ScopeID) (int64, error) {
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

func (m *MockStorage) GetAgentLogs(agentType string, userID storage.ScopeID, limit int) ([]storage.AgentLog, error) {
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

func (m *MockStorage) GetAgentLogFull(ctx context.Context, id int64, userID storage.ScopeID) (*storage.AgentLog, error) {
	args := m.Called(ctx, id, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.AgentLog), args.Error(1)
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

func (m *MockStorage) DeletePerson(userID storage.ScopeID, personID int64) error {
	args := m.Called(userID, personID)
	return args.Error(0)
}

func (m *MockStorage) GetPerson(userID storage.ScopeID, personID int64) (*storage.Person, error) {
	args := m.Called(userID, personID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Person), args.Error(1)
}

func (m *MockStorage) GetPeople(userID storage.ScopeID) ([]storage.Person, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Person), args.Error(1)
}

func (m *MockStorage) GetPeopleByIDs(userID storage.ScopeID, ids []int64) ([]storage.Person, error) {
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

func (m *MockStorage) FindPersonByTelegramID(userID storage.ScopeID, telegramID int64) (*storage.Person, error) {
	args := m.Called(userID, telegramID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Person), args.Error(1)
}

func (m *MockStorage) FindPersonByExternalID(userID storage.ScopeID, transport, nativeID string) (*storage.Person, error) {
	args := m.Called(userID, transport, nativeID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Person), args.Error(1)
}

func (m *MockStorage) IsChannelScope(id storage.ScopeID) (bool, error) {
	args := m.Called(id)
	return args.Bool(0), args.Error(1)
}

// IdentityRepository.

func (m *MockStorage) GetIdentity(transport, nativeID string) (*storage.Identity, error) {
	args := m.Called(transport, nativeID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Identity), args.Error(1)
}

func (m *MockStorage) PutIdentity(transport, nativeID string, scopeID storage.ScopeID) error {
	args := m.Called(transport, nativeID, scopeID)
	return args.Error(0)
}

// PrincipalRepository.

func (m *MockStorage) GetOrCreatePrincipal(in storage.PrincipalInput) (storage.ScopeID, bool, error) {
	args := m.Called(in)
	return args.Get(0).(storage.ScopeID), args.Bool(1), args.Error(2)
}

func (m *MockStorage) GetPrincipal(scopeID storage.ScopeID) (*storage.Principal, error) {
	args := m.Called(scopeID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Principal), args.Error(1)
}

// ChannelRepository.

func (m *MockStorage) GetOrCreateChannel(transport, nativeID, displayName string) (storage.ScopeID, error) {
	args := m.Called(transport, nativeID, displayName)
	return args.Get(0).(storage.ScopeID), args.Error(1)
}

func (m *MockStorage) GetChannel(scopeID storage.ScopeID) (*storage.Channel, error) {
	args := m.Called(scopeID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Channel), args.Error(1)
}

func (m *MockStorage) FindPersonByUsername(userID storage.ScopeID, username string) (*storage.Person, error) {
	args := m.Called(userID, username)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Person), args.Error(1)
}

func (m *MockStorage) FindPersonByAlias(userID storage.ScopeID, alias string) ([]storage.Person, error) {
	args := m.Called(userID, alias)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Person), args.Error(1)
}

func (m *MockStorage) FindPersonByName(userID storage.ScopeID, name string) (*storage.Person, error) {
	args := m.Called(userID, name)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Person), args.Error(1)
}

func (m *MockStorage) MergePeople(userID storage.ScopeID, targetID, sourceID int64, newBio string, newAliases []string, newUsername *string, newTelegramID *int64) error {
	args := m.Called(userID, targetID, sourceID, newBio, newAliases, newUsername, newTelegramID)
	return args.Error(0)
}

func (m *MockStorage) GetPeopleExtended(filter storage.PersonFilter, limit, offset int, sortBy, sortDir string) (storage.PersonResult, error) {
	args := m.Called(filter, limit, offset, sortBy, sortDir)
	return args.Get(0).(storage.PersonResult), args.Error(1)
}

func (m *MockStorage) CountPeopleWithoutEmbedding(userID storage.ScopeID) (int, error) {
	args := m.Called(userID)
	return args.Get(0).(int), args.Error(1)
}

func (m *MockStorage) GetPeopleWithoutEmbedding(userID storage.ScopeID) ([]storage.Person, error) {
	args := m.Called(userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.Person), args.Error(1)
}

// MockLLMClient implements llm.Client for tests.
type MockLLMClient struct {
	mock.Mock
}

func (m *MockLLMClient) CreateChatCompletion(ctx context.Context, req llm.ChatCompletionRequest) (llm.ChatCompletionResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return llm.ChatCompletionResponse{}, args.Error(1)
	}
	// Handle both pointer and value returns for compatibility
	if resp, ok := args.Get(0).(*llm.ChatCompletionResponse); ok {
		return *resp, args.Error(1)
	}
	return args.Get(0).(llm.ChatCompletionResponse), args.Error(1)
}

// CreateChatCompletionStream returns the stream registered on the mock under
// the call's first return value. Tests can Return either a
// *llm.ChatCompletionStream or, for convenience, a bare
// <-chan llm.StreamEvent (auto-wrapped with an empty DebugRequestBody).
// The helper StreamEventsFromChunks(chunks...) builds such a channel.
func (m *MockLLMClient) CreateChatCompletionStream(ctx context.Context, req llm.ChatCompletionRequest) (*llm.ChatCompletionStream, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	switch v := args.Get(0).(type) {
	case *llm.ChatCompletionStream:
		return v, args.Error(1)
	case <-chan llm.StreamEvent:
		return &llm.ChatCompletionStream{Events: v}, args.Error(1)
	case chan llm.StreamEvent:
		return &llm.ChatCompletionStream{Events: v}, args.Error(1)
	default:
		return args.Get(0).(*llm.ChatCompletionStream), args.Error(1)
	}
}

// StreamEventsFromChunks builds a closed channel of StreamEvents from a list
// of chunks. Useful in unit tests as Return value for CreateChatCompletionStream.
// The channel is buffered enough for all events plus closes synchronously so
// consumers get full data on first read attempt.
func StreamEventsFromChunks(chunks ...llm.ChatCompletionChunk) <-chan llm.StreamEvent {
	ch := make(chan llm.StreamEvent, len(chunks))
	for i := range chunks {
		ch <- llm.StreamEvent{Chunk: &chunks[i]}
	}
	close(ch)
	return ch
}

func (m *MockLLMClient) CreateEmbeddings(ctx context.Context, req llm.EmbeddingRequest) (llm.EmbeddingResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return llm.EmbeddingResponse{}, args.Error(1)
	}
	// Handle both pointer and value returns for compatibility
	if resp, ok := args.Get(0).(*llm.EmbeddingResponse); ok {
		return *resp, args.Error(1)
	}
	return args.Get(0).(llm.EmbeddingResponse), args.Error(1)
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

// MockFileSaver implements files.FileSaver for tests.
type MockFileSaver struct {
	mock.Mock
}

func (m *MockFileSaver) SaveFile(ctx context.Context, userID storage.ScopeID, messageID int64, fileType string, originalName string, mimeType string, reader io.Reader, messageText string) (*int64, error) {
	args := m.Called(ctx, userID, messageID, fileType, originalName, mimeType, mock.Anything, messageText)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*int64), args.Error(1)
}

// MockVectorSearcher implements rag.VectorSearcher for tests.
type MockVectorSearcher struct {
	mock.Mock
}

func (m *MockVectorSearcher) FindSimilarFacts(ctx context.Context, userID storage.ScopeID, embedding []float32, threshold float32) ([]storage.Fact, error) {
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
	s.On("GetRecentSessionMessages", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return([]storage.Message{}, nil).Maybe()
	s.On("GetSessionArtifacts", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return([]storage.Artifact{}, nil).Maybe()
	// Reply back-fill on the response-send path (bad-response-flag feature).
	s.On("SetReplyTransportID", mock.Anything, mock.Anything).Return(nil).Maybe()
	// v0.7.0 re-embed defaults are auto-installed by MockStorage itself on first call.
}

// ArtifactRepository methods

func (m *MockStorage) AddArtifact(artifact storage.Artifact) (int64, error) {
	args := m.Called(artifact)
	if args.Get(0) == nil {
		return 0, args.Error(1)
	}
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) GetArtifact(userID storage.ScopeID, artifactID int64) (*storage.Artifact, error) {
	args := m.Called(userID, artifactID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Artifact), args.Error(1)
}

func (m *MockStorage) GetByHash(userID storage.ScopeID, contentHash string) (*storage.Artifact, error) {
	args := m.Called(userID, contentHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.Artifact), args.Error(1)
}

func (m *MockStorage) GetPendingArtifacts(userID storage.ScopeID, maxRetries int) ([]storage.Artifact, error) {
	args := m.Called(userID, maxRetries)
	if args.Get(0) == nil {
		return []storage.Artifact{}, args.Error(1)
	}
	return args.Get(0).([]storage.Artifact), args.Error(1)
}

func (m *MockStorage) GetArtifacts(filter storage.ArtifactFilter, limit, offset int) ([]storage.Artifact, int64, error) {
	args := m.Called(filter, limit, offset)
	if args.Get(0) == nil {
		return []storage.Artifact{}, 0, args.Error(1)
	}
	return args.Get(0).([]storage.Artifact), args.Get(1).(int64), args.Error(2)
}

func (m *MockStorage) UpdateArtifact(artifact storage.Artifact) error {
	args := m.Called(artifact)
	return args.Error(0)
}

func (m *MockStorage) RecoverArtifactStates(threshold time.Duration) error {
	args := m.Called(threshold)
	return args.Error(0)
}

func (m *MockStorage) GetArtifactsByIDs(userID storage.ScopeID, artifactIDs []int64) ([]storage.Artifact, error) {
	args := m.Called(userID, artifactIDs)
	if args.Get(0) == nil {
		return []storage.Artifact{}, args.Error(1)
	}
	return args.Get(0).([]storage.Artifact), args.Error(1)
}

func (m *MockStorage) GetSessionArtifacts(ctx context.Context, userID storage.ScopeID, limit int, maxAge time.Duration) ([]storage.Artifact, error) {
	args := m.Called(ctx, userID, limit, maxAge)
	if args.Get(0) == nil {
		return []storage.Artifact{}, args.Error(1)
	}
	return args.Get(0).([]storage.Artifact), args.Error(1)
}

func (m *MockStorage) IncrementContextLoadCount(userID storage.ScopeID, artifactIDs []int64) error {
	args := m.Called(userID, artifactIDs)
	return args.Error(0)
}

func (m *MockStorage) UpdateMessageID(userID storage.ScopeID, artifactID, messageID int64) error {
	args := m.Called(userID, artifactID, messageID)
	return args.Error(0)
}

// MockToolHandler lived here in earlier versions but was moved into
// internal/agent/laplace/mocks_test.go after the ToolHandler interface
// grew dependencies on laplace.ToolCallContext / laplace.ToolResult
// (which would create an import cycle testutil ↔ laplace). See
// docs/TESTING.md "Import Cycle Handling".

// MockRetriever is a mock for testing code that needs rag.Retriever.
// Due to import cycle constraints (rag tests import testutil), this mock
// cannot directly implement rag.Retriever.
//
// For laplace package tests that need rag.Retriever, use this approach:
//
//	// In laplace_context_test.go - define a thin wrapper that casts types:
//	type ragRetrieverAdapter struct {
//		*testutil.MockRetriever
//	}
//
//	func (a *ragRetrieverAdapter) Retrieve(ctx context.Context, userID storage.ScopeID, query string, opts *rag.RetrievalOptions) (*rag.RetrievalResult, *rag.RetrievalDebugInfo, error) {
//		result, debugInfo, err := a.MockRetriever.Retrieve(ctx, userID, query, opts)
//		return result.(*rag.RetrievalResult), debugInfo.(*rag.RetrievalDebugInfo), err
//	}
//
// For now, laplace_context_test.go keeps its inline mockRetriever which directly
// implements the interface. This is acceptable for a single file.
type MockRetriever struct {
	mock.Mock
}

// GetRecentTopics returns the N most recent topics for a user with message counts.
func (m *MockRetriever) GetRecentTopics(userID storage.ScopeID, limit int) ([]storage.TopicExtended, error) {
	args := m.Called(userID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.TopicExtended), args.Error(1)
}

// Retrieve performs RAG retrieval for a query.
// Returns (result interface{}, debugInfo interface{}, error).
// Caller is responsible for type assertions to *rag.RetrievalResult and *rag.RetrievalDebugInfo.
func (m *MockRetriever) Retrieve(ctx context.Context, userID storage.ScopeID, query string, opts interface{}) (interface{}, interface{}, error) {
	args := m.Called(ctx, userID, query, opts)
	return args.Get(0), args.Get(1), args.Error(2)
}

// --- v0.7.0 embedding migration (re-embed) ---
//
// These methods are called unconditionally by rag.Service.Start(). Most
// tests don't care about the migration, so we auto-install ".Maybe()"
// defaults returning "nothing to re-embed". Tests that DO care can still
// override with stricter expectations; testify matches specific-args
// expectations before the fall-through Maybe() default.

var reembedDefaultsInstalled sync.Map // *MockStorage -> struct{}

func (m *MockStorage) installReembedDefaults() {
	if _, loaded := reembedDefaultsInstalled.LoadOrStore(m, struct{}{}); loaded {
		return
	}
	m.On("GetTopicsNeedingReembed", mock.Anything, mock.Anything).Return([]storage.ReembedCandidate{}, nil).Maybe()
	m.On("GetFactsNeedingReembed", mock.Anything, mock.Anything).Return([]storage.ReembedCandidate{}, nil).Maybe()
	m.On("GetPeopleNeedingReembed", mock.Anything, mock.Anything).Return([]storage.ReembedCandidate{}, nil).Maybe()
	m.On("GetArtifactsNeedingReembed", mock.Anything, mock.Anything).Return([]storage.ReembedCandidate{}, nil).Maybe()
}

func (m *MockStorage) GetTopicsNeedingReembed(expectedVersion string, limit int) ([]storage.ReembedCandidate, error) {
	m.installReembedDefaults()
	args := m.Called(expectedVersion, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.ReembedCandidate), args.Error(1)
}

func (m *MockStorage) UpdateTopicEmbeddingVersion(id int64, emb []float32, version string) error {
	return m.Called(id, emb, version).Error(0)
}

func (m *MockStorage) GetFactsNeedingReembed(expectedVersion string, limit int) ([]storage.ReembedCandidate, error) {
	m.installReembedDefaults()
	args := m.Called(expectedVersion, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.ReembedCandidate), args.Error(1)
}

func (m *MockStorage) UpdateFactEmbeddingVersion(id int64, emb []float32, version string) error {
	return m.Called(id, emb, version).Error(0)
}

func (m *MockStorage) GetPeopleNeedingReembed(expectedVersion string, limit int) ([]storage.ReembedCandidate, error) {
	m.installReembedDefaults()
	args := m.Called(expectedVersion, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.ReembedCandidate), args.Error(1)
}

func (m *MockStorage) UpdatePersonEmbeddingVersion(id int64, emb []float32, version string) error {
	return m.Called(id, emb, version).Error(0)
}

func (m *MockStorage) GetArtifactsNeedingReembed(expectedVersion string, limit int) ([]storage.ReembedCandidate, error) {
	m.installReembedDefaults()
	args := m.Called(expectedVersion, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]storage.ReembedCandidate), args.Error(1)
}

func (m *MockStorage) UpdateArtifactEmbeddingVersion(id int64, emb []float32, version string) error {
	return m.Called(id, emb, version).Error(0)
}

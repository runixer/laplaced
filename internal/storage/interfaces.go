package storage

import (
	"context"
)

// MessageRepository handles message history operations.
type MessageRepository interface {
	AddMessageToHistory(userID int64, message Message) error
	ImportMessage(userID int64, message Message) error
	GetRecentHistory(userID int64, limit int) ([]Message, error)
	GetMessagesByIDs(userID int64, ids []int64) ([]Message, error)
	ClearHistory(userID int64) error
	GetMessagesInRange(ctx context.Context, userID int64, startID, endID int64) ([]Message, error)
	GetMessagesByTopicID(ctx context.Context, topicID int64) ([]Message, error)
	UpdateMessageTopic(userID int64, messageID, topicID int64) error
	UpdateMessagesTopicInRange(ctx context.Context, userID, startMsgID, endMsgID, topicID int64) error
	GetUnprocessedMessages(userID int64) ([]Message, error)
}

// UserRepository handles user data operations.
type UserRepository interface {
	UpsertUser(user User) error
	GetAllUsers() ([]User, error)
	ResetUserData(userID int64) error
}

// TopicRepository handles topic operations.
type TopicRepository interface {
	AddTopic(topic Topic) (int64, error)
	AddTopicWithoutMessageUpdate(topic Topic) (int64, error)
	CreateTopic(topic Topic) (int64, error)
	DeleteTopic(userID int64, id int64) error
	DeleteTopicCascade(userID int64, id int64) error
	GetLastTopicEndMessageID(userID int64) (int64, error)
	GetAllTopics() ([]Topic, error)
	GetTopicsAfterID(minID int64) ([]Topic, error)
	GetTopicsByIDs(userID int64, ids []int64) ([]Topic, error)
	GetTopics(userID int64) ([]Topic, error)
	SetTopicFactsExtracted(userID int64, topicID int64, extracted bool) error
	SetTopicConsolidationChecked(userID int64, topicID int64, checked bool) error
	GetTopicsPendingFacts(userID int64) ([]Topic, error)
	GetTopicsExtended(filter TopicFilter, limit, offset int, sortBy, sortDir string) (TopicResult, error)
	GetMergeCandidates(userID int64) ([]MergeCandidate, error)
}

// FactRepository handles fact operations.
type FactRepository interface {
	AddFact(fact Fact) (int64, error)
	GetFacts(userID int64) ([]Fact, error)
	GetFactsByIDs(userID int64, ids []int64) ([]Fact, error)
	GetFactsByTopicID(userID int64, topicID int64) ([]Fact, error)
	GetAllFacts() ([]Fact, error)
	GetFactsAfterID(minID int64) ([]Fact, error)
	GetFactStats() (FactStats, error)
	GetFactStatsByUser(userID int64) (FactStats, error)
	UpdateFact(fact Fact) error
	UpdateFactsTopic(userID int64, oldTopicID, newTopicID int64) error
	DeleteFact(userID, id int64) error
}

// FactHistoryRepository handles fact history operations.
type FactHistoryRepository interface {
	AddFactHistory(history FactHistory) error
	UpdateFactHistoryTopic(oldTopicID, newTopicID int64) error
	GetFactHistory(userID int64, limit int) ([]FactHistory, error)
	GetFactHistoryExtended(filter FactHistoryFilter, limit, offset int, sortBy, sortDir string) (FactHistoryResult, error)
}

// StatsRepository handles usage statistics.
type StatsRepository interface {
	AddStat(stat Stat) error
	GetStats() (map[int64]Stat, error)
	GetDashboardStats(userID int64) (*DashboardStats, error)
}

// MemoryBankRepository handles the legacy memory bank.
type MemoryBankRepository interface {
	GetMemoryBank(userID int64) (string, error)
	UpdateMemoryBank(userID int64, content string) error
}

// MaintenanceRepository handles database maintenance operations.
type MaintenanceRepository interface {
	GetDBSize() (int64, error)
	GetTableSizes() ([]TableSize, error)
	CleanupFactHistory(keepPerUser int) (int64, error)
	CleanupAgentLogs(keepPerUserPerAgent int) (int64, error)
	CountAgentLogs() (int64, error)
	CountFactHistory() (int64, error)

	// Database health diagnostics
	CountOrphanedTopics(userID int64) (int, error)
	GetOrphanedTopicIDs(userID int64) ([]int64, error)
	CountOverlappingTopics(userID int64) (int, error)
	GetOverlappingTopics(userID int64) ([]OverlappingPair, error)
	CountFactsOnOrphanedTopics(userID int64) (int, error)
	RecalculateTopicRanges(userID int64) (int, error)
	RecalculateTopicSizes(userID int64) (int, error)

	// Cross-user contamination detection and repair
	GetContaminatedTopics(userID int64) ([]ContaminatedTopic, error)
	CountContaminatedTopics(userID int64) (int, error)
	FixContaminatedTopics(userID int64) (int64, error)

	// WAL checkpoint for ensuring data persistence
	Checkpoint() error
}

// AgentLogRepository handles unified agent debug log operations.
type AgentLogRepository interface {
	AddAgentLog(log AgentLog) error
	GetAgentLogs(agentType string, userID int64, limit int) ([]AgentLog, error)
	GetAgentLogsExtended(filter AgentLogFilter, limit, offset int) (AgentLogResult, error)
}

// PeopleRepository handles people from the user's social graph.
type PeopleRepository interface {
	// CRUD operations
	AddPerson(person Person) (int64, error)
	UpdatePerson(person Person) error
	DeletePerson(userID, personID int64) error

	// Retrieval
	GetPerson(userID, personID int64) (*Person, error)
	GetPeople(userID int64) ([]Person, error)
	GetPeopleByIDs(userID int64, ids []int64) ([]Person, error)
	GetAllPeople() ([]Person, error)
	GetPeopleAfterID(minID int64) ([]Person, error)

	// Direct matching (fast path for @username and name lookup)
	FindPersonByTelegramID(userID, telegramID int64) (*Person, error)
	FindPersonByUsername(userID int64, username string) (*Person, error)
	FindPersonByAlias(userID int64, alias string) ([]Person, error)
	FindPersonByName(userID int64, name string) (*Person, error)

	// Merge operations
	MergePeople(userID, targetID, sourceID int64, newBio string, newAliases []string, newUsername *string, newTelegramID *int64) error

	// Extended queries with filtering and pagination
	GetPeopleExtended(filter PersonFilter, limit, offset int, sortBy, sortDir string) (PersonResult, error)

	// Maintenance
	CountPeopleWithoutEmbedding(userID int64) (int, error)
	GetPeopleWithoutEmbedding(userID int64) ([]Person, error)
}

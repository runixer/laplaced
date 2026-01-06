package storage

import (
	"context"
)

// MessageRepository handles message history operations.
type MessageRepository interface {
	AddMessageToHistory(userID int64, message Message) error
	ImportMessage(userID int64, message Message) error
	GetRecentHistory(userID int64, limit int) ([]Message, error)
	GetMessagesByIDs(ids []int64) ([]Message, error)
	ClearHistory(userID int64) error
	GetMessagesInRange(ctx context.Context, userID int64, startID, endID int64) ([]Message, error)
	GetMessagesByTopicID(ctx context.Context, topicID int64) ([]Message, error)
	UpdateMessageTopic(messageID, topicID int64) error
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
	DeleteTopic(id int64) error
	DeleteTopicCascade(id int64) error
	GetLastTopicEndMessageID(userID int64) (int64, error)
	GetAllTopics() ([]Topic, error)
	GetTopicsAfterID(minID int64) ([]Topic, error)
	GetTopicsByIDs(ids []int64) ([]Topic, error)
	GetTopics(userID int64) ([]Topic, error)
	SetTopicFactsExtracted(topicID int64, extracted bool) error
	SetTopicConsolidationChecked(topicID int64, checked bool) error
	GetTopicsPendingFacts(userID int64) ([]Topic, error)
	GetTopicsExtended(filter TopicFilter, limit, offset int, sortBy, sortDir string) (TopicResult, error)
	GetMergeCandidates(userID int64) ([]MergeCandidate, error)
}

// FactRepository handles fact operations.
type FactRepository interface {
	AddFact(fact Fact) (int64, error)
	GetFacts(userID int64) ([]Fact, error)
	GetFactsByIDs(ids []int64) ([]Fact, error)
	GetFactsByTopicID(topicID int64) ([]Fact, error)
	GetAllFacts() ([]Fact, error)
	GetFactsAfterID(minID int64) ([]Fact, error)
	GetFactStats() (FactStats, error)
	GetFactStatsByUser(userID int64) (FactStats, error)
	UpdateFact(fact Fact) error
	UpdateFactTopic(oldTopicID, newTopicID int64) error
	DeleteFact(userID, id int64) error
}

// FactHistoryRepository handles fact history operations.
type FactHistoryRepository interface {
	AddFactHistory(history FactHistory) error
	UpdateFactHistoryTopic(oldTopicID, newTopicID int64) error
	GetFactHistory(userID int64, limit int) ([]FactHistory, error)
	GetFactHistoryExtended(filter FactHistoryFilter, limit, offset int, sortBy, sortDir string) (FactHistoryResult, error)
}

// LogRepository handles RAG log operations.
type LogRepository interface {
	AddRAGLog(log RAGLog) error
	GetRAGLogs(userID int64, limit int) ([]RAGLog, error)
	GetTopicExtractionLogs(limit, offset int) ([]RAGLog, int, error)
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
	CleanupRagLogs(keepPerUser int) (int64, error)
	CleanupRerankerLogs(keepPerUser int) (int64, error)

	// Database health diagnostics
	CountOrphanedTopics(userID int64) (int, error)
	GetOrphanedTopicIDs(userID int64) ([]int64, error)
	CountOverlappingTopics(userID int64) (int, error)
	CountFactsOnOrphanedTopics(userID int64) (int, error)
	RecalculateTopicSizes(userID int64) (int, error)

	// Cross-user contamination detection and repair
	GetContaminatedTopics(userID int64) ([]ContaminatedTopic, error)
	CountContaminatedTopics(userID int64) (int, error)
	FixContaminatedTopics(userID int64) (int64, error)

	// WAL checkpoint for ensuring data persistence
	Checkpoint() error
}

// RerankerLogRepository handles reranker debug log operations.
type RerankerLogRepository interface {
	AddRerankerLog(log RerankerLog) error
	GetRerankerLogs(userID int64, limit int) ([]RerankerLog, error)
}

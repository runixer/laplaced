// Package storage provides SQLite repository interfaces and implementations.
//
// Repository Pattern:
//   - All repositories are interfaces for testability
//   - SQLiteStore implements all 12+ interfaces
//   - Methods return wrapped errors with context
//
// Critical Invariants (MUST READ):
//   - Message IDs are GLOBAL auto-increment, NOT per-user
//   - ANY query using ID ranges MUST include user_id filter
//   - Example: WHERE id >= ? AND id <= ? AND user_id = ?
//
// Thread Safety:
//   - SQLiteStore uses a single connection with WAL mode
//   - Concurrent reads are safe
//   - Writes are serialized by SQLite
//
// Usage Example:
//
//	mockStorage := testutil.NewMockStorage()
//	mockStorage.On("GetFacts", userID).Return(testutil.TestFacts(), nil)
//	svc := NewService(mockStorage, ...)
package storage

import (
	"context"
	"time"
)

// MessageRepository handles message history operations.
//
// Messages represent the conversation log between user and assistant.
// Message IDs are globally auto-incremented across all users.
//
// Critical: Any range query MUST include user_id filter to prevent data leakage.
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
	GetRecentSessionMessages(ctx context.Context, userID int64, limit int, excludeIDs []int64) ([]Message, error)
}

// UserRepository handles user data operations.
type UserRepository interface {
	UpsertUser(user User) error
	GetAllUsers() ([]User, error)
	ResetUserData(userID int64) error
}

// TopicRepository handles topic operations.
//
// Topics are compressed summaries of conversation chunks created after
// session archival (inactivity timeout or force-close). Each topic has
// an embedding vector for RAG retrieval.
//
// Key Fields:
//   - StartMsgID/EndMsgID: Message range (inclusive)
//   - SizeChars: Total character count for size tracking
//   - Embedding: Vector for semantic search
//
// Note: Message IDs within a topic are guaranteed to be from the same user.
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
//
// Facts are structured pieces of information extracted from conversations
// by the Archivist agent. They form the user's long-term profile memory.
//
// Fact Types:
//   - identity: Core user information (name, location)
//   - importance: User-defined importance score (0-100)
//   - Facts with importance ≥ 90 are always included in profile
//
// Each fact has an embedding vector for semantic search and deduplication.
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
	GetAgentLogFull(ctx context.Context, id int64, userID int64) (*AgentLog, error)
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

// ArtifactFilter defines filtering options for artifact queries.
type ArtifactFilter struct {
	UserID   int64
	State    string // "pending", "processing", "ready", "failed", or "" for all
	FileType string // "image", "voice", "pdf", "video_note", "document", or "" for all
}

// ArtifactRepository handles artifact file metadata operations.
type ArtifactRepository interface {
	AddArtifact(artifact Artifact) (int64, error)
	GetArtifact(userID, artifactID int64) (*Artifact, error)
	GetByHash(userID int64, contentHash string) (*Artifact, error)
	// GetPendingArtifacts returns artifacts ready for processing:
	// - state='pending' (new artifacts)
	// - state='failed' with retry_count < maxRetries and sufficient backoff elapsed (v0.6.0)
	GetPendingArtifacts(userID int64, maxRetries int) ([]Artifact, error)
	GetArtifacts(filter ArtifactFilter, limit, offset int) ([]Artifact, int64, error)
	UpdateArtifact(artifact Artifact) error
	RecoverArtifactStates(threshold time.Duration) error
	GetArtifactsByIDs(userID int64, artifactIDs []int64) ([]Artifact, error)
	// IncrementContextLoadCount tracks usage when artifacts are loaded into LLM context (v0.6.0)
	IncrementContextLoadCount(userID int64, artifactIDs []int64) error
	// UpdateMessageID links artifact to history message (called after message is saved)
	// Requires userID for proper data isolation.
	UpdateMessageID(userID, artifactID, messageID int64) error
}

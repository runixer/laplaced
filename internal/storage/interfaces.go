// Package storage provides dialect-aware repository interfaces and a single
// implementation (Store) that serves both the SQLite and PostgreSQL
// backends. The active dialect is chosen at construction; the repository
// code below is backend-agnostic (it goes through the central exec shim and the
// Dialect interface — see storage.go and dialect.go).
//
// Repository Pattern:
//   - All repositories are interfaces for testability
//   - Store implements all 12+ interfaces
//   - Methods return wrapped errors with context
//
// Critical Invariants (MUST READ):
//   - Message IDs are GLOBAL auto-increment, NOT per-user
//   - ANY query using ID ranges MUST include user_id filter
//   - Example: WHERE id >= ? AND id <= ? AND user_id = ?
//
// Thread Safety (SQLite backend):
//   - The SQLite store uses a single connection with WAL mode
//   - Concurrent reads are safe; writes are serialized by SQLite
//   - PostgreSQL uses a normal connection pool
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
	AddMessageToHistory(userID ScopeID, message Message) error
	ImportMessage(userID ScopeID, message Message) error
	GetRecentHistory(userID ScopeID, limit int) ([]Message, error)
	GetMessagesByIDs(userID ScopeID, ids []int64) ([]Message, error)
	ClearHistory(userID ScopeID) error
	GetMessagesInRange(ctx context.Context, userID ScopeID, startID, endID int64) ([]Message, error)
	GetMessagesByTopicID(ctx context.Context, topicID int64) ([]Message, error)
	UpdateMessageTopic(userID ScopeID, messageID, topicID int64) error
	UpdateMessagesTopicInRange(ctx context.Context, userID ScopeID, startMsgID, endMsgID, topicID int64) error
	GetUnprocessedMessages(userID ScopeID) ([]Message, error)
	GetRecentSessionMessages(ctx context.Context, userID ScopeID, limit int, excludeIDs []int64) ([]Message, error)
	// SetReplyTransportID back-fills the transport-native message id on the user's
	// most recent unlinked assistant reply, after that reply is sent.
	SetReplyTransportID(userID ScopeID, transportMsgID string) error
	// GetReplyByTransportID resolves an assistant reply by its transport-native
	// message id (nil, nil on miss). Used by the inbound-reaction handler.
	GetReplyByTransportID(userID ScopeID, transportMsgID string) (*Message, error)
}

// FlagRepository handles user-flagged bad replies (migration 016). A flag is
// recorded when a user reacts to a bot reply; it carries the reply's trace_id so
// the operator can investigate straight from the trace.
type FlagRepository interface {
	AddFlag(flag Flag) error
	GetFlags(userID ScopeID, limit int) ([]Flag, error)
}

// UserRepository handles user data operations.
type UserRepository interface {
	UpsertUser(user User) error
	GetAllUsers() ([]User, error)
	ResetUserData(userID ScopeID) error
}

// ScopeRepository exposes scope-type detection over the memory partition key.
// Scope creation and lookup are owned by the Identity/Principal/Channel
// repositories and PassthroughScopeID; this is the read side used by background
// loops. See internal/bot/identity.go.
type ScopeRepository interface {
	// IsChannelScope reports whether the scope id is a multi-participant channel
	// (scope_type='channel'). Background memory loops have only the scope id and use
	// this to gate channel-aware behaviour. Absence of a row means a DM/person scope
	// (Telegram passthrough / Mattermost DM / principal), so it returns false.
	IsChannelScope(id ScopeID) (bool, error)
}

// IdentityRepository maps a transport-native handle to its scope id. The
// resolver-driven DM flow looks up an identity to reuse an existing scope, then
// writes one after resolving a principal. Telegram passthrough writes no identity
// row (its id is a deterministic uuidv5).
type IdentityRepository interface {
	GetIdentity(transport, nativeID string) (*Identity, error)
	PutIdentity(transport, nativeID string, scopeID ScopeID) error
}

// PrincipalRepository manages AD-backed person scopes. A principal
// is one human; many transport identities may map to a single principal scope for
// unified cross-transport memory. Dedup prefers object_guid (the stable AD anchor,
// nullable until a later lookup) and falls back to ad_login (lowercase preferred_username),
// so a later object_guid backfill is additive and never re-partitions memory.
type PrincipalRepository interface {
	GetOrCreatePrincipal(in PrincipalInput) (ScopeID, bool, error)
	GetPrincipal(scopeID ScopeID) (*Principal, error)
}

// ChannelRepository manages channel scopes. A channel scope is
// keyed deterministically by (transport, native_id); participants are tracked as
// people within the scope.
type ChannelRepository interface {
	GetOrCreateChannel(transport, nativeID, displayName string) (ScopeID, error)
	GetChannel(scopeID ScopeID) (*Channel, error)
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
	DeleteTopic(userID ScopeID, id int64) error
	DeleteTopicCascade(userID ScopeID, id int64) error
	GetLastTopicEndMessageID(userID ScopeID) (int64, error)
	GetAllTopics() ([]Topic, error)
	GetTopicsAfterID(minID int64) ([]Topic, error)
	GetTopicsByIDs(userID ScopeID, ids []int64) ([]Topic, error)
	GetTopics(userID ScopeID) ([]Topic, error)
	SetTopicFactsExtracted(userID ScopeID, topicID int64, extracted bool) error
	SetTopicConsolidationChecked(userID ScopeID, topicID int64, checked bool) error
	GetTopicsPendingFacts(userID ScopeID) ([]Topic, error)
	GetTopicsExtended(filter TopicFilter, limit, offset int, sortBy, sortDir string) (TopicResult, error)
	GetMergeCandidates(userID ScopeID) ([]MergeCandidate, error)

	// v0.7.0: embedding migration
	GetTopicsNeedingReembed(expectedVersion string, limit int) ([]ReembedCandidate, error)
	UpdateTopicEmbeddingVersion(id int64, emb []float32, version string) error
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
	GetFacts(userID ScopeID) ([]Fact, error)
	GetFactsByIDs(userID ScopeID, ids []int64) ([]Fact, error)
	GetFactsByTopicID(userID ScopeID, topicID int64) ([]Fact, error)
	GetAllFacts() ([]Fact, error)
	GetFactsAfterID(minID int64) ([]Fact, error)
	GetFactStats() (FactStats, error)
	GetFactStatsByUser(userID ScopeID) (FactStats, error)
	UpdateFact(fact Fact) error
	UpdateFactsTopic(userID ScopeID, oldTopicID, newTopicID int64) error
	DeleteFact(userID ScopeID, id int64) error

	// v0.7.0: embedding migration
	GetFactsNeedingReembed(expectedVersion string, limit int) ([]ReembedCandidate, error)
	UpdateFactEmbeddingVersion(id int64, emb []float32, version string) error
}

// FactHistoryRepository handles fact history operations.
type FactHistoryRepository interface {
	AddFactHistory(history FactHistory) error
	UpdateFactHistoryTopic(oldTopicID, newTopicID int64) error
	GetFactHistory(userID ScopeID, limit int) ([]FactHistory, error)
	GetFactHistoryExtended(filter FactHistoryFilter, limit, offset int, sortBy, sortDir string) (FactHistoryResult, error)
}

// StatsRepository handles usage statistics.
type StatsRepository interface {
	AddStat(stat Stat) error
	GetStats() (map[ScopeID]Stat, error)
	GetDashboardStats(userID ScopeID) (*DashboardStats, error)
}

// MemoryBankRepository handles the legacy memory bank.
type MemoryBankRepository interface {
	GetMemoryBank(userID ScopeID) (string, error)
	UpdateMemoryBank(userID ScopeID, content string) error
}

// MaintenanceRepository handles database maintenance operations.
type MaintenanceRepository interface {
	GetDBSize() (int64, error)
	GetTableSizes() ([]TableSize, error)
	CleanupFactHistory(keepPerUser int) (int64, error)
	CleanupAgentLogs(keepPerUserPerAgent int, minAge time.Duration) (int64, error)
	CountAgentLogs() (int64, error)
	CountFactHistory() (int64, error)

	// Database health diagnostics
	CountOrphanedTopics(userID ScopeID) (int, error)
	GetOrphanedTopicIDs(userID ScopeID) ([]int64, error)
	CountOverlappingTopics(userID ScopeID) (int, error)
	GetOverlappingTopics(userID ScopeID) ([]OverlappingPair, error)
	CountFactsOnOrphanedTopics(userID ScopeID) (int, error)
	RecalculateTopicRanges(userID ScopeID) (int, error)
	RecalculateTopicSizes(userID ScopeID) (int, error)

	// Cross-user contamination detection and repair
	GetContaminatedTopics(userID ScopeID) ([]ContaminatedTopic, error)
	CountContaminatedTopics(userID ScopeID) (int, error)
	FixContaminatedTopics(userID ScopeID) (int64, error)

	// WAL checkpoint for ensuring data persistence
	Checkpoint() error
}

// AgentLogRepository handles unified agent debug log operations.
type AgentLogRepository interface {
	AddAgentLog(log AgentLog) error
	GetAgentLogs(agentType string, userID ScopeID, limit int) ([]AgentLog, error)
	GetAgentLogsExtended(filter AgentLogFilter, limit, offset int) (AgentLogResult, error)
	GetAgentLogFull(ctx context.Context, id int64, userID ScopeID) (*AgentLog, error)
}

// PeopleRepository handles people from the user's social graph.
type PeopleRepository interface {
	// CRUD operations
	AddPerson(person Person) (int64, error)
	UpdatePerson(person Person) error
	DeletePerson(userID ScopeID, personID int64) error

	// Retrieval
	GetPerson(userID ScopeID, personID int64) (*Person, error)
	GetPeople(userID ScopeID) ([]Person, error)
	GetPeopleByIDs(userID ScopeID, ids []int64) ([]Person, error)
	GetAllPeople() ([]Person, error)
	GetPeopleAfterID(minID int64) ([]Person, error)

	// Direct matching (fast path for @username and name lookup)
	FindPersonByTelegramID(userID ScopeID, telegramID int64) (*Person, error)
	// FindPersonByExternalID matches on the transport-neutral external id
	// (transport, native_id) introduced in v0.10. For Telegram this is
	// equivalent to FindPersonByTelegramID via the backfilled ('telegram', id).
	FindPersonByExternalID(userID ScopeID, transport, nativeID string) (*Person, error)
	FindPersonByUsername(userID ScopeID, username string) (*Person, error)
	FindPersonByAlias(userID ScopeID, alias string) ([]Person, error)
	FindPersonByName(userID ScopeID, name string) (*Person, error)

	// Merge operations
	MergePeople(userID ScopeID, targetID, sourceID int64, newBio string, newAliases []string, newUsername *string, newTelegramID *int64) error

	// Extended queries with filtering and pagination
	GetPeopleExtended(filter PersonFilter, limit, offset int, sortBy, sortDir string) (PersonResult, error)

	// Maintenance
	CountPeopleWithoutEmbedding(userID ScopeID) (int, error)
	GetPeopleWithoutEmbedding(userID ScopeID) ([]Person, error)

	// v0.7.0: embedding migration
	GetPeopleNeedingReembed(expectedVersion string, limit int) ([]ReembedCandidate, error)
	UpdatePersonEmbeddingVersion(id int64, emb []float32, version string) error
}

// ArtifactFilter defines filtering options for artifact queries.
type ArtifactFilter struct {
	UserID   ScopeID
	State    string // "pending", "processing", "ready", "failed", or "" for all
	FileType string // "image", "voice", "pdf", "video_note", "document", or "" for all
}

// ArtifactRepository handles artifact file metadata operations.
type ArtifactRepository interface {
	AddArtifact(artifact Artifact) (int64, error)
	GetArtifact(userID ScopeID, artifactID int64) (*Artifact, error)
	GetByHash(userID ScopeID, contentHash string) (*Artifact, error)
	// GetPendingArtifacts returns artifacts ready for processing:
	// - state='pending' (new artifacts)
	// - state='failed' with retry_count < maxRetries and sufficient backoff elapsed (v0.6.0)
	GetPendingArtifacts(userID ScopeID, maxRetries int) ([]Artifact, error)
	GetArtifacts(filter ArtifactFilter, limit, offset int) ([]Artifact, int64, error)
	UpdateArtifact(artifact Artifact) error
	RecoverArtifactStates(threshold time.Duration) error
	GetArtifactsByIDs(userID ScopeID, artifactIDs []int64) ([]Artifact, error)
	// GetSessionArtifacts returns artifacts on messages still in active session (topic_id IS NULL).
	// Used to inject freshly-created artifacts as priority candidates for the reranker.
	GetSessionArtifacts(ctx context.Context, userID ScopeID, limit int, maxAge time.Duration) ([]Artifact, error)
	// IncrementContextLoadCount tracks usage when artifacts are loaded into LLM context (v0.6.0)
	IncrementContextLoadCount(userID ScopeID, artifactIDs []int64) error
	// UpdateMessageID links artifact to history message (called after message is saved)
	// Requires userID for proper data isolation.
	UpdateMessageID(userID ScopeID, artifactID, messageID int64) error

	// v0.7.0: embedding migration
	GetArtifactsNeedingReembed(expectedVersion string, limit int) ([]ReembedCandidate, error)
	UpdateArtifactEmbeddingVersion(id int64, emb []float32, version string) error
}

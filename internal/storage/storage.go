package storage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/runixer/laplaced/internal/storage/migrations"
)

type Message struct {
	ID        int64
	UserID    int64
	Role      string
	Content   string
	TopicID   *int64 // Nullable
	CreatedAt time.Time
}

type User struct {
	ID        int64
	Username  string
	FirstName string
	LastName  string
	LastSeen  time.Time
}

type Topic struct {
	ID                   int64
	UserID               int64
	Summary              string
	StartMsgID           int64
	EndMsgID             int64
	SizeChars            int // Total character count of all messages in topic
	Embedding            []float32
	FactsExtracted       bool
	IsConsolidated       bool
	ConsolidationChecked bool
	CreatedAt            time.Time
}

type TopicExtended struct {
	Topic
	FactsCount   int
	MessageCount int
}

type MergeCandidate struct {
	Topic1 Topic
	Topic2 Topic
}

type TopicFilter struct {
	UserID         int64
	Search         string
	HasFacts       *bool // nil = all, true = yes, false = no
	IsConsolidated *bool // nil = all
	TopicID        *int64
}

type TopicResult struct {
	Data       []TopicExtended
	TotalCount int
}

type Fact struct {
	ID          int64
	UserID      int64
	Relation    string
	Content     string
	Category    string
	Type        string // identity, context, status
	Importance  int    // 0-100
	Embedding   []float32
	TopicID     *int64 // Nullable
	CreatedAt   time.Time
	LastUpdated time.Time
}

type FactHistory struct {
	ID           int64
	FactID       int64
	UserID       int64
	Action       string // add, update, delete
	OldContent   string
	NewContent   string
	Reason       string
	Category     string
	Relation     string
	Importance   int
	TopicID      *int64
	CreatedAt    time.Time
	RequestInput string
}

type FactHistoryFilter struct {
	UserID   int64
	Action   string
	Category string
	Search   string
}

type FactHistoryResult struct {
	Data       []FactHistory
	TotalCount int
}

type FactStats struct {
	CountByType map[string]int
	AvgAgeDays  float64
}

type Stat struct {
	UserID     int64
	TokensUsed int
	CostUSD    float64
}

type DashboardStats struct {
	TotalTopics         int
	AvgTopicSize        float64
	ProcessedTopicsPct  float64
	ConsolidatedTopics  int
	TotalFacts          int
	FactsByCategory     map[string]int
	FactsByType         map[string]int
	TotalMessages       int
	UnprocessedMessages int
	TotalRAGQueries     int
	AvgRAGCost          float64
	MessagesPerDay      map[string]int
	FactsGrowth         map[string]int
}

// RerankerCandidate is a single candidate for JSON serialization
type RerankerCandidate struct {
	TopicID      int64   `json:"topic_id"`
	Summary      string  `json:"summary"`
	Score        float32 `json:"score"`
	Date         string  `json:"date"`
	MessageCount int     `json:"message_count"`
	SizeChars    int     `json:"size_chars"`
}

// RerankerToolCall represents one iteration of tool calls
type RerankerToolCall struct {
	Iteration int                     `json:"iteration"`
	TopicIDs  []int64                 `json:"topic_ids"`
	Topics    []RerankerToolCallTopic `json:"topics"`
}

// RerankerToolCallTopic contains topic info for tool call display
type RerankerToolCallTopic struct {
	ID      int64  `json:"id"`
	Summary string `json:"summary"`
}

// AgentLog stores debug traces from LLM agent calls (unified logging for all agents)
type AgentLog struct {
	ID                int64
	UserID            int64
	AgentType         string // laplace, reranker, splitter, merger, enricher, archivist, scout
	InputPrompt       string
	InputContext      string // JSON - full OpenRouter API request
	OutputResponse    string
	OutputParsed      string // JSON - structured output
	OutputContext     string // JSON - full OpenRouter API response
	Model             string
	PromptTokens      int
	CompletionTokens  int
	TotalCost         *float64
	DurationMs        int
	Metadata          string // JSON - agent-specific data
	Success           bool
	ErrorMessage      string
	ConversationTurns string // JSON - all request/response turns for multi-turn agents
	CreatedAt         time.Time
}

// AgentLogFilter for filtering agent logs
type AgentLogFilter struct {
	UserID    int64
	AgentType string
	Success   *bool
	Search    string
}

// AgentLogResult wraps agent logs with total count for pagination
type AgentLogResult struct {
	Data       []AgentLog
	TotalCount int
}

// Person represents a person from the user's social graph.
type Person struct {
	ID           int64     `json:"id"`
	UserID       int64     `json:"user_id"`
	DisplayName  string    `json:"display_name"`
	Aliases      []string  `json:"aliases"`     // JSON array: ["Гелёй", "@akaGelo"]
	TelegramID   *int64    `json:"telegram_id"` // For direct @mention match
	Username     *string   `json:"username"`    // @username without @
	Circle       string    `json:"circle"`      // Family, Friends, Work_Inner, Work_Outer, Other
	Bio          string    `json:"bio"`         // Aggregated profile (2-3 sentences)
	Embedding    []float32 `json:"embedding"`   // Bio vector (JSON float32 array)
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	MentionCount int       `json:"mention_count"`
}

// PersonFilter for filtering people queries.
type PersonFilter struct {
	UserID int64
	Circle string
	Search string
}

// PersonResult wraps people with total count for pagination.
type PersonResult struct {
	Data       []Person
	TotalCount int
}

// Artifact represents a file stored in the artifacts system.
// v0.6.0: Simplified with summary-based search (no chunks, no full_text).
type Artifact struct {
	ID        int64 `json:"id"`
	UserID    int64 `json:"user_id"`
	MessageID int64 `json:"message_id"`

	// File metadata
	FileType     string `json:"file_type"` // 'image', 'voice', 'pdf', 'video_note', 'document'
	FilePath     string `json:"file_path"` // Relative path from storage dir
	FileSize     int64  `json:"file_size"` // Bytes
	MimeType     string `json:"mime_type"`
	OriginalName string `json:"original_name"` // From Telegram

	// Deduplication
	ContentHash string `json:"content_hash"` // SHA256 of file content

	// Processing status
	State        string  `json:"state"` // 'pending', 'processing', 'ready', 'failed'
	ErrorMessage *string `json:"error_message,omitempty"`

	// Retry tracking (v0.6.0 - CRIT-3)
	RetryCount   int        `json:"retry_count"`
	LastFailedAt *time.Time `json:"last_failed_at,omitempty"`

	// AI-generated metadata (summary-based search, populated in Phase 2)
	Summary   *string   `json:"summary,omitempty"`   // 2-4 sentence description
	Keywords  *string   `json:"keywords,omitempty"`  // JSON array: ["tag1", "tag2"]
	Entities  *string   `json:"entities,omitempty"`  // JSON array: ["person", "company"]
	RAGHints  *string   `json:"rag_hints,omitempty"` // JSON array: ["what questions?"]
	Embedding []float32 `json:"embedding,omitempty"` // Summary embedding for vector search

	// Timestamps
	CreatedAt   time.Time  `json:"created_at"`
	ProcessedAt *time.Time `json:"processed_at"`

	// Usage tracking (v0.6.0)
	ContextLoadCount int        `json:"context_load_count"` // How many times loaded into LLM context
	LastLoadedAt     *time.Time `json:"last_loaded_at"`     // Last time loaded

	// User context (v0.6.0) - text of message(s) when file was sent
	UserContext *string `json:"user_context,omitempty"`
}

type Storage interface {
	MessageRepository
	UserRepository
	StatsRepository
	TopicRepository
	MemoryBankRepository
	FactRepository
	FactHistoryRepository
	MaintenanceRepository
	AgentLogRepository
	PeopleRepository
	ArtifactRepository
}

type SQLiteStore struct {
	db     *sql.DB
	logger *slog.Logger
	dbPath string // Original path without query params, for file size check
}

func NewSQLiteStore(logger *slog.Logger, path string) (*SQLiteStore, error) {
	// Save original path for file operations (before adding query params)
	originalPath := path
	if idx := strings.Index(path, "?"); idx != -1 {
		originalPath = path[:idx]
	}

	// Note: modernc.org/sqlite doesn't support _journal_mode query param,
	// so we set it via PRAGMA after opening the connection
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	// Set max open connections to 1 to avoid "database is locked" errors
	// even with WAL mode, as modernc.org/sqlite might have issues with concurrent writes.
	// For a personal bot, 1 connection is sufficient.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		return nil, err
	}

	// Set WAL mode explicitly - the _journal_mode query param doesn't work with modernc.org/sqlite
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(&journalMode); err != nil {
		logger.Warn("failed to set WAL journal mode", "error", err)
	} else {
		logger.Info("SQLite journal mode set", "mode", journalMode, "path", originalPath)
	}

	// Set busy timeout explicitly as well
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		logger.Warn("failed to set busy timeout", "error", err)
	}

	return &SQLiteStore{db: db, logger: logger, dbPath: originalPath}, nil
}

func (s *SQLiteStore) Init() error {
	query := `
	CREATE TABLE IF NOT EXISTS history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS stats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		tokens_used INTEGER NOT NULL,
		cost_usd REAL NOT NULL,
		recorded_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS rag_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		original_query TEXT,
		enriched_query TEXT,
		enrichment_prompt TEXT,
		context_used TEXT,
		system_prompt TEXT,
		retrieval_results TEXT,
		llm_response TEXT,
		enrichment_tokens INTEGER,
		generation_tokens INTEGER,
		total_cost_usd REAL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS topics (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		summary TEXT,
		start_msg_id INTEGER NOT NULL,
		end_msg_id INTEGER NOT NULL,
		embedding BLOB,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_history_user_id ON history(user_id);
	CREATE INDEX IF NOT EXISTS idx_topics_user_id ON topics(user_id);
	
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY,
		username TEXT,
		first_name TEXT,
		last_name TEXT,
		last_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS memory_bank (
		user_id INTEGER PRIMARY KEY,
		content TEXT,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS structured_facts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		relation TEXT NOT NULL,
		category TEXT NOT NULL,
		content TEXT NOT NULL,
		type TEXT NOT NULL,
		importance INTEGER NOT NULL,
		embedding BLOB,
		topic_id INTEGER,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		last_updated TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(user_id, relation, content)
	);

	CREATE INDEX IF NOT EXISTS idx_structured_facts_category ON structured_facts(user_id, category);
	CREATE INDEX IF NOT EXISTS idx_structured_facts_type ON structured_facts(user_id, type);

	CREATE TABLE IF NOT EXISTS fact_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		fact_id INTEGER NOT NULL,
		user_id INTEGER NOT NULL,
		action TEXT NOT NULL,
		old_content TEXT,
		new_content TEXT,
		reason TEXT,
		category TEXT,
		importance INTEGER DEFAULT 0,
		topic_id INTEGER,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_fact_history_user_id ON fact_history(user_id);
	CREATE INDEX IF NOT EXISTS idx_fact_history_fact_id ON fact_history(fact_id);
	`
	if _, err := s.db.Exec(query); err != nil {
		return err
	}

	// Run migrations
	runner := migrations.NewRunner(s.db, s.logger)
	if err := runner.Run(); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	return nil
}

// CheckpointResult contains the result of a WAL checkpoint operation.
type CheckpointResult struct {
	Busy         int // 0 = success, 1 = blocked by reader
	Log          int // Total frames in WAL file
	Checkpointed int // Frames actually checkpointed
}

// Checkpoint forces a WAL checkpoint to flush all pending writes to the main database file.
// This is useful before shutdown or after critical writes to ensure data persistence.
// Returns CheckpointResult with details about what was checkpointed.
func (s *SQLiteStore) Checkpoint() error {
	var busy, log, checkpointed int
	err := s.db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed)
	if err != nil {
		return fmt.Errorf("checkpoint query failed: %w", err)
	}

	s.logger.Info("WAL checkpoint result",
		"busy", busy,
		"log_frames", log,
		"checkpointed_frames", checkpointed,
	)

	if busy != 0 {
		return fmt.Errorf("checkpoint blocked by reader (busy=%d)", busy)
	}
	if log > 0 && checkpointed < log {
		return fmt.Errorf("incomplete checkpoint: %d/%d frames", checkpointed, log)
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	// Checkpoint WAL to ensure all writes are flushed to main database
	if err := s.Checkpoint(); err != nil {
		s.logger.Warn("failed to checkpoint WAL before close", "error", err)
	}
	return s.db.Close()
}

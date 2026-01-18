package storage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "modernc.org/sqlite"
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

	// Migrations
	if err := s.migrate(); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	return nil
}

func (s *SQLiteStore) migrate() error {
	// Check if topic_id exists in history
	var count int
	err := s.db.QueryRow("SELECT count(*) FROM pragma_table_info('history') WHERE name='topic_id'").Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		s.logger.Info("migrating history table: adding topic_id")
		if _, err := s.db.Exec("ALTER TABLE history ADD COLUMN topic_id INTEGER REFERENCES topics(id)"); err != nil {
			return err
		}
		// PERF: This index is critical for GetTopicsExtended() which joins history to count messages per topic.
		// Without it, queries with message_count sorting become O(n) table scans.
		if _, err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_history_topic_id ON history(topic_id)"); err != nil {
			return err
		}
	}

	// Check if facts_extracted exists in topics
	err = s.db.QueryRow("SELECT count(*) FROM pragma_table_info('topics') WHERE name='facts_extracted'").Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		s.logger.Info("migrating topics table: adding facts_extracted")
		if _, err := s.db.Exec("ALTER TABLE topics ADD COLUMN facts_extracted BOOLEAN DEFAULT 0"); err != nil {
			return err
		}
	}

	// Check if is_consolidated exists in topics
	err = s.db.QueryRow("SELECT count(*) FROM pragma_table_info('topics') WHERE name='is_consolidated'").Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		s.logger.Info("migrating topics table: adding is_consolidated")
		if _, err := s.db.Exec("ALTER TABLE topics ADD COLUMN is_consolidated BOOLEAN DEFAULT 0"); err != nil {
			return err
		}
	}

	// Check if consolidation_checked exists in topics
	err = s.db.QueryRow("SELECT count(*) FROM pragma_table_info('topics') WHERE name='consolidation_checked'").Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		s.logger.Info("migrating topics table: adding consolidation_checked")
		if _, err := s.db.Exec("ALTER TABLE topics ADD COLUMN consolidation_checked BOOLEAN DEFAULT 0"); err != nil {
			return err
		}
	}

	// Check if topic_id exists in structured_facts
	err = s.db.QueryRow("SELECT count(*) FROM pragma_table_info('structured_facts') WHERE name='topic_id'").Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		s.logger.Info("migrating structured_facts table: adding topic_id")
		if _, err := s.db.Exec("ALTER TABLE structured_facts ADD COLUMN topic_id INTEGER"); err != nil {
			return err
		}
	}

	// Check if topic_id exists in fact_history
	err = s.db.QueryRow("SELECT count(*) FROM pragma_table_info('fact_history') WHERE name='topic_id'").Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		s.logger.Info("migrating fact_history table: adding topic_id, category, importance")
		if _, err := s.db.Exec("ALTER TABLE fact_history ADD COLUMN topic_id INTEGER"); err != nil {
			return err
		}
		if _, err := s.db.Exec("ALTER TABLE fact_history ADD COLUMN category TEXT"); err != nil {
			return err
		}
		if _, err := s.db.Exec("ALTER TABLE fact_history ADD COLUMN importance INTEGER DEFAULT 0"); err != nil {
			return err
		}
	}

	// Check if relation exists in fact_history
	err = s.db.QueryRow("SELECT count(*) FROM pragma_table_info('fact_history') WHERE name='relation'").Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		s.logger.Info("migrating fact_history table: adding relation")
		if _, err := s.db.Exec("ALTER TABLE fact_history ADD COLUMN relation TEXT"); err != nil {
			return err
		}
	}

	// Check if request_input exists in fact_history
	err = s.db.QueryRow("SELECT count(*) FROM pragma_table_info('fact_history') WHERE name='request_input'").Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		s.logger.Info("migrating fact_history table: adding request_input")
		if _, err := s.db.Exec("ALTER TABLE fact_history ADD COLUMN request_input TEXT"); err != nil {
			return err
		}
	}

	// Ensure indexes exist (idempotent)
	if _, err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_structured_facts_topic_id ON structured_facts(topic_id)"); err != nil {
		return err
	}
	if _, err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_fact_history_topic_id ON fact_history(topic_id)"); err != nil {
		return err
	}

	// Backfill topic_id for existing topics
	// We do this if we have topics but history has null topic_ids
	// This is a simple heuristic: if we have topics, run the update.
	// It's idempotent enough (re-updating same values).
	// Note: SQLite update with join/subquery
	// IMPORTANT: Filter by user_id to prevent cross-user contamination!
	backfillQuery := `
		UPDATE history
		SET topic_id = (
			SELECT id FROM topics
			WHERE history.user_id = topics.user_id
			AND history.id >= topics.start_msg_id
			AND history.id <= topics.end_msg_id
			LIMIT 1
		)
		WHERE topic_id IS NULL
		AND EXISTS (SELECT 1 FROM topics)
	`
	if _, err := s.db.Exec(backfillQuery); err != nil {
		s.logger.Warn("failed to backfill topic_id", "error", err)
		// Don't fail migration, maybe just no topics or complex query issue
	}

	// Check if size_chars exists in topics
	err = s.db.QueryRow("SELECT count(*) FROM pragma_table_info('topics') WHERE name='size_chars'").Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		s.logger.Info("migrating topics table: adding size_chars")
		if _, err := s.db.Exec("ALTER TABLE topics ADD COLUMN size_chars INTEGER DEFAULT 0"); err != nil {
			return err
		}
		// Backfill: calculate size from messages
		s.logger.Info("backfilling size_chars for existing topics...")
		backfillSizeQuery := `
			UPDATE topics SET size_chars = (
				SELECT COALESCE(SUM(LENGTH(content)), 0)
				FROM history
				WHERE topic_id = topics.id
			)
		`
		if _, err := s.db.Exec(backfillSizeQuery); err != nil {
			s.logger.Warn("failed to backfill size_chars", "error", err)
		}
	}

	// Create reranker_logs table if not exists
	rerankerLogsQuery := `
		CREATE TABLE IF NOT EXISTS reranker_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			original_query TEXT,
			enriched_query TEXT,
			candidates_json TEXT,
			tool_calls_json TEXT,
			selected_ids_json TEXT,
			reasoning_json TEXT,
			fallback_reason TEXT,
			duration_enrichment_ms INTEGER,
			duration_vector_ms INTEGER,
			duration_reranker_ms INTEGER,
			duration_total_ms INTEGER,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_reranker_logs_user_id ON reranker_logs(user_id);
		CREATE INDEX IF NOT EXISTS idx_reranker_logs_created_at ON reranker_logs(created_at DESC);
	`
	if _, err := s.db.Exec(rerankerLogsQuery); err != nil {
		return fmt.Errorf("failed to create reranker_logs table: %w", err)
	}

	// Migration: add reasoning_json column if missing
	var reasoningColExists int
	err = s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('reranker_logs') WHERE name='reasoning_json'`).Scan(&reasoningColExists)
	if err == nil && reasoningColExists == 0 {
		if _, err := s.db.Exec(`ALTER TABLE reranker_logs ADD COLUMN reasoning_json TEXT`); err != nil {
			s.logger.Warn("failed to add reasoning_json column", "error", err)
		}
	}

	// Create agent_logs table for unified agent debugging
	agentLogsQuery := `
		CREATE TABLE IF NOT EXISTS agent_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			agent_type TEXT NOT NULL,
			input_prompt TEXT,
			input_context TEXT,
			output_response TEXT,
			output_parsed TEXT,
			output_context TEXT,
			model TEXT,
			prompt_tokens INTEGER,
			completion_tokens INTEGER,
			total_cost REAL,
			duration_ms INTEGER,
			metadata TEXT,
			success BOOLEAN DEFAULT 1,
			error_message TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_agent_logs_user_id ON agent_logs(user_id);
		CREATE INDEX IF NOT EXISTS idx_agent_logs_agent_type ON agent_logs(agent_type);
		CREATE INDEX IF NOT EXISTS idx_agent_logs_created_at ON agent_logs(created_at DESC);
	`
	if _, err := s.db.Exec(agentLogsQuery); err != nil {
		return fmt.Errorf("failed to create agent_logs table: %w", err)
	}

	// Add output_context column if it doesn't exist (migration for existing databases)
	var outputContextColExists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agent_logs') WHERE name='output_context'`).Scan(&outputContextColExists); err == nil && outputContextColExists == 0 {
		if _, err := s.db.Exec(`ALTER TABLE agent_logs ADD COLUMN output_context TEXT`); err != nil {
			s.logger.Warn("failed to add output_context column", "error", err)
		}
	}

	// Add conversation_turns column for multi-turn agent logs (e.g., Reranker with tool calls)
	var convTurnsColExists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agent_logs') WHERE name='conversation_turns'`).Scan(&convTurnsColExists); err == nil && convTurnsColExists == 0 {
		s.logger.Info("migrating agent_logs table: adding conversation_turns")
		if _, err := s.db.Exec(`ALTER TABLE agent_logs ADD COLUMN conversation_turns TEXT`); err != nil {
			s.logger.Warn("failed to add conversation_turns column", "error", err)
		}
	}

	// Create people table for social graph (v0.5.1)
	peopleTableQuery := `
		CREATE TABLE IF NOT EXISTS people (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			display_name TEXT NOT NULL,
			aliases TEXT DEFAULT '[]',
			telegram_id INTEGER,
			username TEXT,
			circle TEXT DEFAULT 'Other',
			bio TEXT,
			embedding BLOB,
			first_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			last_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			mention_count INTEGER DEFAULT 1,
			UNIQUE(user_id, display_name)
		);
		CREATE INDEX IF NOT EXISTS idx_people_user_id ON people(user_id);
		CREATE INDEX IF NOT EXISTS idx_people_telegram_id ON people(user_id, telegram_id);
		CREATE INDEX IF NOT EXISTS idx_people_username ON people(user_id, username);
		CREATE INDEX IF NOT EXISTS idx_people_circle ON people(user_id, circle);
	`
	if _, err := s.db.Exec(peopleTableQuery); err != nil {
		return fmt.Errorf("failed to create people table: %w", err)
	}

	// Drop legacy facts table (replaced by structured_facts)
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS facts`); err != nil {
		s.logger.Warn("failed to drop legacy facts table", "error", err)
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

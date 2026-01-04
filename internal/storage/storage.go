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
	Entity      string
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
	Entity       string
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

type RAGLog struct {
	ID               int64
	UserID           int64
	OriginalQuery    string
	EnrichedQuery    string
	EnrichmentPrompt string
	ContextUsed      string // The full context sent to the generation model
	SystemPrompt     string
	RetrievalResults string // JSON array of retrieved messages
	LLMResponse      string
	EnrichmentTokens int
	GenerationTokens int
	TotalCostUSD     float64
	CreatedAt        time.Time
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

// RerankerLog stores debug traces from the reranker component
type RerankerLog struct {
	ID                   int64
	UserID               int64
	OriginalQuery        string
	EnrichedQuery        string
	CandidatesJSON       string  // JSON array of candidates with scores
	ToolCallsJSON        string  // JSON array of tool call iterations
	SelectedIDsJSON      string  // JSON array of selected topic IDs
	FallbackReason       *string // nil if no fallback, otherwise reason
	DurationEnrichmentMs int
	DurationVectorMs     int
	DurationRerankerMs   int
	DurationTotalMs      int
	CreatedAt            time.Time
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

type Storage interface {
	MessageRepository
	UserRepository
	StatsRepository
	LogRepository
	TopicRepository
	MemoryBankRepository
	FactRepository
	FactHistoryRepository
	MaintenanceRepository
	RerankerLogRepository
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

	// Add connection parameters for better concurrency
	if !strings.Contains(path, "?") {
		path += "?"
	} else {
		path += "&"
	}
	path += "_busy_timeout=5000&_journal_mode=WAL"

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

	CREATE TABLE IF NOT EXISTS facts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		content TEXT NOT NULL,
		embedding BLOB,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_facts_user_id ON facts(user_id);

	CREATE TABLE IF NOT EXISTS structured_facts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		entity TEXT NOT NULL,
		relation TEXT NOT NULL,
		category TEXT NOT NULL,
		content TEXT NOT NULL,
		type TEXT NOT NULL,
		importance INTEGER NOT NULL,
		embedding BLOB,
		topic_id INTEGER,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		last_updated TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(user_id, entity, relation, content)
	);

	CREATE INDEX IF NOT EXISTS idx_structured_facts_user_entity ON structured_facts(user_id, entity);
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
		entity TEXT,
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
		s.logger.Info("migrating fact_history table: adding topic_id, category, entity, importance")
		if _, err := s.db.Exec("ALTER TABLE fact_history ADD COLUMN topic_id INTEGER"); err != nil {
			return err
		}
		if _, err := s.db.Exec("ALTER TABLE fact_history ADD COLUMN category TEXT"); err != nil {
			return err
		}
		if _, err := s.db.Exec("ALTER TABLE fact_history ADD COLUMN entity TEXT"); err != nil {
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
	backfillQuery := `
		UPDATE history
		SET topic_id = (
			SELECT id FROM topics
			WHERE history.id >= topics.start_msg_id
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

	return nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

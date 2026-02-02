package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     1,
		Description: "Initial schema: users, history, topics, structured_facts",
		Up:          migrateInitial,
	})
}

func migrateInitial(tx *sql.Tx) error {
	// Create tables in order (some have foreign keys)

	// users table - Telegram user info
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY,
			username TEXT,
			first_name TEXT,
			last_name TEXT,
			last_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}

	// history table - all messages
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_history_user_id ON history(user_id)
	`); err != nil {
		return err
	}

	// stats table - token usage tracking
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS stats (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			tokens_used INTEGER NOT NULL,
			cost_usd REAL NOT NULL,
			recorded_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}

	// rag_logs table - RAG pipeline debugging
	if _, err := tx.Exec(`
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
		)
	`); err != nil {
		return err
	}

	// topics table - conversation summaries
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS topics (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			summary TEXT,
			start_msg_id INTEGER NOT NULL,
			end_msg_id INTEGER NOT NULL,
			embedding BLOB,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_topics_user_id ON topics(user_id)
	`); err != nil {
		return err
	}

	// memory_bank table - deprecated but kept for compatibility
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS memory_bank (
			user_id INTEGER PRIMARY KEY,
			content TEXT,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}

	// structured_facts table - extracted facts
	if _, err := tx.Exec(`
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
		CREATE INDEX IF NOT EXISTS idx_structured_facts_type ON structured_facts(user_id, type)
	`); err != nil {
		return err
	}

	// fact_history table - fact change tracking
	if _, err := tx.Exec(`
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
		CREATE INDEX IF NOT EXISTS idx_fact_history_fact_id ON fact_history(fact_id)
	`); err != nil {
		return err
	}

	return nil
}

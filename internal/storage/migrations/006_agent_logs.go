package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     6,
		Description: "Agent logs table for unified debugging",
		Up:          migrateAgentLogs,
	})
}

func migrateAgentLogs(tx *sql.Tx) error {
	query := `
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
		CREATE INDEX IF NOT EXISTS idx_agent_logs_created_at ON agent_logs(created_at DESC)
	`
	if _, err := tx.Exec(query); err != nil {
		return err
	}

	// Add output_context column if missing
	if !columnExists(tx, "agent_logs", "output_context") {
		if _, err := tx.Exec("ALTER TABLE agent_logs ADD COLUMN output_context TEXT"); err != nil {
			return err
		}
	}

	// Add conversation_turns column for multi-turn agent logs (e.g., Reranker with tool calls)
	if !columnExists(tx, "agent_logs", "conversation_turns") {
		if _, err := tx.Exec("ALTER TABLE agent_logs ADD COLUMN conversation_turns TEXT"); err != nil {
			return err
		}
	}

	return nil
}

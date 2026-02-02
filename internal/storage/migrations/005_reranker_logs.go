package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     5,
		Description: "Reranker logs table",
		Up:          migrateRerankerLogs,
	})
}

func migrateRerankerLogs(tx *sql.Tx) error {
	query := `
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
		CREATE INDEX IF NOT EXISTS idx_reranker_logs_created_at ON reranker_logs(created_at DESC)
	`
	if _, err := tx.Exec(query); err != nil {
		return err
	}

	// Add reasoning_json column if missing (for databases created before this migration)
	if !columnExists(tx, "reranker_logs", "reasoning_json") {
		if _, err := tx.Exec("ALTER TABLE reranker_logs ADD COLUMN reasoning_json TEXT"); err != nil {
			return err
		}
	}

	return nil
}

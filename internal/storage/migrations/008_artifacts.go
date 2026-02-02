package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     8,
		Description: "Artifacts system for file storage (v0.6.0)",
		Up:          migrateArtifacts,
	})
}

func migrateArtifacts(tx *sql.Tx) error {
	// Create artifacts table
	query := `
		CREATE TABLE IF NOT EXISTS artifacts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			message_id INTEGER NOT NULL,

			-- File metadata
			file_type TEXT NOT NULL,
			file_path TEXT NOT NULL,
			file_size INTEGER,
			mime_type TEXT,
			original_name TEXT,

			-- Deduplication
			content_hash TEXT NOT NULL,

			-- Processing status
			state TEXT NOT NULL DEFAULT 'pending',
			error_message TEXT,

			-- Retry tracking
			retry_count INTEGER DEFAULT 0,
			last_failed_at TIMESTAMP,

			-- AI-generated metadata (summary-based search)
			summary TEXT,
			keywords TEXT,
			entities TEXT,
			rag_hints TEXT,
			embedding BLOB,

			-- Timestamps
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			processed_at TIMESTAMP,

			-- Usage tracking
			context_load_count INTEGER DEFAULT 0,
			last_loaded_at TIMESTAMP,

			-- User context
			user_context TEXT,

			-- Constraints
			UNIQUE(user_id, content_hash),
			FOREIGN KEY (user_id) REFERENCES users(id),
			FOREIGN KEY (message_id) REFERENCES history(id)
		);
		CREATE INDEX IF NOT EXISTS idx_artifacts_user_id ON artifacts(user_id);
		CREATE INDEX IF NOT EXISTS idx_artifacts_status ON artifacts(user_id, state);
		CREATE INDEX IF NOT EXISTS idx_artifacts_hash ON artifacts(user_id, content_hash);
		CREATE INDEX IF NOT EXISTS idx_artifacts_message_id ON artifacts(user_id, message_id);
		CREATE INDEX IF NOT EXISTS idx_artifacts_type ON artifacts(user_id, file_type)
	`
	if _, err := tx.Exec(query); err != nil {
		return err
	}

	// Drop old artifact_chunks table if exists (v0.6.0 cleanup)
	exec(tx, "DROP TABLE IF EXISTS artifact_chunks")

	// Add entities column if missing (for databases created before metadata columns)
	if !columnExists(tx, "artifacts", "entities") {
		if _, err := tx.Exec("ALTER TABLE artifacts ADD COLUMN entities TEXT"); err != nil {
			return err
		}
	}

	// Add rag_hints column if missing
	if !columnExists(tx, "artifacts", "rag_hints") {
		if _, err := tx.Exec("ALTER TABLE artifacts ADD COLUMN rag_hints TEXT"); err != nil {
			return err
		}
	}

	// Add embedding column if missing
	if !columnExists(tx, "artifacts", "embedding") {
		if _, err := tx.Exec("ALTER TABLE artifacts ADD COLUMN embedding BLOB"); err != nil {
			return err
		}
	}

	// Add retry_count column if missing
	if !columnExists(tx, "artifacts", "retry_count") {
		if _, err := tx.Exec("ALTER TABLE artifacts ADD COLUMN retry_count INTEGER DEFAULT 0"); err != nil {
			return err
		}
	}

	// Add last_failed_at column if missing
	if !columnExists(tx, "artifacts", "last_failed_at") {
		if _, err := tx.Exec("ALTER TABLE artifacts ADD COLUMN last_failed_at TIMESTAMP"); err != nil {
			return err
		}
	}

	// Add context_load_count column if missing
	if !columnExists(tx, "artifacts", "context_load_count") {
		if _, err := tx.Exec("ALTER TABLE artifacts ADD COLUMN context_load_count INTEGER DEFAULT 0"); err != nil {
			return err
		}
	}

	// Add last_loaded_at column if missing
	if !columnExists(tx, "artifacts", "last_loaded_at") {
		if _, err := tx.Exec("ALTER TABLE artifacts ADD COLUMN last_loaded_at TIMESTAMP"); err != nil {
			return err
		}
	}

	// Add user_context column if missing
	if !columnExists(tx, "artifacts", "user_context") {
		if _, err := tx.Exec("ALTER TABLE artifacts ADD COLUMN user_context TEXT"); err != nil {
			return err
		}
	}

	return nil
}

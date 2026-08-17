package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     20,
		Description: "Ordered history-to-artifact delivery references",
		Up:          migrateHistoryArtifactRefs,
	})
}

// migrateHistoryArtifactRefs adds an M:N association between logical replies
// and delivered artifacts. artifacts.message_id remains the canonical creator
// link; resending an old artifact inserts a row here instead of rebinding it.
func migrateHistoryArtifactRefs(tx *sql.Tx) error {
	_, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS history_artifact_refs (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			history_id  INTEGER NOT NULL,
			user_id     TEXT NOT NULL,
			artifact_id INTEGER NOT NULL,
			ordinal     INTEGER NOT NULL CHECK (ordinal >= 0),
			mode        TEXT NOT NULL CHECK (mode IN ('auto', 'preview', 'original', 'preview_and_original')),
			source_kind TEXT NOT NULL CHECK (source_kind IN ('generated', 'stored')),
			created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(history_id, ordinal)
		);
		CREATE INDEX IF NOT EXISTS idx_history_artifact_refs_history
			ON history_artifact_refs(user_id, history_id, ordinal);
		CREATE INDEX IF NOT EXISTS idx_history_artifact_refs_artifact
			ON history_artifact_refs(user_id, artifact_id, history_id)
	`)
	return err
}

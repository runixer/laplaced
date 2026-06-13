package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     16,
		Description: "Response flags: history.trace_id + response_flags table for user-flagged bad replies",
		Up:          migrateResponseFlags,
	})
}

// migrateResponseFlags supports the "user flags a bad bot reply via reaction"
// feature. Two additive changes:
//
//   - history.trace_id : the trace that produced an assistant reply, set when
//     the reply is stored. Lets an inbound reaction on that reply (matched by
//     transport message_id) resolve straight to its trace. NULL on user rows and
//     on replies stored before this migration.
//   - response_flags   : one row per reaction a user adds to a bot reply,
//     carrying the resolved trace_id so the operator (root-cause skill) can jump
//     directly to the trace. reply_preview is a truncated copy of the reply for
//     self-contained listing; it is real conversation content, so it lives only
//     in the gitignored DB.
func migrateResponseFlags(tx *sql.Tx) error {
	if tableExists(tx, "history") && !columnExists(tx, "history", "trace_id") {
		if _, err := tx.Exec("ALTER TABLE history ADD COLUMN trace_id TEXT"); err != nil {
			return err
		}
	}

	query := `
		CREATE TABLE IF NOT EXISTS response_flags (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id       TEXT NOT NULL,
			history_id    INTEGER,
			message_id    TEXT,
			trace_id      TEXT,
			emoji         TEXT,
			reply_preview TEXT,
			created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_response_flags_user_id ON response_flags(user_id);
		CREATE INDEX IF NOT EXISTS idx_response_flags_created_at ON response_flags(created_at DESC)
	`
	_, err := tx.Exec(query)
	return err
}

package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     10,
		Description: "Scopes table: maps (transport, native_id) to internal user_id (v0.10)",
		Up:          migrateScopes,
	})
}

// migrateScopes introduces the identity-resolution seam for multi-transport
// support (v0.10). The schema partition key (`user_id int64`) is treated as a
// scope_id: the memory tenant. A scope is usually a person (DM), but the
// scope_type column is laid in now to allow channels later without a schema
// rewrite.
//
// Telegram resolution is identity passthrough (internalID == telegramID) and
// writes NO row here — the home instance never gets scopes rows. Mattermost/
// Time users have 26-char string ids, so the scopes table mints a surrogate
// int64 internal_id per (transport, native_id).
func migrateScopes(tx *sql.Tx) error {
	query := `
		CREATE TABLE IF NOT EXISTS scopes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transport TEXT NOT NULL,
			scope_type TEXT NOT NULL DEFAULT 'user',
			native_id TEXT NOT NULL,
			internal_id INTEGER NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			last_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(transport, native_id)
		);
		CREATE INDEX IF NOT EXISTS idx_scopes_internal_id ON scopes(internal_id)
	`
	_, err := tx.Exec(query)
	return err
}

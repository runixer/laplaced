package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     13,
		Description: "Nullable history.thread_root for channel thread-reply detection (Phase 6)",
		Up:          migrateHistoryThreadRoot,
	})
}

// migrateHistoryThreadRoot adds the nullable thread_root column to history.
//
// It records, for a channel message, the transport thread it belongs to (MM
// root_id, collapsing to the post's own id for a top-level post). The bot stores
// it on its own replies so a later reply into the same thread can be recognised
// as addressed-to-the-bot without an explicit @mention (channel thread-reply
// gating). NULL for existing rows and for DM/Telegram paths — purely additive,
// no Telegram-path behavior change.
func migrateHistoryThreadRoot(tx *sql.Tx) error {
	if !tableExists(tx, "history") {
		return nil
	}
	if columnExists(tx, "history", "thread_root") {
		return nil
	}
	_, err := tx.Exec("ALTER TABLE history ADD COLUMN thread_root TEXT")
	return err
}

package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     4,
		Description: "Topic size tracking with backfill",
		Up:          migrateSizeTracking,
	})
}

func migrateSizeTracking(tx *sql.Tx) error {
	// Add size_chars to topics
	if !columnExists(tx, "topics", "size_chars") {
		if _, err := tx.Exec("ALTER TABLE topics ADD COLUMN size_chars INTEGER DEFAULT 0"); err != nil {
			return err
		}

		// Backfill: calculate size from messages
		// Note: This is a best-effort backfill, don't fail on errors
		exec(tx, `
			UPDATE topics SET size_chars = (
				SELECT COALESCE(SUM(LENGTH(content)), 0)
				FROM history
				WHERE topic_id = topics.id
			)
		`)
	}

	return nil
}

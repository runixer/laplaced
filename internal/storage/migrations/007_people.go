package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     7,
		Description: "People table for social graph (v0.5.1)",
		Up:          migratePeople,
	})
}

func migratePeople(tx *sql.Tx) error {
	query := `
		CREATE TABLE IF NOT EXISTS people (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			display_name TEXT NOT NULL,
			aliases TEXT DEFAULT '[]',
			telegram_id INTEGER,
			username TEXT,
			circle TEXT DEFAULT 'Other',
			bio TEXT,
			embedding BLOB,
			first_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			last_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			mention_count INTEGER DEFAULT 1,
			UNIQUE(user_id, display_name)
		);
		CREATE INDEX IF NOT EXISTS idx_people_user_id ON people(user_id);
		CREATE INDEX IF NOT EXISTS idx_people_telegram_id ON people(user_id, telegram_id);
		CREATE INDEX IF NOT EXISTS idx_people_username ON people(user_id, username);
		CREATE INDEX IF NOT EXISTS idx_people_circle ON people(user_id, circle)
	`
	if _, err := tx.Exec(query); err != nil {
		return err
	}

	// Drop legacy facts table (replaced by structured_facts)
	// This is safe to run multiple times
	exec(tx, "DROP TABLE IF EXISTS facts")

	return nil
}

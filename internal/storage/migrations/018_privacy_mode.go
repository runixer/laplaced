package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     18,
		Description: "Do-not-store privacy mode: history.do_not_store + users.privacy_mode",
		Up:          migratePrivacyMode,
	})
}

// migratePrivacyMode supports the user-facing "don't save this" request.
// Two additive flags:
//
//   - history.do_not_store : set on rows written while the scope's privacy
//     mode is on. Such rows stay in raw history for short-term session
//     context, but the topic pipeline redacts their content and
//     GetMessagesByTopicID filters them out, so they never enter topics,
//     facts, or embeddings.
//   - users.privacy_mode   : the per-scope toggle, flipped by the
//     privacy_mode tool and auto-reset when the session is archived.
func migratePrivacyMode(tx *sql.Tx) error {
	if tableExists(tx, "history") && !columnExists(tx, "history", "do_not_store") {
		if _, err := tx.Exec("ALTER TABLE history ADD COLUMN do_not_store BOOLEAN NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	if tableExists(tx, "users") && !columnExists(tx, "users", "privacy_mode") {
		if _, err := tx.Exec("ALTER TABLE users ADD COLUMN privacy_mode BOOLEAN NOT NULL DEFAULT 0"); err != nil {
			return err
		}
	}
	return nil
}

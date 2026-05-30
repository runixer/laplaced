package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     11,
		Description: "Generalize Person external id to (transport, native_id) (v0.10)",
		Up:          migratePersonExternalID,
	})
}

// migratePersonExternalID generalizes the people social-graph external id from
// the Telegram-only `telegram_id` to a transport-neutral pair
// (external_transport, external_id). Additive and backward compatible:
//
//   - The legacy `telegram_id` column stays in place and remains the fast-path
//     for @mention matching on the Telegram/home path (no read-site changes).
//   - Existing rows are backfilled to ('telegram', telegram_id) so the new
//     finder works uniformly across transports.
//   - Non-Telegram people (e.g. Mattermost channel participants, later) store
//     their native id here without overloading telegram_id.
func migratePersonExternalID(tx *sql.Tx) error {
	if !tableExists(tx, "people") {
		return nil
	}

	if !columnExists(tx, "people", "external_transport") {
		if _, err := tx.Exec("ALTER TABLE people ADD COLUMN external_transport TEXT"); err != nil {
			return err
		}
	}
	if !columnExists(tx, "people", "external_id") {
		if _, err := tx.Exec("ALTER TABLE people ADD COLUMN external_id TEXT"); err != nil {
			return err
		}
	}

	// Backfill existing Telegram people. Idempotent: only fills rows not yet set.
	if _, err := tx.Exec(`
		UPDATE people
		SET external_transport = 'telegram',
		    external_id = CAST(telegram_id AS TEXT)
		WHERE telegram_id IS NOT NULL AND external_transport IS NULL
	`); err != nil {
		return err
	}

	if _, err := tx.Exec(
		"CREATE INDEX IF NOT EXISTS idx_people_external_id ON people(user_id, external_transport, external_id)",
	); err != nil {
		return err
	}
	return nil
}

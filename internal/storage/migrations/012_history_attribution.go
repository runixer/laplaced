package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     12,
		Description: "Nullable history attribution columns for multi-participant transports (v0.10)",
		Up:          migrateHistoryAttribution,
	})
}

// migrateHistoryAttribution adds nullable attribution columns to history,
// forward-design for multi-participant conversations (channels) and for
// transports that expose stable message ids (edits, reactions, threading).
//
// All columns are NULL for existing rows and for the single-user Telegram DM
// and Mattermost DM PoC paths, so this is purely additive — no behavior change
// on the Telegram path:
//
//   - author          : display/handle of the message author within the scope
//     (NULL in DMs where the scope IS the author)
//   - message_id       : transport-native message/post id (string)
//   - conversation_id  : transport-native chat/channel id (string)
func migrateHistoryAttribution(tx *sql.Tx) error {
	if !tableExists(tx, "history") {
		return nil
	}
	for _, col := range []string{"author", "message_id", "conversation_id"} {
		if columnExists(tx, "history", col) {
			continue
		}
		if _, err := tx.Exec("ALTER TABLE history ADD COLUMN " + col + " TEXT"); err != nil {
			return err
		}
	}
	return nil
}

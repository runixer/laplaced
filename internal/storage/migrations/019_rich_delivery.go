package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     19,
		Description: "Rich delivery transport mappings and content-free delivery ledger",
		Up:          migrateRichDelivery,
	})
}

// migrateRichDelivery adds the durable identity/state needed by multi-part
// persistent replies. The ledger deliberately stores no response body, prompt,
// media URL/path, or error text: only bounded state, transport identifiers, and
// counts needed to avoid replaying an operation whose result is unknown.
func migrateRichDelivery(tx *sql.Tx) error {
	query := `
		CREATE TABLE IF NOT EXISTS history_transport_messages (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			history_id      INTEGER NOT NULL,
			user_id         TEXT NOT NULL,
			transport       TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			message_id      TEXT NOT NULL,
			ordinal         INTEGER NOT NULL,
			is_primary      BOOLEAN NOT NULL DEFAULT 0,
			created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(user_id, transport, conversation_id, message_id),
			UNIQUE(history_id, transport, conversation_id, ordinal)
		);
		CREATE INDEX IF NOT EXISTS idx_history_transport_messages_history
			ON history_transport_messages(history_id);
		CREATE INDEX IF NOT EXISTS idx_history_transport_messages_lookup
			ON history_transport_messages(user_id, transport, conversation_id, message_id);

		CREATE TABLE IF NOT EXISTS outbound_deliveries (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id         TEXT NOT NULL,
			transport       TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			trace_id        TEXT,
			history_id      INTEGER,
			status          TEXT NOT NULL,
			operation_count INTEGER NOT NULL,
			confirmed_count INTEGER NOT NULL DEFAULT 0,
			created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_outbound_deliveries_user
			ON outbound_deliveries(user_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_outbound_deliveries_status
			ON outbound_deliveries(status);

		CREATE TABLE IF NOT EXISTS outbound_delivery_ops (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			delivery_id INTEGER NOT NULL,
			ordinal     INTEGER NOT NULL,
			kind        TEXT NOT NULL,
			status      TEXT NOT NULL,
			error_class TEXT,
			started_at  TIMESTAMP,
			finished_at TIMESTAMP,
			created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(delivery_id, ordinal)
		);
		CREATE INDEX IF NOT EXISTS idx_outbound_delivery_ops_delivery
			ON outbound_delivery_ops(delivery_id, ordinal);
		CREATE INDEX IF NOT EXISTS idx_outbound_delivery_ops_status
			ON outbound_delivery_ops(status);

		CREATE TABLE IF NOT EXISTS outbound_delivery_messages (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			delivery_id INTEGER NOT NULL,
			op_id       INTEGER NOT NULL,
			message_id  TEXT NOT NULL,
			ordinal     INTEGER NOT NULL,
			created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(delivery_id, message_id),
			UNIQUE(op_id, ordinal)
		);
		CREATE INDEX IF NOT EXISTS idx_outbound_delivery_messages_delivery
			ON outbound_delivery_messages(delivery_id, ordinal);
		CREATE INDEX IF NOT EXISTS idx_outbound_delivery_messages_op
			ON outbound_delivery_messages(op_id, ordinal)
	`
	_, err := tx.Exec(query)
	return err
}

package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     14,
		Description: "Principal identity model: normalized scopes/identities/principals/channels",
		Up:          migratePrincipalModel,
	})
}

// migratePrincipalModel finalizes the principal identity schema. It
// supersedes the transitional `scopes` table from migration 010, which conflated
// the handle→scope map with the scope_type registry, by splitting it into four
// normalized tables:
//
//   - scopes      — thin registry of memory tenants (id = partition key,
//     scope_type ∈ 'user' | 'channel' | 'principal').
//   - identities  — (transport, native_id) → scope_id map. Many identities may
//     point at one principal scope (unified cross-transport memory).
//   - principals  — AD-backed person details, dedup'd by object_guid (stable
//     anchor, nullable until a later lookup) then ad_login.
//   - channels    — channel scope details, keyed by (transport, native_id).
//
// The transitional table is DROPPED: the Telegram passthrough path never wrote a
// scopes row (passthrough), and the dev Postgres/Mattermost DB is droppable, so
// no data is lost. Passthrough scopes (Telegram, resolver-less transports)
// still write NO row here — their id is a deterministic uuidv5 and an absent
// scopes row means a DM/user scope (IsChannelScope → false).
func migratePrincipalModel(tx *sql.Tx) error {
	query := `
		DROP TABLE IF EXISTS scopes;

		CREATE TABLE IF NOT EXISTS scopes (
			id           TEXT PRIMARY KEY,
			scope_type   TEXT NOT NULL DEFAULT 'user',
			display_name TEXT,
			created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			last_seen    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS identities (
			transport  TEXT NOT NULL,
			native_id  TEXT NOT NULL,
			scope_id   TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			last_seen  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (transport, native_id)
		);
		CREATE INDEX IF NOT EXISTS idx_identities_scope_id ON identities(scope_id);

		CREATE TABLE IF NOT EXISTS principals (
			scope_id     TEXT PRIMARY KEY,
			object_guid  TEXT,
			ad_login     TEXT,
			email        TEXT,
			display_name TEXT,
			created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_principals_object_guid ON principals(object_guid) WHERE object_guid IS NOT NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_principals_ad_login ON principals(ad_login) WHERE ad_login IS NOT NULL;

		CREATE TABLE IF NOT EXISTS channels (
			scope_id     TEXT PRIMARY KEY,
			transport    TEXT NOT NULL,
			native_id    TEXT NOT NULL,
			display_name TEXT,
			created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			last_seen    TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(transport, native_id)
		)
	`
	_, err := tx.Exec(query)
	return err
}

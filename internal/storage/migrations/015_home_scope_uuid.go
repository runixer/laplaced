package migrations

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"

	"github.com/google/uuid"
)

func init() {
	Register(Migration{
		Version:     15,
		Description: "Rewrite Telegram int64 user_id partition keys to UUID scope ids",
		Up:          migrateHomeScopeUUID,
	})
}

// homeScopeNamespace MUST equal storage.scopeNamespace. It is duplicated here
// because the migrations package cannot import storage (storage imports
// migrations — that would be an import cycle). A consistency test in the storage
// package (TestMigration015NamespaceMatchesPassthrough) pins the two together so
// a drift fails the build: drift would re-key every Telegram scope and orphan all
// existing memory.
var homeScopeNamespace = uuid.MustParse("6f9c1d2e-7a3b-5c4d-8e1f-0a2b3c4d5e6f")

// PassthroughTelegramScope derives the deterministic UUID scope id for a Telegram
// native id, identically to storage.PassthroughScopeID("telegram", id). Exported
// so the storage-package consistency test can assert they agree.
func PassthroughTelegramScope(nativeID string) string {
	return uuid.NewSHA1(homeScopeNamespace, []byte("telegram:"+nativeID)).String()
}

// bareTelegramID matches a partition value that is still a raw Telegram int64
// (the legacy int64 shape). UUID scope ids contain hyphens and hex, so they
// never match — the rewrite is therefore idempotent and skips already-migrated
// rows.
var bareTelegramID = regexp.MustCompile(`^[0-9]+$`)

// scopePartitionColumns lists every (table, column) that holds the scope
// partition key on the SQLite schema. Entity ids (message/topic/fact/
// artifact ids, telegram_id, *_id references) are NOT here — only the tenant key.
var scopePartitionColumns = []struct{ table, col string }{
	{"history", "user_id"},
	{"stats", "user_id"},
	{"rag_logs", "user_id"},
	{"topics", "user_id"},
	{"memory_bank", "user_id"},
	{"structured_facts", "user_id"},
	{"fact_history", "user_id"},
	{"reranker_logs", "user_id"},
	{"agent_logs", "user_id"},
	{"people", "user_id"},
	{"artifacts", "user_id"},
	{"users", "id"},
}

// migrateHomeScopeUUID rewrites the existing int64 Telegram user_id
// partition keys into deterministic UUID scope ids (uuidv5("telegram:"+id)),
// matching what the running code now computes via PassthroughScopeID. Without
// this, queries after this migration (keyed by the UUID) would miss every pre-existing
// row and the bot would appear to have lost all memory.
//
// It first converts users.id and memory_bank.user_id from INTEGER PRIMARY KEY to
// TEXT PRIMARY KEY. Those two columns are strict rowid aliases — unlike the plain
// INTEGER (affinity) user_id columns, a rowid alias REJECTS a non-integer value
// ("datatype mismatch"), so a UUID scope cannot be stored (or rewritten) until
// the column is TEXT. The other partition columns take UUID text in place.
//
// Scope and assumptions (SQLite-only — Postgres is greenfield and never runs the
// migration chain):
//   - The SQLite backend is Telegram-only, so every numeric user_id is a
//     Telegram id and is rewritten with transport "telegram".
//   - Already-UUID values (a DB created or partially written after this migration) are
//     skipped, so the migration is idempotent and safe to re-run.
//   - Each distinct int64 maps to a distinct UUID, so PK/UNIQUE columns
//     (users.id, memory_bank.user_id, structured_facts UNIQUE) never collide.
//
// Empty databases (fresh installs, tests) are a no-op for the value rewrite, but
// still get the TEXT-PK conversion so subsequent UUID writes succeed.
func migrateHomeScopeUUID(tx *sql.Tx) error {
	// Step 1: widen the two rowid-alias partition columns to TEXT PK so they can
	// hold UUID scopes. Idempotent — skipped once already TEXT.
	if err := widenScopePKToText(tx, "users", "id", usersTextPKDDL); err != nil {
		return err
	}
	if err := widenScopePKToText(tx, "memory_bank", "user_id", memoryBankTextPKDDL); err != nil {
		return err
	}

	// Step 2: rewrite int64 → UUID across every partition column.
	var totalRewritten int
	for _, c := range scopePartitionColumns {
		olds, err := distinctScopeValues(tx, c.table, c.col)
		if err != nil {
			return fmt.Errorf("read distinct %s.%s: %w", c.table, c.col, err)
		}
		for _, old := range olds {
			if !bareTelegramID.MatchString(old) {
				continue // already a UUID (or empty) — skip
			}
			newID := PassthroughTelegramScope(old)
			// #nosec G202 -- table/col are internal constants from scopePartitionColumns, never user input
			res, err := tx.Exec(
				"UPDATE "+c.table+" SET "+c.col+" = ? WHERE "+c.col+" = ?", newID, old,
			)
			if err != nil {
				return fmt.Errorf("rewrite %s.%s %s: %w", c.table, c.col, old, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("rows affected for %s.%s: %w", c.table, c.col, err)
			}
			totalRewritten += int(n)
		}
	}
	if totalRewritten > 0 {
		// Best-effort visibility; the runner logs migration application separately.
		fmt.Printf("migration 015: rewrote %d row(s) from int64 to UUID scope ids\n", totalRewritten)
	}
	return nil
}

// usersTextPKDDL / memoryBankTextPKDDL recreate the two rowid-alias tables with a
// TEXT PRIMARY KEY. The non-key columns mirror the current schema exactly (no
// migration ever altered these two tables); CAST(... AS TEXT) turns the existing
// integer ids into their decimal-string form, which step 2 then rewrites to UUIDs.
const usersTextPKDDL = `
	CREATE TABLE users_textpk (
		id TEXT PRIMARY KEY,
		username TEXT,
		first_name TEXT,
		last_name TEXT,
		last_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	INSERT INTO users_textpk (id, username, first_name, last_name, last_seen)
		SELECT CAST(id AS TEXT), username, first_name, last_name, last_seen FROM users;
	DROP TABLE users;
	ALTER TABLE users_textpk RENAME TO users;
`

const memoryBankTextPKDDL = `
	CREATE TABLE memory_bank_textpk (
		user_id TEXT PRIMARY KEY,
		content TEXT,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	INSERT INTO memory_bank_textpk (user_id, content, updated_at)
		SELECT CAST(user_id AS TEXT), content, updated_at FROM memory_bank;
	DROP TABLE memory_bank;
	ALTER TABLE memory_bank_textpk RENAME TO memory_bank;
`

// widenScopePKToText rebuilds table so its partition column is TEXT PRIMARY KEY,
// unless it already is (idempotent). SQLite cannot ALTER a column type in place,
// so this is the standard create-copy-drop-rename rebuild.
func widenScopePKToText(tx *sql.Tx, table, col, ddl string) error {
	var colType string
	if err := tx.QueryRow(
		"SELECT type FROM pragma_table_info(?) WHERE name = ?", table, col,
	).Scan(&colType); err != nil {
		return fmt.Errorf("inspect %s.%s type: %w", table, col, err)
	}
	if colType == "TEXT" {
		return nil // already widened
	}
	if _, err := tx.Exec(ddl); err != nil {
		return fmt.Errorf("widen %s.%s to TEXT PK: %w", table, col, err)
	}
	return nil
}

// distinctScopeValues reads the distinct partition values as strings. The column
// has INTEGER affinity, so SQLite may return numeric scopes as int64 and UUID
// scopes as string/[]byte — normalize all three (mirrors storage.ScopeID.Scan).
func distinctScopeValues(tx *sql.Tx, table, col string) ([]string, error) {
	// #nosec G202 -- table/col are internal constants from scopePartitionColumns, never user input
	rows, err := tx.Query("SELECT DISTINCT " + col + " FROM " + table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var raw any
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		switch v := raw.(type) {
		case nil:
			continue
		case int64:
			out = append(out, strconv.FormatInt(v, 10))
		case string:
			out = append(out, v)
		case []byte:
			out = append(out, string(v))
		default:
			return nil, fmt.Errorf("unexpected %s.%s value type %T", table, col, raw)
		}
	}
	return out, rows.Err()
}

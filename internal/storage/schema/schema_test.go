// Package schema pins the SQLite migration end-state to the consolidated
// PostgreSQL schema in postgres.sql.
//
// The two schemas are maintained by hand in two places: SQLite is the
// bootstrap DDL in storage.initSQLite plus the incremental migration chain,
// while PostgreSQL is the single consolidated postgres.sql file (greenfield
// backend, no migration replay). Nothing ties them together at build time, so
// a migration that adds a column only on the SQLite side leaves Postgres
// silently behind. This test runs the full SQLite chain on a scratch database,
// parses postgres.sql, and fails on any table/column/type divergence.
package schema

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/runixer/laplaced/internal/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tableSchema maps lower-cased column name -> declared type (as written in DDL).
type tableSchema map[string]string

// sqliteOnlyTables exist on SQLite but intentionally have no Postgres
// counterpart.
var sqliteOnlyTables = map[string]bool{
	// Migration bookkeeping: Postgres is greenfield and never replays the
	// migration chain, so it has no schema_version table.
	"schema_version": true,
	// SQLite-internal AUTOINCREMENT bookkeeping.
	"sqlite_sequence": true,
}

// scopeColumns hold the memory-partition key: TEXT UUIDs on SQLite (some still
// declared INTEGER — migration 015 rewrote values in place and SQLite affinity
// tolerates that), native uuid on Postgres.
var scopeColumns = map[string]bool{
	"history.user_id":          true,
	"stats.user_id":            true,
	"rag_logs.user_id":         true,
	"topics.user_id":           true,
	"memory_bank.user_id":      true,
	"structured_facts.user_id": true,
	"fact_history.user_id":     true,
	"reranker_logs.user_id":    true,
	"agent_logs.user_id":       true,
	"people.user_id":           true,
	"artifacts.user_id":        true,
	"response_flags.user_id":   true,
	"users.id":                 true,
	"scopes.id":                true,
	"identities.scope_id":      true,
	"principals.scope_id":      true,
	"channels.scope_id":        true,
}

func TestSchemaParity_SQLiteVsPostgres(t *testing.T) {
	sqliteTables := sqliteEndStateSchema(t)
	pgTables := parsePostgresSchema(t)

	// Table sets must match (minus the documented SQLite-only tables).
	for name := range sqliteOnlyTables {
		delete(sqliteTables, name)
	}
	assert.ElementsMatch(t, keys(sqliteTables), keys(pgTables),
		"table sets diverged between SQLite migrations and postgres.sql")

	for name, sqliteCols := range sqliteTables {
		pgCols, ok := pgTables[name]
		if !ok {
			continue // already reported by ElementsMatch above
		}
		t.Run(name, func(t *testing.T) {
			assert.ElementsMatch(t, keys(sqliteCols), keys(pgCols),
				"column sets diverged for table %s", name)

			for col, sqliteType := range sqliteCols {
				pgType, ok := pgCols[col]
				if !ok {
					continue
				}
				assert.True(t, typesCompatible(name, col, sqliteType, pgType),
					"type mismatch for %s.%s: SQLite %q vs Postgres %q",
					name, col, sqliteType, pgType)
			}
		})
	}
}

// typesCompatible reports whether a SQLite declared type and a Postgres
// declared type are the same logical type under the mapping documented in the
// postgres.sql header.
func typesCompatible(table, col, sqliteType, pgType string) bool {
	st := strings.ToUpper(strings.TrimSpace(sqliteType))
	pt := strings.ToLower(strings.TrimSpace(pgType))

	if pt == "uuid" {
		// Only the known scope-partition columns may map to uuid.
		return scopeColumns[table+"."+col] && (st == "TEXT" || st == "INTEGER")
	}
	if scopeColumns[table+"."+col] {
		// A scope column must be uuid on Postgres, whatever SQLite declares.
		return false
	}

	switch pt {
	case "text":
		return st == "TEXT"
	case "integer", "bigint":
		// SQLite INTEGER is 64-bit; both Postgres widths are a faithful mapping.
		return st == "INTEGER" || st == "BIGINT"
	case "bytea":
		return st == "BLOB"
	case "double precision":
		return st == "REAL"
	case "boolean":
		return st == "BOOLEAN"
	case "timestamptz":
		return st == "TIMESTAMP" || st == "DATETIME"
	default:
		return false
	}
}

// sqliteEndStateSchema creates a scratch SQLite database, runs the bootstrap
// DDL plus the full migration chain, and returns the resulting schema.
func sqliteEndStateSchema(t *testing.T) map[string]tableSchema {
	t.Helper()

	path := filepath.Join(t.TempDir(), "schema.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	store, err := storage.NewSQLiteStore(logger, path)
	require.NoError(t, err, "open scratch SQLite store")
	require.NoError(t, store.Init(), "run bootstrap DDL + migrations")
	require.NoError(t, store.Close())

	// Reopen the file directly: Store does not expose its *sql.DB, and PRAGMA
	// introspection is test-only anyway. The "sqlite" driver is registered by
	// the storage package import.
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close() //nolint:errcheck // read-only introspection

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	require.NoError(t, err)
	defer rows.Close() //nolint:errcheck // read-only introspection

	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, names, "scratch database has no tables — migrations did not run")

	tables := make(map[string]tableSchema, len(names))
	for _, name := range names {
		tables[strings.ToLower(name)] = sqliteTableInfo(t, db, name)
	}
	return tables
}

// sqliteTableInfo reads column name -> declared type via PRAGMA table_info.
func sqliteTableInfo(t *testing.T, db *sql.DB, table string) tableSchema {
	t.Helper()

	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%q)", table))
	require.NoError(t, err)
	defer rows.Close() //nolint:errcheck // read-only introspection

	cols := tableSchema{}
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, typ        string
			dflt             sql.NullString
		)
		require.NoError(t, rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk))
		cols[strings.ToLower(name)] = typ
	}
	require.NoError(t, rows.Err())
	return cols
}

var (
	sqlLineComment = regexp.MustCompile(`--[^\n]*`)
	createTableRe  = regexp.MustCompile(`(?is)^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+(\w+)\s*\((.*)\)$`)
	alterTableRe   = regexp.MustCompile(`(?is)^ALTER\s+TABLE\s+(\w+)\s+ADD\s+COLUMN\s+IF\s+NOT\s+EXISTS\s+(\w+)\s+(.+)$`)
)

// parsePostgresSchema parses postgres.sql (CREATE TABLE bodies plus the
// idempotent retrofit ALTER TABLE ... ADD COLUMN statements) into the same
// shape as the SQLite side.
func parsePostgresSchema(t *testing.T) map[string]tableSchema {
	t.Helper()

	raw, err := os.ReadFile("postgres.sql")
	require.NoError(t, err, "postgres.sql must live next to this test")

	clean := sqlLineComment.ReplaceAllString(string(raw), "")
	tables := make(map[string]tableSchema)

	for _, stmt := range splitTopLevel(clean, ';') {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if m := createTableRe.FindStringSubmatch(stmt); m != nil {
			name := strings.ToLower(m[1])
			tables[name] = parseCreateTableBody(t, name, m[2])
			continue
		}
		if m := alterTableRe.FindStringSubmatch(stmt); m != nil {
			name := strings.ToLower(m[1])
			require.Contains(t, tables, name, "ALTER TABLE %s before its CREATE TABLE", m[1])
			tables[name][strings.ToLower(m[2])] = extractColumnType(m[3])
		}
	}

	require.NotEmpty(t, tables, "no CREATE TABLE statements parsed from postgres.sql")
	return tables
}

// parseCreateTableBody extracts column definitions from a CREATE TABLE body,
// skipping table-level constraints.
func parseCreateTableBody(t *testing.T, table, body string) tableSchema {
	t.Helper()

	cols := tableSchema{}
	for _, def := range splitTopLevel(body, ',') {
		def = strings.TrimSpace(def)
		if def == "" {
			continue
		}
		// A constraint keyword may abut its paren ("UNIQUE(user_id, ...)").
		first, _, _ := strings.Cut(strings.ToUpper(strings.Fields(def)[0]), "(")
		switch first {
		case "UNIQUE", "PRIMARY", "CHECK", "FOREIGN", "CONSTRAINT":
			continue // table-level constraint, not a column
		}
		fields := strings.Fields(def)
		require.GreaterOrEqual(t, len(fields), 2, "unparseable column def in %s: %q", table, def)
		cols[strings.ToLower(fields[0])] = extractColumnType(strings.Join(fields[1:], " "))
	}
	require.NotEmpty(t, cols, "table %s parsed with no columns", table)
	return cols
}

// extractColumnType returns the leading type tokens of a column definition
// tail, stopping at the first constraint keyword. Handles multi-word types
// ("double precision") and identity/default/not-null suffixes.
func extractColumnType(tail string) string {
	stop := map[string]bool{
		"NOT": true, "NULL": true, "DEFAULT": true, "PRIMARY": true,
		"UNIQUE": true, "GENERATED": true, "REFERENCES": true, "CHECK": true,
	}
	var typeTokens []string
	for _, tok := range strings.Fields(tail) {
		if stop[strings.ToUpper(tok)] {
			break
		}
		typeTokens = append(typeTokens, tok)
	}
	return strings.Join(typeTokens, " ")
}

// splitTopLevel splits s on sep, ignoring separators inside parentheses.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case sep:
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

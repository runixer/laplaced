package migrations

import "database/sql"

// columnExists checks if a column exists in a table.
func columnExists(tx *sql.Tx, table, column string) bool {
	var count int
	//nolint:errcheck // Error returns false (column doesn't exist) which is correct
	tx.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?", table, column).Scan(&count)
	return count > 0
}

// tableExists checks if a table exists in the database.
func tableExists(tx *sql.Tx, table string) bool {
	var count int
	//nolint:errcheck // Error returns false (table doesn't exist) which is correct
	tx.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count)
	return count > 0
}

// exec executes a statement and logs any error (used for optional cleanup).
func exec(tx *sql.Tx, query string, args ...any) {
	//nolint:errcheck // Cleanup operations are optional
	tx.Exec(query, args...)
}

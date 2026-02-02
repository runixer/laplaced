package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     0,
		Description: "Bootstrap: detect pre-existing schema",
		Up:          migrateBootstrap,
	})
}

// migrateBootstrap detects existing databases and marks all migrations as applied.
// This ensures databases created before the migration system are handled correctly.
func migrateBootstrap(tx *sql.Tx) error {
	// Check if this is a fresh database (schema_version table doesn't exist yet)
	// The runner creates schema_version before running migrations, so if we're here,
	// the table exists. We need to detect if other tables exist.

	// Check for artifacts table (latest feature in old migrate())
	if !tableExists(tx, "artifacts") {
		// Fresh database - no bootstrap needed
		return nil
	}

	// Database was created with old migrate() system.
	// Mark all migrations as applied to skip them.

	// The schema_version table was just created by the runner,
	// so we can insert all versions at once.

	versions := []struct {
		version     int
		description string
	}{
		{1, "pre-existing (bootstrap)"},
		{2, "pre-existing (bootstrap)"},
		{3, "pre-existing (bootstrap)"},
		{4, "pre-existing (bootstrap)"},
		{5, "pre-existing (bootstrap)"},
		{6, "pre-existing (bootstrap)"},
		{7, "pre-existing (bootstrap)"},
		{8, "pre-existing (bootstrap)"},
	}

	for _, v := range versions {
		if _, err := tx.Exec(
			"INSERT OR IGNORE INTO schema_version (version, description) VALUES (?, ?)",
			v.version, v.description,
		); err != nil {
			return err
		}
	}

	return nil
}

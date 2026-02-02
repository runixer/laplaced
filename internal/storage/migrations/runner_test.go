package migrations

import (
	"database/sql"
	"io"
	"log/slog"
	"testing"

	_ "modernc.org/sqlite"
)

// testLogger returns a discard logger for testing
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMigrationRunner(t *testing.T) {
	t.Run("fresh database runs all migrations", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		runner := NewRunner(db, testLogger())
		if err := runner.Run(); err != nil {
			t.Fatalf("Run() failed: %v", err)
		}

		// Check schema_version table
		var version int
		err = db.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
		if err != nil {
			t.Fatalf("Failed to query version: %v", err)
		}

		// Should be at least version 8 (latest migration)
		if version < 8 {
			t.Errorf("Expected version >= 8, got %d", version)
		}

		// Re-run should be idempotent
		if err := runner.Run(); err != nil {
			t.Fatalf("Re-run failed: %v", err)
		}
	})

	t.Run("bootstrap existing database", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		// Simulate existing database by creating artifacts table manually
		_, err = db.Exec(`
			CREATE TABLE IF NOT EXISTS artifacts (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id INTEGER NOT NULL,
				message_id INTEGER NOT NULL,
				file_type TEXT NOT NULL,
				file_path TEXT NOT NULL,
				content_hash TEXT NOT NULL,
				state TEXT NOT NULL DEFAULT 'pending',
				UNIQUE(user_id, content_hash)
			)
		`)
		if err != nil {
			t.Fatal(err)
		}

		// Create schema_version table first (as runner does)
		_, err = db.Exec(`
			CREATE TABLE IF NOT EXISTS schema_version (
				version INTEGER PRIMARY KEY,
				applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				description TEXT
			)
		`)
		if err != nil {
			t.Fatal(err)
		}

		runner := NewRunner(db, testLogger())
		if err := runner.Run(); err != nil {
			t.Fatalf("Run() failed: %v", err)
		}

		// Check that all versions are marked as applied (bootstrap effect)
		var count int
		err = db.QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&count)
		if err != nil {
			t.Fatalf("Failed to count migrations: %v", err)
		}

		// Bootstrap should have inserted all versions 1-8
		if count < 8 {
			t.Errorf("Expected >= 8 migrations applied, got %d", count)
		}
	})

	t.Run("verify tables exist", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		runner := NewRunner(db, testLogger())
		if err := runner.Run(); err != nil {
			t.Fatalf("Run() failed: %v", err)
		}

		// Verify key tables exist
		tables := []string{
			"users", "history", "stats", "topics", "structured_facts",
			"fact_history", "reranker_logs", "agent_logs", "people", "artifacts",
			"schema_version",
		}

		for _, table := range tables {
			var count int
			err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count)
			if err != nil {
				t.Errorf("Failed to check table %s: %v", table, err)
			}
			if count == 0 {
				t.Errorf("Table %s does not exist", table)
			}
		}
	})
}

func TestHelpers(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	//nolint:errcheck // Test cleanup, rollback is safe
	defer tx.Rollback()

	// Create test table
	_, err = tx.Exec("CREATE TABLE test_table (id INTEGER, name TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("columnExists returns true for existing column", func(t *testing.T) {
		if !columnExists(tx, "test_table", "id") {
			t.Error("Expected column 'id' to exist")
		}
		if !columnExists(tx, "test_table", "name") {
			t.Error("Expected column 'name' to exist")
		}
	})

	t.Run("columnExists returns false for non-existing column", func(t *testing.T) {
		if columnExists(tx, "test_table", "nonexistent") {
			t.Error("Expected column 'nonexistent' to not exist")
		}
	})

	t.Run("tableExists returns true for existing table", func(t *testing.T) {
		if !tableExists(tx, "test_table") {
			t.Error("Expected table to exist")
		}
	})

	t.Run("tableExists returns false for non-existing table", func(t *testing.T) {
		if tableExists(tx, "nonexistent_table") {
			t.Error("Expected table to not exist")
		}
	})
}

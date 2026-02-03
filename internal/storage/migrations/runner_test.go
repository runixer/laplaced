package migrations

import (
	"database/sql"
	"io"
	"log/slog"
	"testing"

	_ "modernc.org/sqlite"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunner(t *testing.T) {
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

	t.Run("re-run is idempotent", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		runner := NewRunner(db, testLogger())

		// First run
		if err := runner.Run(); err != nil {
			t.Fatalf("First Run() failed: %v", err)
		}

		// Second run should not fail
		if err := runner.Run(); err != nil {
			t.Fatalf("Second Run() failed: %v", err)
		}

		// Version should be the same
		var version int
		err = db.QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
		if err != nil {
			t.Fatalf("Failed to query version: %v", err)
		}
		if version < 8 {
			t.Errorf("Expected version >= 8, got %d", version)
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
}

// TestMigration004_Backfill tests the backfill logic in size_tracking migration.
// This is the only migration with non-trivial business logic (calculating size from messages).
func TestMigration004_Backfill(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Setup: create initial schema without size_chars column
	_, err = db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			telegram_id INTEGER NOT NULL UNIQUE,
			username TEXT,
			first_name TEXT,
			last_name TEXT,
			language_code TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`
		CREATE TABLE history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			topic_id INTEGER,
			timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES users(id)
		)
	`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`
		CREATE TABLE topics (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			start_msg_id INTEGER NOT NULL,
			end_msg_id INTEGER NOT NULL,
			summary TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES users(id),
			FOREIGN KEY (start_msg_id) REFERENCES history(id),
			FOREIGN KEY (end_msg_id) REFERENCES history(id)
		)
	`)
	if err != nil {
		t.Fatal(err)
	}

	// Create a topic and some messages
	var topicID int64
	err = db.QueryRow("INSERT INTO topics (user_id, start_msg_id, end_msg_id) VALUES (1, 1, 1) RETURNING id").Scan(&topicID)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec("INSERT INTO history (user_id, role, content, topic_id) VALUES (1, 'user', 'hello world', ?)", topicID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO history (user_id, role, content, topic_id) VALUES (1, 'assistant', 'hi there', ?)", topicID)
	if err != nil {
		t.Fatal(err)
	}

	// Run migration
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSizeTracking(tx); err != nil {
		t.Fatalf("migrateSizeTracking failed: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Check backfill happened (should have calculated size from messages)
	var sizeChars int
	err = db.QueryRow("SELECT size_chars FROM topics WHERE id = ?", topicID).Scan(&sizeChars)
	if err != nil {
		t.Fatalf("Failed to query size_chars: %v", err)
	}
	// "hello world" (11) + "hi there" (8) = 19
	if sizeChars != 19 {
		t.Errorf("Expected size_chars = 19, got %d", sizeChars)
	}
}

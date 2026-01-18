package storage

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func setupTestDB(t *testing.T) (*SQLiteStore, func()) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	// Use in-memory SQLite database for testing
	store, err := NewSQLiteStore(logger, ":memory:")
	if err != nil {
		t.Fatal(err)
	}

	cleanup := func() {
		store.Close()
	}

	return store, cleanup
}

func TestNewSQLiteStore(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	assert.NotNil(t, store)
	assert.NotNil(t, store.db)
}

func TestInit(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	err := store.Init()
	assert.NoError(t, err)

	// Check if tables were created
	// We check a few key tables to ensure migration ran
	tables := []string{"history", "stats", "topics", "users", "structured_facts", "fact_history", "rag_logs", "memory_bank", "people"}

	for _, table := range tables {
		var name string
		err := store.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		assert.NoError(t, err, "Table %s should exist", table)
		assert.Equal(t, table, name)
	}
}

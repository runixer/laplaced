//go:build test

package testutil

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/stretchr/testify/require"
)

// TestBot wraps a full bot instance for testing.
// This file only compiles during tests to avoid circular imports.
type TestBot struct {
	t          *testing.T
	logger     *slog.Logger
	logCapture *LogCapture
	cfg        *testConfig
	store      *storage.SQLiteStore
	tempDir    string
	dbPath     string
}

// testConfig is a minimal config for testing to avoid circular imports.
type testConfig struct {
	databasePath string
}

// TestBotOptions configures TestBot creation.
type TestBotOptions struct {
	Logger      *slog.Logger
	CaptureLogs bool
	TempDir     string
}

// NewTestBot creates a minimal test bot for integration tests.
// Note: This is a simplified version that only provides database and logging.
// For full bot functionality, tests should use the scenario DSL with mock agents.
func NewTestBot(t *testing.T, opts *TestBotOptions) *TestBot {
	t.Helper()

	if opts == nil {
		opts = &TestBotOptions{}
	}

	tb := &TestBot{t: t}

	// Setup temp directory
	if opts.TempDir != "" {
		tb.tempDir = opts.TempDir
	} else {
		tb.tempDir = t.TempDir()
	}

	// Setup logger
	if opts.Logger != nil {
		tb.logger = opts.Logger
	} else {
		if opts.CaptureLogs {
			tb.logCapture = NewLogCapture()
			tb.logger = slog.New(tb.logCapture.handler)
		} else {
			tb.logger = TestLogger()
		}
	}

	// Setup temporary database
	tb.dbPath = filepath.Join(tb.tempDir, "test.db")

	// Create store
	var err error
	tb.store, err = storage.NewSQLiteStore(tb.logger, tb.dbPath)
	require.NoError(t, err, "failed to create store")
	err = tb.store.Init()
	require.NoError(t, err, "failed to init store")

	return tb
}

// Store returns the storage store.
func (tb *TestBot) Store() *storage.SQLiteStore {
	return tb.store
}

// Logger returns the logger.
func (tb *TestBot) Logger() *slog.Logger {
	return tb.logger
}

// LogEntries returns captured log entries.
func (tb *TestBot) LogEntries() []LogEntry {
	if tb.logCapture == nil {
		tb.t.Fatal("log capture not enabled - use TestBotOptions{CaptureLogs: true}")
	}
	return tb.logCapture.Entries()
}

// Close cleans up resources.
func (tb *TestBot) Close() {
	if tb.store != nil {
		tb.store.Close()
	}
	if tb.dbPath != "" {
		os.Remove(tb.dbPath)
	}
}

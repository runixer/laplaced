// Package migrations handles database schema migrations with version tracking.
//
// Migration System:
//
//   - Each migration has a unique version number
//   - schema_version table tracks applied migrations
//   - Migrations run in transaction (all-or-nothing)
//   - Bootstrap migration detects pre-existing databases
//
// Adding New Migration:
//
//  1. Create file XXX_descriptive_name.go (XXX = next version number)
//  2. Register migration in init() using migrations.Register()
//  3. Implement up(tx) function with schema changes
//  4. Add verification test in runner_test.go
package migrations

import (
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// Migration represents a single schema migration.
type Migration struct {
	Version     int
	Description string
	Up          func(tx *sql.Tx) error
}

// Runner executes migrations in order.
type Runner struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewRunner creates a new migration runner.
func NewRunner(db *sql.DB, logger *slog.Logger) *Runner {
	return &Runner{
		db:     db,
		logger: logger,
	}
}

// Run executes all pending migrations.
func (r *Runner) Run() error {
	// 1. Ensure schema_version table exists
	if err := r.ensureVersionTable(); err != nil {
		return fmt.Errorf("failed to create version table: %w", err)
	}

	// 2. Get current version
	currentVersion, err := r.getCurrentVersion()
	if err != nil {
		return fmt.Errorf("failed to get current version: %w", err)
	}

	// 3. Get all registered migrations
	all := allMigrations()
	if len(all) == 0 {
		r.logger.Info("no migrations registered")
		return nil
	}

	// 4. Run pending migrations
	for _, m := range all {
		if m.Version <= currentVersion {
			continue
		}
		if err := r.runMigration(m); err != nil {
			return fmt.Errorf("migration %d (%s) failed: %w", m.Version, m.Description, err)
		}
	}

	r.logger.Info("migrations completed", "current_version", currentVersion, "latest_version", all[len(all)-1].Version)
	return nil
}

// ensureVersionTable creates the schema_version table if it doesn't exist.
func (r *Runner) ensureVersionTable() error {
	_, err := r.db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_version (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			description TEXT
		)
	`)
	return err
}

// getCurrentVersion returns the highest applied migration version.
func (r *Runner) getCurrentVersion() (int, error) {
	var version int
	err := r.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	return version, err
}

// runMigration executes a single migration in a transaction.
func (r *Runner) runMigration(m Migration) error {
	r.logger.Info("running migration", "version", m.Version, "description", m.Description)

	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	//nolint:errcheck // Rollback is harmless if transaction commits; Commit error is checked
	defer tx.Rollback()

	if err := m.Up(tx); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	if _, err := tx.Exec(
		"INSERT INTO schema_version (version, description) VALUES (?, ?)",
		m.Version, m.Description,
	); err != nil {
		return fmt.Errorf("failed to record migration: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit migration: %w", err)
	}

	r.logger.Info("migration applied", "version", m.Version)
	return nil
}

// registry holds all registered migrations.
var (
	registry []Migration
	mu       sync.RWMutex
)

// Register registers a migration. Called from init() functions.
func Register(m Migration) {
	mu.Lock()
	defer mu.Unlock()
	registry = append(registry, m)
}

// allMigrations returns all registered migrations sorted by version.
func allMigrations() []Migration {
	mu.RLock()
	defer mu.RUnlock()

	// Make a copy to avoid race conditions
	result := make([]Migration, len(registry))
	copy(result, registry)

	// Sort by version
	sort.Slice(result, func(i, j int) bool {
		return result[i].Version < result[j].Version
	})

	return result
}

package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     3,
		Description: "Fact history tracking columns",
		Up:          migrateFactHistory,
	})
}

func migrateFactHistory(tx *sql.Tx) error {
	// Add topic_id to fact_history
	if !columnExists(tx, "fact_history", "topic_id") {
		if _, err := tx.Exec("ALTER TABLE fact_history ADD COLUMN topic_id INTEGER"); err != nil {
			return err
		}
	}

	// Add category to fact_history
	if !columnExists(tx, "fact_history", "category") {
		if _, err := tx.Exec("ALTER TABLE fact_history ADD COLUMN category TEXT"); err != nil {
			return err
		}
	}

	// Add importance to fact_history
	if !columnExists(tx, "fact_history", "importance") {
		if _, err := tx.Exec("ALTER TABLE fact_history ADD COLUMN importance INTEGER DEFAULT 0"); err != nil {
			return err
		}
	}

	// Add relation to fact_history
	if !columnExists(tx, "fact_history", "relation") {
		if _, err := tx.Exec("ALTER TABLE fact_history ADD COLUMN relation TEXT"); err != nil {
			return err
		}
	}

	// Add request_input to fact_history
	if !columnExists(tx, "fact_history", "request_input") {
		if _, err := tx.Exec("ALTER TABLE fact_history ADD COLUMN request_input TEXT"); err != nil {
			return err
		}
	}

	// Ensure indexes exist (idempotent)
	if _, err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_fact_history_topic_id ON fact_history(topic_id)"); err != nil {
		return err
	}

	return nil
}

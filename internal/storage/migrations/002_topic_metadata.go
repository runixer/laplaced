package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     2,
		Description: "Topic metadata: history.topic_id, topic tracking columns",
		Up:          migrateTopicMetadata,
	})
}

func migrateTopicMetadata(tx *sql.Tx) error {
	// Add topic_id to history table if not exists
	if !columnExists(tx, "history", "topic_id") {
		if _, err := tx.Exec("ALTER TABLE history ADD COLUMN topic_id INTEGER REFERENCES topics(id)"); err != nil {
			return err
		}
		// PERF: This index is critical for GetTopicsExtended() which joins history to count messages per topic.
		// Without it, queries with message_count sorting become O(n) table scans.
		if _, err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_history_topic_id ON history(topic_id)"); err != nil {
			return err
		}
	}

	// Add facts_extracted to topics
	if !columnExists(tx, "topics", "facts_extracted") {
		if _, err := tx.Exec("ALTER TABLE topics ADD COLUMN facts_extracted BOOLEAN DEFAULT 0"); err != nil {
			return err
		}
	}

	// Add is_consolidated to topics
	if !columnExists(tx, "topics", "is_consolidated") {
		if _, err := tx.Exec("ALTER TABLE topics ADD COLUMN is_consolidated BOOLEAN DEFAULT 0"); err != nil {
			return err
		}
	}

	// Add consolidation_checked to topics
	if !columnExists(tx, "topics", "consolidation_checked") {
		if _, err := tx.Exec("ALTER TABLE topics ADD COLUMN consolidation_checked BOOLEAN DEFAULT 0"); err != nil {
			return err
		}
	}

	// Add topic_id to structured_facts
	if !columnExists(tx, "structured_facts", "topic_id") {
		if _, err := tx.Exec("ALTER TABLE structured_facts ADD COLUMN topic_id INTEGER"); err != nil {
			return err
		}
		// Create index for topic lookups
		if _, err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_structured_facts_topic_id ON structured_facts(topic_id)"); err != nil {
			return err
		}
	}

	return nil
}

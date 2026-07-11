package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     17,
		Description: "Fact provenance: structured_facts.kind (self_report/user_opinion/verified/constraint)",
		Up:          migrateFactKind,
	})
}

// migrateFactKind adds a `kind` provenance column to structured_facts.
//
// Stored facts are re-injected into every LLM context as ground truth, which
// turns one-sided user verdicts about other people into statements the model
// cannot argue with. The kind column lets write paths record provenance
// (self_report / user_opinion / verified / constraint) so the context layer
// can present opinions as testimony and behavioral constraints as overridable
// preferences. Existing rows default to self_report — historically most facts
// are the user's statements about themselves; no backfill classification is
// attempted.
func migrateFactKind(tx *sql.Tx) error {
	if tableExists(tx, "structured_facts") && !columnExists(tx, "structured_facts", "kind") {
		if _, err := tx.Exec("ALTER TABLE structured_facts ADD COLUMN kind TEXT NOT NULL DEFAULT 'self_report'"); err != nil {
			return err
		}
	}
	return nil
}

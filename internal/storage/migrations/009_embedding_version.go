package migrations

import "database/sql"

func init() {
	Register(Migration{
		Version:     9,
		Description: "Embedding version tracking for v2 migration (v0.7.0)",
		Up:          migrateEmbeddingVersion,
	})
}

// migrateEmbeddingVersion adds an `embedding_version` TEXT column to every
// table that stores embeddings. The value encodes the embedding model + dim
// (e.g. "google/gemini-embedding-2-preview:1536"); NULL means legacy,
// pre-v0.7.0 vectors produced by gemini-embedding-001 at 3072.
//
// On startup the bot checks this column against the configured current model
// and re-embeds rows whose version does not match.
func migrateEmbeddingVersion(tx *sql.Tx) error {
	tables := []string{"topics", "structured_facts", "people", "artifacts"}
	for _, table := range tables {
		if !tableExists(tx, table) {
			continue
		}
		if columnExists(tx, table, "embedding_version") {
			continue
		}
		if _, err := tx.Exec("ALTER TABLE " + table + " ADD COLUMN embedding_version TEXT"); err != nil {
			return err
		}
	}
	return nil
}

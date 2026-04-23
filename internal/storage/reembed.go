package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// ReembedCandidate identifies a row that needs its embedding re-generated
// because the current `embedding_version` does not match the configured
// embedding model + dimension.
//
// Content is the text that should be passed to the embedding model for this
// entity (summary for topics/artifacts, body for facts, composed searchable
// string for people).
type ReembedCandidate struct {
	ID      int64
	UserID  int64
	Content string
}

// reembedSelect runs the common SELECT for "rows needing re-embed" against a
// single table with a caller-provided content expression. Version match is
// strict: NULL or a string different from expectedVersion both qualify.
//
// limit == 0 means unlimited.
func (s *SQLiteStore) reembedSelect(table, contentExpr, expectedVersion string, limit int) ([]ReembedCandidate, error) {
	// #nosec G201 -- `table` and `contentExpr` are internal constants from this package, never user input
	q := fmt.Sprintf(
		`SELECT id, user_id, %s FROM %s
		 WHERE embedding IS NOT NULL
		   AND (embedding_version IS NULL OR embedding_version != ?)
		 ORDER BY id`,
		contentExpr, table,
	)
	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = s.db.Query(q+" LIMIT ?", expectedVersion, limit)
	} else {
		rows, err = s.db.Query(q, expectedVersion)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReembedCandidate
	for rows.Next() {
		var c ReembedCandidate
		if err := rows.Scan(&c.ID, &c.UserID, &c.Content); err != nil {
			return nil, err
		}
		if c.Content == "" {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// reembedUpdate writes a freshly-computed embedding + version tag to one row.
// Uses JSON text representation to stay compatible with existing readers.
func (s *SQLiteStore) reembedUpdate(table string, id int64, emb []float32, version string) error {
	embBytes, err := json.Marshal(emb)
	if err != nil {
		return err
	}
	// #nosec G201 -- `table` is an internal constant from this package, never user input
	q := fmt.Sprintf(`UPDATE %s SET embedding = ?, embedding_version = ? WHERE id = ?`, table)
	_, err = s.db.Exec(q, embBytes, version, id)
	return err
}

// --- Topics ---

// GetTopicsNeedingReembed returns topics whose embedding_version is not the
// expected one. Content is the topic summary.
func (s *SQLiteStore) GetTopicsNeedingReembed(expectedVersion string, limit int) ([]ReembedCandidate, error) {
	return s.reembedSelect("topics", "COALESCE(summary, '')", expectedVersion, limit)
}

func (s *SQLiteStore) UpdateTopicEmbeddingVersion(id int64, emb []float32, version string) error {
	return s.reembedUpdate("topics", id, emb, version)
}

// --- Facts ---

func (s *SQLiteStore) GetFactsNeedingReembed(expectedVersion string, limit int) ([]ReembedCandidate, error) {
	return s.reembedSelect("structured_facts", "COALESCE(content, '')", expectedVersion, limit)
}

func (s *SQLiteStore) UpdateFactEmbeddingVersion(id int64, emb []float32, version string) error {
	return s.reembedUpdate("structured_facts", id, emb, version)
}

// --- People ---

// GetPeopleNeedingReembed composes the search text inline, mirroring
// `internal/bot/tools/people.go`: display name + aliases (JSON) + bio.
func (s *SQLiteStore) GetPeopleNeedingReembed(expectedVersion string, limit int) ([]ReembedCandidate, error) {
	q := `SELECT id, user_id, display_name, aliases, bio FROM people
	      WHERE embedding IS NOT NULL
	        AND (embedding_version IS NULL OR embedding_version != ?)
	      ORDER BY id`
	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = s.db.Query(q+" LIMIT ?", expectedVersion, limit)
	} else {
		rows, err = s.db.Query(q, expectedVersion)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReembedCandidate
	for rows.Next() {
		var c ReembedCandidate
		var name, aliasesJSON, bio *string
		if err := rows.Scan(&c.ID, &c.UserID, &name, &aliasesJSON, &bio); err != nil {
			return nil, err
		}
		c.Content = composePersonEmbeddingText(name, aliasesJSON, bio)
		if c.Content == "" {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func composePersonEmbeddingText(name, aliasesJSON, bio *string) string {
	var s string
	if name != nil {
		s = *name
	}
	if aliasesJSON != nil && *aliasesJSON != "" && *aliasesJSON != "null" {
		var aliases []string
		if err := json.Unmarshal([]byte(*aliasesJSON), &aliases); err == nil {
			for _, a := range aliases {
				if a != "" {
					s += " " + a
				}
			}
		}
	}
	if bio != nil && *bio != "" {
		s += " " + *bio
	}
	return s
}

func (s *SQLiteStore) UpdatePersonEmbeddingVersion(id int64, emb []float32, version string) error {
	return s.reembedUpdate("people", id, emb, version)
}

// --- Artifacts ---

func (s *SQLiteStore) GetArtifactsNeedingReembed(expectedVersion string, limit int) ([]ReembedCandidate, error) {
	return s.reembedSelect("artifacts", "COALESCE(summary, '')", expectedVersion, limit)
}

func (s *SQLiteStore) UpdateArtifactEmbeddingVersion(id int64, emb []float32, version string) error {
	return s.reembedUpdate("artifacts", id, emb, version)
}

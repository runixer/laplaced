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
	UserID  ScopeID
	Content string
}

// reembedSelect runs the common SELECT for "rows needing re-embed" against a
// single table with a caller-provided content expression. Version match is
// strict: NULL or a string different from expectedVersion both qualify.
//
// limit == 0 means unlimited.
func (s *Store) reembedSelect(table, contentExpr, expectedVersion string, limit int) ([]ReembedCandidate, error) {
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
		rows, err = s.query(q+" LIMIT ?", expectedVersion, limit)
	} else {
		rows, err = s.query(q, expectedVersion)
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
func (s *Store) reembedUpdate(table string, id int64, emb []float32, version string) error {
	embBytes, err := json.Marshal(emb)
	if err != nil {
		return err
	}
	// #nosec G201 -- `table` is an internal constant from this package, never user input
	q := fmt.Sprintf(`UPDATE %s SET embedding = ?, embedding_version = ? WHERE id = ?`, table)
	_, err = s.exec(q, embBytes, version, id)
	return err
}

// --- Topics ---

// GetTopicsNeedingReembed returns topics whose embedding_version is not the
// expected one. Content is the topic summary.
func (s *Store) GetTopicsNeedingReembed(expectedVersion string, limit int) ([]ReembedCandidate, error) {
	return s.reembedSelect("topics", "COALESCE(summary, '')", expectedVersion, limit)
}

func (s *Store) UpdateTopicEmbeddingVersion(id int64, emb []float32, version string) error {
	return s.reembedUpdate("topics", id, emb, version)
}

// --- Facts ---

func (s *Store) GetFactsNeedingReembed(expectedVersion string, limit int) ([]ReembedCandidate, error) {
	return s.reembedSelect("structured_facts", "COALESCE(content, '')", expectedVersion, limit)
}

func (s *Store) UpdateFactEmbeddingVersion(id int64, emb []float32, version string) error {
	return s.reembedUpdate("structured_facts", id, emb, version)
}

// --- People ---

// GetPeopleNeedingReembed composes the search text via the canonical
// ComposePersonEmbeddingText, the SAME function the live write path uses, so a
// re-embed reproduces existing vectors instead of drifting them. It MUST pull
// every field that composer reads (display_name, username, aliases, bio) — a
// missing column here silently produces a different text and a drifted vector.
func (s *Store) GetPeopleNeedingReembed(expectedVersion string, limit int) ([]ReembedCandidate, error) {
	q := `SELECT id, user_id, display_name, username, aliases, bio FROM people
	      WHERE embedding IS NOT NULL
	        AND (embedding_version IS NULL OR embedding_version != ?)
	      ORDER BY id`
	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = s.query(q+" LIMIT ?", expectedVersion, limit)
	} else {
		rows, err = s.query(q, expectedVersion)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReembedCandidate
	for rows.Next() {
		var c ReembedCandidate
		var name, username, aliasesJSON, bio *string
		if err := rows.Scan(&c.ID, &c.UserID, &name, &username, &aliasesJSON, &bio); err != nil {
			return nil, err
		}
		var displayName, bioStr string
		if name != nil {
			displayName = *name
		}
		if bio != nil {
			bioStr = *bio
		}
		var aliases []string
		if aliasesJSON != nil && *aliasesJSON != "" && *aliasesJSON != "null" {
			_ = json.Unmarshal([]byte(*aliasesJSON), &aliases)
		}
		c.Content = ComposePersonEmbeddingText(displayName, username, aliases, bioStr)
		if c.Content == "" {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ComposePersonEmbeddingText is the single source of truth for the text that
// represents a person to the embedding model: display name + username +
// aliases + bio. Every site that embeds a person — the live add/update/merge
// paths and the startup re-embed — MUST go through this function. When two
// sites composed it differently (the re-embed once dropped username), stored
// and freshly-written vectors landed in subtly different spaces and people
// RAG quality silently degraded for anyone with a username.
func ComposePersonEmbeddingText(displayName string, username *string, aliases []string, bio string) string {
	text := displayName
	if username != nil && *username != "" {
		text += " " + *username
	}
	for _, alias := range aliases {
		if alias != "" {
			text += " " + alias
		}
	}
	if bio != "" {
		text += " " + bio
	}
	return text
}

func (s *Store) UpdatePersonEmbeddingVersion(id int64, emb []float32, version string) error {
	return s.reembedUpdate("people", id, emb, version)
}

// --- Artifacts ---

func (s *Store) GetArtifactsNeedingReembed(expectedVersion string, limit int) ([]ReembedCandidate, error) {
	return s.reembedSelect("artifacts", "COALESCE(summary, '')", expectedVersion, limit)
}

func (s *Store) UpdateArtifactEmbeddingVersion(id int64, emb []float32, version string) error {
	return s.reembedUpdate("artifacts", id, emb, version)
}

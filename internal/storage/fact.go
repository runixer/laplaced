package storage

import (
	"database/sql"
	"encoding/json"
	"time"
)

func (s *Store) AddFact(fact Fact) (int64, error) {
	embBytes, err := json.Marshal(fact.Embedding)
	if err != nil {
		return 0, err
	}
	if fact.CreatedAt.IsZero() {
		fact.CreatedAt = time.Now()
	}
	if fact.LastUpdated.IsZero() {
		fact.LastUpdated = time.Now()
	}
	query := `
		INSERT INTO structured_facts (user_id, relation, category, content, type, importance, embedding, embedding_version, topic_id, created_at, last_updated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, relation, content) DO UPDATE SET
			last_updated = excluded.last_updated,
			importance = excluded.importance,
			type = excluded.type,
			category = excluded.category,
			topic_id = COALESCE(excluded.topic_id, structured_facts.topic_id)
	`
	_, err = s.exec(query, fact.UserID, fact.Relation, fact.Category, fact.Content, fact.Type, fact.Importance, embBytes, s.embeddingVersion, fact.TopicID, s.dialect.BindTime(fact.CreatedAt), s.dialect.BindTime(fact.LastUpdated))
	if err != nil {
		return 0, err
	}

	// Get the ID (whether inserted or updated)
	var id int64
	err = s.queryRow("SELECT id FROM structured_facts WHERE user_id = ? AND relation = ? AND content = ?", fact.UserID, fact.Relation, fact.Content).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// GetAllFacts retrieves all facts across all users.
// WARNING: Cross-user access - used for vector index loading only.
func (s *Store) GetAllFacts() ([]Fact, error) {
	query := "SELECT id, user_id, relation, category, content, type, importance, embedding, topic_id, created_at, last_updated FROM structured_facts"
	rows, err := s.query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var embBytes []byte
		if err := rows.Scan(&f.ID, &f.UserID, &f.Relation, &f.Category, &f.Content, &f.Type, &f.Importance, &embBytes, &f.TopicID, &f.CreatedAt, &f.LastUpdated); err != nil {
			return nil, err
		}
		if len(embBytes) > 0 {
			if err := json.Unmarshal(embBytes, &f.Embedding); err != nil {
				s.logger.Warn("failed to unmarshal fact embedding", "id", f.ID, "error", err)
				continue
			}
		}
		facts = append(facts, f)
	}
	return facts, nil
}

// GetFactsAfterID retrieves facts created after given ID across all users.
// WARNING: Cross-user access - used for incremental vector index updates.
func (s *Store) GetFactsAfterID(minID int64) ([]Fact, error) {
	query := "SELECT id, user_id, relation, category, content, type, importance, embedding, topic_id, created_at, last_updated FROM structured_facts WHERE id > ? ORDER BY id ASC"
	rows, err := s.query(query, minID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var embBytes []byte
		if err := rows.Scan(&f.ID, &f.UserID, &f.Relation, &f.Category, &f.Content, &f.Type, &f.Importance, &embBytes, &f.TopicID, &f.CreatedAt, &f.LastUpdated); err != nil {
			return nil, err
		}
		if len(embBytes) > 0 {
			if err := json.Unmarshal(embBytes, &f.Embedding); err != nil {
				s.logger.Warn("failed to unmarshal fact embedding", "id", f.ID, "error", err)
				continue
			}
		}
		facts = append(facts, f)
	}
	return facts, nil
}

func (s *Store) GetFactStats() (FactStats, error) {
	stats := FactStats{
		CountByType: make(map[string]int),
	}

	// 1. Count by type
	rows, err := s.query("SELECT type, COUNT(*) FROM structured_facts GROUP BY type")
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		var count int
		if err := rows.Scan(&t, &count); err != nil {
			return stats, err
		}
		stats.CountByType[t] = count
	}

	// 2. Average Age
	// SQLite's julianday returns the Julian day number. The difference is in days.
	// We use COALESCE to fallback to created_at if last_updated is null (though it shouldn't be per schema default)
	var avgAgeDays sql.NullFloat64
	err = s.queryRow("SELECT " + s.dialect.AvgAgeDaysExpr("last_updated") + " FROM structured_facts").Scan(&avgAgeDays)
	if err != nil {
		return stats, err
	}
	if avgAgeDays.Valid {
		stats.AvgAgeDays = avgAgeDays.Float64
	}

	return stats, nil
}

func (s *Store) GetFactStatsByUser(userID ScopeID) (FactStats, error) {
	stats := FactStats{
		CountByType: make(map[string]int),
	}

	// 1. Count by type for this user
	rows, err := s.query("SELECT type, COUNT(*) FROM structured_facts WHERE user_id = ? GROUP BY type", userID)
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		var count int
		if err := rows.Scan(&t, &count); err != nil {
			return stats, err
		}
		stats.CountByType[t] = count
	}

	// 2. Average Age for this user
	var avgAgeDays sql.NullFloat64
	err = s.queryRow("SELECT "+s.dialect.AvgAgeDaysExpr("last_updated")+" FROM structured_facts WHERE user_id = ?", userID).Scan(&avgAgeDays)
	if err != nil {
		return stats, err
	}
	if avgAgeDays.Valid {
		stats.AvgAgeDays = avgAgeDays.Float64
	}

	return stats, nil
}

func (s *Store) UpdateFact(fact Fact) error {
	if fact.LastUpdated.IsZero() {
		fact.LastUpdated = time.Now()
	}
	// The embedding MUST be persisted here: callers recompute it whenever the
	// content changes (see memory.applyUpdateWithStats). Omitting it leaves a
	// vector that no longer matches the text — a silent RAG-quality regression
	// invisible to tests and logs.
	embBytes, err := json.Marshal(fact.Embedding)
	if err != nil {
		return err
	}
	// embedding_version is re-stamped only when the vector actually changed:
	// importance-only updates round-trip the stored embedding, and stamping
	// them with the current version would exempt an old-space vector from the
	// startup re-embed forever (SET expressions see pre-update column values).
	query := `
		UPDATE structured_facts
		SET content = ?, type = ?, importance = ?, embedding = ?,
		    embedding_version = CASE WHEN embedding = ? THEN embedding_version ELSE ? END,
		    last_updated = ?
		WHERE id = ? AND user_id = ?
	`
	_, err = s.exec(query, fact.Content, fact.Type, fact.Importance, embBytes, embBytes, s.embeddingVersion, s.dialect.BindTime(fact.LastUpdated), fact.ID, fact.UserID)
	return err
}

// UpdateFactsTopic updates topic_id for all facts belonging to a user and old topic.
func (s *Store) UpdateFactsTopic(userID ScopeID, oldTopicID, newTopicID int64) error {
	query := "UPDATE structured_facts SET topic_id = ? WHERE user_id = ? AND topic_id = ?"
	_, err := s.exec(query, newTopicID, userID, oldTopicID)
	return err
}

func (s *Store) DeleteFact(userID ScopeID, id int64) error {
	query := "DELETE FROM structured_facts WHERE id = ? AND user_id = ?"
	_, err := s.exec(query, id, userID)
	return err
}

// DeleteAllFacts removes all facts for a user in a single query.
func (s *Store) DeleteAllFacts(userID ScopeID) error {
	query := "DELETE FROM structured_facts WHERE user_id = ?"
	_, err := s.exec(query, userID)
	return err
}

func (s *Store) GetFacts(userID ScopeID) ([]Fact, error) {
	query := "SELECT id, user_id, relation, category, content, type, importance, embedding, topic_id, created_at, last_updated FROM structured_facts WHERE user_id = ?"
	rows, err := s.query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var embBytes []byte
		if err := rows.Scan(&f.ID, &f.UserID, &f.Relation, &f.Category, &f.Content, &f.Type, &f.Importance, &embBytes, &f.TopicID, &f.CreatedAt, &f.LastUpdated); err != nil {
			return nil, err
		}
		if len(embBytes) > 0 {
			if err := json.Unmarshal(embBytes, &f.Embedding); err != nil {
				s.logger.Warn("failed to unmarshal fact embedding", "id", f.ID, "error", err)
				continue
			}
		}
		facts = append(facts, f)
	}
	return facts, nil
}

func (s *Store) GetFactsByIDs(userID ScopeID, ids []int64) ([]Fact, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query, args, err := ExpandIn(
		"SELECT id, user_id, relation, category, content, type, importance, embedding, topic_id, created_at, last_updated FROM structured_facts WHERE user_id = ? AND id IN (?)",
		userID, ids,
	)
	if err != nil {
		return nil, err
	}

	rows, err := s.query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var embBytes []byte
		if err := rows.Scan(&f.ID, &f.UserID, &f.Relation, &f.Category, &f.Content, &f.Type, &f.Importance, &embBytes, &f.TopicID, &f.CreatedAt, &f.LastUpdated); err != nil {
			return nil, err
		}
		if len(embBytes) > 0 {
			if err := json.Unmarshal(embBytes, &f.Embedding); err != nil {
				s.logger.Warn("failed to unmarshal fact embedding", "id", f.ID, "error", err)
				continue
			}
		}
		facts = append(facts, f)
	}
	return facts, nil
}

func (s *Store) GetFactsByTopicID(userID ScopeID, topicID int64) ([]Fact, error) {
	query := "SELECT id, user_id, relation, category, content, type, importance, embedding, topic_id, created_at, last_updated FROM structured_facts WHERE user_id = ? AND topic_id = ?"
	rows, err := s.query(query, userID, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var embBytes []byte
		if err := rows.Scan(&f.ID, &f.UserID, &f.Relation, &f.Category, &f.Content, &f.Type, &f.Importance, &embBytes, &f.TopicID, &f.CreatedAt, &f.LastUpdated); err != nil {
			return nil, err
		}
		if len(embBytes) > 0 {
			if err := json.Unmarshal(embBytes, &f.Embedding); err != nil {
				s.logger.Warn("failed to unmarshal fact embedding", "id", f.ID, "error", err)
				continue
			}
		}
		facts = append(facts, f)
	}
	return facts, nil
}

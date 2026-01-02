package storage

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

func (s *SQLiteStore) AddFact(fact Fact) (int64, error) {
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
		INSERT INTO structured_facts (user_id, entity, relation, category, content, type, importance, embedding, topic_id, created_at, last_updated) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, entity, relation, content) DO UPDATE SET
			last_updated = excluded.last_updated,
			importance = excluded.importance,
			type = excluded.type,
			category = excluded.category,
			topic_id = COALESCE(excluded.topic_id, structured_facts.topic_id)
	`
	_, err = s.db.Exec(query, fact.UserID, fact.Entity, fact.Relation, fact.Category, fact.Content, fact.Type, fact.Importance, embBytes, fact.TopicID, fact.CreatedAt.UTC().Format("2006-01-02 15:04:05.999"), fact.LastUpdated.UTC().Format("2006-01-02 15:04:05.999"))
	if err != nil {
		return 0, err
	}

	// Get the ID (whether inserted or updated)
	var id int64
	err = s.db.QueryRow("SELECT id FROM structured_facts WHERE user_id = ? AND entity = ? AND relation = ? AND content = ?", fact.UserID, fact.Entity, fact.Relation, fact.Content).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *SQLiteStore) GetAllFacts() ([]Fact, error) {
	query := "SELECT id, user_id, entity, relation, category, content, type, importance, embedding, topic_id, created_at, last_updated FROM structured_facts"
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var embBytes []byte
		if err := rows.Scan(&f.ID, &f.UserID, &f.Entity, &f.Relation, &f.Category, &f.Content, &f.Type, &f.Importance, &embBytes, &f.TopicID, &f.CreatedAt, &f.LastUpdated); err != nil {
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

func (s *SQLiteStore) GetFactsAfterID(minID int64) ([]Fact, error) {
	query := "SELECT id, user_id, entity, relation, category, content, type, importance, embedding, topic_id, created_at, last_updated FROM structured_facts WHERE id > ? ORDER BY id ASC"
	rows, err := s.db.Query(query, minID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var embBytes []byte
		if err := rows.Scan(&f.ID, &f.UserID, &f.Entity, &f.Relation, &f.Category, &f.Content, &f.Type, &f.Importance, &embBytes, &f.TopicID, &f.CreatedAt, &f.LastUpdated); err != nil {
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

func (s *SQLiteStore) GetFactStats() (FactStats, error) {
	stats := FactStats{
		CountByType: make(map[string]int),
	}

	// 1. Count by type
	rows, err := s.db.Query("SELECT type, COUNT(*) FROM structured_facts GROUP BY type")
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
	err = s.db.QueryRow("SELECT AVG(julianday('now') - julianday(last_updated)) FROM structured_facts").Scan(&avgAgeDays)
	if err != nil {
		return stats, err
	}
	if avgAgeDays.Valid {
		stats.AvgAgeDays = avgAgeDays.Float64
	}

	return stats, nil
}

func (s *SQLiteStore) GetFactStatsByUser(userID int64) (FactStats, error) {
	stats := FactStats{
		CountByType: make(map[string]int),
	}

	// 1. Count by type for this user
	rows, err := s.db.Query("SELECT type, COUNT(*) FROM structured_facts WHERE user_id = ? GROUP BY type", userID)
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
	err = s.db.QueryRow("SELECT AVG(julianday('now') - julianday(last_updated)) FROM structured_facts WHERE user_id = ?", userID).Scan(&avgAgeDays)
	if err != nil {
		return stats, err
	}
	if avgAgeDays.Valid {
		stats.AvgAgeDays = avgAgeDays.Float64
	}

	return stats, nil
}

func (s *SQLiteStore) UpdateFact(fact Fact) error {
	if fact.LastUpdated.IsZero() {
		fact.LastUpdated = time.Now()
	}
	query := `
		UPDATE structured_facts 
		SET content = ?, type = ?, importance = ?, last_updated = ? 
		WHERE id = ? AND user_id = ?
	`
	_, err := s.db.Exec(query, fact.Content, fact.Type, fact.Importance, fact.LastUpdated.UTC().Format("2006-01-02 15:04:05.999"), fact.ID, fact.UserID)
	return err
}

func (s *SQLiteStore) UpdateFactTopic(oldTopicID, newTopicID int64) error {
	query := "UPDATE structured_facts SET topic_id = ? WHERE topic_id = ?"
	_, err := s.db.Exec(query, newTopicID, oldTopicID)
	return err
}

func (s *SQLiteStore) DeleteFact(userID, id int64) error {
	query := "DELETE FROM structured_facts WHERE id = ? AND user_id = ?"
	_, err := s.db.Exec(query, id, userID)
	return err
}

func (s *SQLiteStore) GetFacts(userID int64) ([]Fact, error) {
	query := "SELECT id, user_id, entity, relation, category, content, type, importance, embedding, topic_id, created_at, last_updated FROM structured_facts WHERE user_id = ?"
	rows, err := s.db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var embBytes []byte
		if err := rows.Scan(&f.ID, &f.UserID, &f.Entity, &f.Relation, &f.Category, &f.Content, &f.Type, &f.Importance, &embBytes, &f.TopicID, &f.CreatedAt, &f.LastUpdated); err != nil {
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

func (s *SQLiteStore) GetFactsByIDs(ids []int64) ([]Fact, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query := "SELECT id, user_id, entity, relation, category, content, type, importance, embedding, topic_id, created_at, last_updated FROM structured_facts WHERE id IN (?" + strings.Repeat(",?", len(ids)-1) + ")"
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		var embBytes []byte
		if err := rows.Scan(&f.ID, &f.UserID, &f.Entity, &f.Relation, &f.Category, &f.Content, &f.Type, &f.Importance, &embBytes, &f.TopicID, &f.CreatedAt, &f.LastUpdated); err != nil {
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

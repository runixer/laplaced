package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (s *SQLiteStore) CreateTopic(topic Topic) (int64, error) {
	embBytes, err := json.Marshal(topic.Embedding)
	if err != nil {
		return 0, err
	}
	if topic.CreatedAt.IsZero() {
		topic.CreatedAt = time.Now()
	}
	query := "INSERT INTO topics (user_id, summary, start_msg_id, end_msg_id, embedding, facts_extracted, is_consolidated, consolidation_checked, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)"
	res, err := s.db.Exec(query, topic.UserID, topic.Summary, topic.StartMsgID, topic.EndMsgID, embBytes, topic.FactsExtracted, topic.IsConsolidated, topic.ConsolidationChecked, topic.CreatedAt.UTC().Format("2006-01-02 15:04:05.999"))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *SQLiteStore) AddTopic(topic Topic) (int64, error) {
	id, err := s.CreateTopic(topic)
	if err != nil {
		return 0, err
	}

	// Update messages in range?
	// The user said "for each message add identifier".
	// If we rely on start/end msg id, we can do it here.
	// UPDATE history SET topic_id = ? WHERE user_id = ? AND id >= ? AND id <= ?
	updateQuery := "UPDATE history SET topic_id = ? WHERE user_id = ? AND id >= ? AND id <= ?"
	_, err = s.db.Exec(updateQuery, id, topic.UserID, topic.StartMsgID, topic.EndMsgID)
	return id, err
}

func (s *SQLiteStore) DeleteTopic(id int64) error {
	query := "DELETE FROM topics WHERE id = ?"
	_, err := s.db.Exec(query, id)
	return err
}

func (s *SQLiteStore) DeleteTopicCascade(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	// Delete facts linked to this topic
	if _, err := tx.Exec("DELETE FROM structured_facts WHERE topic_id = ?", id); err != nil {
		_ = tx.Rollback()
		return err
	}

	// Delete history linked to this topic
	if _, err := tx.Exec("DELETE FROM fact_history WHERE topic_id = ?", id); err != nil {
		_ = tx.Rollback()
		return err
	}

	// Delete topic
	if _, err := tx.Exec("DELETE FROM topics WHERE id = ?", id); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (s *SQLiteStore) GetLastTopicEndMessageID(userID int64) (int64, error) {
	var maxID sql.NullInt64
	query := "SELECT MAX(end_msg_id) FROM topics WHERE user_id = ?"
	err := s.db.QueryRow(query, userID).Scan(&maxID)
	if err != nil {
		return 0, err
	}
	if maxID.Valid {
		return maxID.Int64, nil
	}
	return 0, nil
}

func (s *SQLiteStore) GetAllTopics() ([]Topic, error) {
	query := "SELECT id, user_id, summary, start_msg_id, end_msg_id, embedding, facts_extracted, is_consolidated, consolidation_checked, created_at FROM topics"
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var topics []Topic
	for rows.Next() {
		var t Topic
		var embBytes []byte
		if err := rows.Scan(&t.ID, &t.UserID, &t.Summary, &t.StartMsgID, &t.EndMsgID, &embBytes, &t.FactsExtracted, &t.IsConsolidated, &t.ConsolidationChecked, &t.CreatedAt); err != nil {
			return nil, err
		}
		if len(embBytes) > 0 {
			if err := json.Unmarshal(embBytes, &t.Embedding); err != nil {
				s.logger.Warn("failed to unmarshal topic embedding", "id", t.ID, "error", err)
				continue
			}
		}
		topics = append(topics, t)
	}
	return topics, nil
}

func (s *SQLiteStore) GetTopicsByIDs(ids []int64) ([]Topic, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(
		"SELECT id, user_id, summary, start_msg_id, end_msg_id, embedding, facts_extracted, is_consolidated, consolidation_checked, created_at FROM topics WHERE id IN (%s)",
		strings.Join(placeholders, ","),
	)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var topics []Topic
	for rows.Next() {
		var t Topic
		var embBytes []byte
		if err := rows.Scan(&t.ID, &t.UserID, &t.Summary, &t.StartMsgID, &t.EndMsgID, &embBytes, &t.FactsExtracted, &t.IsConsolidated, &t.ConsolidationChecked, &t.CreatedAt); err != nil {
			return nil, err
		}
		if len(embBytes) > 0 {
			if err := json.Unmarshal(embBytes, &t.Embedding); err != nil {
				s.logger.Warn("failed to unmarshal topic embedding", "id", t.ID, "error", err)
				continue
			}
		}
		topics = append(topics, t)
	}
	return topics, nil
}

func (s *SQLiteStore) GetTopics(userID int64) ([]Topic, error) {
	query := "SELECT id, user_id, summary, start_msg_id, end_msg_id, embedding, facts_extracted, is_consolidated, consolidation_checked, created_at FROM topics WHERE user_id = ?"
	rows, err := s.db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var topics []Topic
	for rows.Next() {
		var t Topic
		var embBytes []byte
		if err := rows.Scan(&t.ID, &t.UserID, &t.Summary, &t.StartMsgID, &t.EndMsgID, &embBytes, &t.FactsExtracted, &t.IsConsolidated, &t.ConsolidationChecked, &t.CreatedAt); err != nil {
			return nil, err
		}
		if len(embBytes) > 0 {
			if err := json.Unmarshal(embBytes, &t.Embedding); err != nil {
				s.logger.Warn("failed to unmarshal topic embedding", "id", t.ID, "error", err)
				continue
			}
		}
		topics = append(topics, t)
	}
	return topics, nil
}

func (s *SQLiteStore) SetTopicFactsExtracted(topicID int64, extracted bool) error {
	query := "UPDATE topics SET facts_extracted = ? WHERE id = ?"
	_, err := s.db.Exec(query, extracted, topicID)
	return err
}

func (s *SQLiteStore) SetTopicConsolidationChecked(topicID int64, checked bool) error {
	query := "UPDATE topics SET consolidation_checked = ? WHERE id = ?"
	_, err := s.db.Exec(query, checked, topicID)
	return err
}

func (s *SQLiteStore) GetTopicsPendingFacts(userID int64) ([]Topic, error) {
	query := "SELECT id, user_id, summary, start_msg_id, end_msg_id, embedding, facts_extracted, is_consolidated, consolidation_checked, created_at FROM topics WHERE user_id = ? AND facts_extracted = 0 ORDER BY created_at ASC, id ASC"
	rows, err := s.db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var topics []Topic
	for rows.Next() {
		var t Topic
		var embBytes []byte
		if err := rows.Scan(&t.ID, &t.UserID, &t.Summary, &t.StartMsgID, &t.EndMsgID, &embBytes, &t.FactsExtracted, &t.IsConsolidated, &t.ConsolidationChecked, &t.CreatedAt); err != nil {
			return nil, err
		}
		if len(embBytes) > 0 {
			if err := json.Unmarshal(embBytes, &t.Embedding); err != nil {
				s.logger.Warn("failed to unmarshal topic embedding", "id", t.ID, "error", err)
				continue
			}
		}
		topics = append(topics, t)
	}
	return topics, nil
}

func (s *SQLiteStore) GetTopicsExtended(filter TopicFilter, limit, offset int, sortBy, sortDir string) (TopicResult, error) {
	var whereClauses []string
	var args []interface{}

	if filter.UserID != 0 {
		whereClauses = append(whereClauses, "t.user_id = ?")
		args = append(args, filter.UserID)
	}
	if filter.Search != "" {
		whereClauses = append(whereClauses, "t.summary LIKE ?")
		args = append(args, "%"+filter.Search+"%")
	}
	if filter.IsConsolidated != nil {
		whereClauses = append(whereClauses, "t.is_consolidated = ?")
		args = append(args, *filter.IsConsolidated)
	}
	if filter.TopicID != nil {
		whereClauses = append(whereClauses, "t.id = ?")
		args = append(args, *filter.TopicID)
	}

	// For HasFacts, we need to check the count.
	// We can do this in HAVING or WHERE with subquery.
	// WHERE (SELECT COUNT(*) FROM structured_facts f WHERE f.topic_id = t.id) > 0
	if filter.HasFacts != nil {
		op := ">"
		if !*filter.HasFacts {
			op = "="
		}
		whereClauses = append(whereClauses, fmt.Sprintf("(SELECT COUNT(*) FROM structured_facts f WHERE f.topic_id = t.id) %s 0", op))
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Count total
	countQuery := "SELECT COUNT(*) FROM topics t " + whereSQL
	var totalCount int
	if err := s.db.QueryRow(countQuery, args...).Scan(&totalCount); err != nil {
		return TopicResult{}, err
	}

	// Sort
	validSortCols := map[string]string{
		"created_at":  "t.created_at",
		"size":        "message_count", // Alias from subquery
		"facts_count": "facts_count",   // Alias from subquery
		"summary":     "t.summary",
		"id":          "t.id",
	}

	sortCol, ok := validSortCols[sortBy]
	if !ok {
		sortCol = "t.created_at"
	}

	if sortDir != "ASC" && sortDir != "DESC" {
		sortDir = "DESC"
	}

	query := fmt.Sprintf(`
		SELECT 
			t.id, t.user_id, t.summary, t.start_msg_id, t.end_msg_id, t.embedding, t.facts_extracted, t.is_consolidated, t.consolidation_checked, t.created_at,
			(SELECT COUNT(*) FROM structured_facts f WHERE f.topic_id = t.id) as facts_count,
			(SELECT COUNT(*) FROM history h WHERE h.topic_id = t.id) as message_count
		FROM topics t
		%s
		ORDER BY %s %s
		LIMIT ? OFFSET ?
	`, whereSQL, sortCol, sortDir)

	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return TopicResult{}, err
	}
	defer rows.Close()

	var topics []TopicExtended
	for rows.Next() {
		var t TopicExtended
		var embBytes []byte
		if err := rows.Scan(
			&t.ID, &t.UserID, &t.Summary, &t.StartMsgID, &t.EndMsgID, &embBytes, &t.FactsExtracted, &t.IsConsolidated, &t.ConsolidationChecked, &t.CreatedAt,
			&t.FactsCount, &t.MessageCount,
		); err != nil {
			return TopicResult{}, err
		}
		if len(embBytes) > 0 {
			if err := json.Unmarshal(embBytes, &t.Embedding); err != nil {
				s.logger.Warn("failed to unmarshal topic embedding", "id", t.ID, "error", err)
				continue
			}
		}
		topics = append(topics, t)
	}

	return TopicResult{
		Data:       topics,
		TotalCount: totalCount,
	}, nil
}

func (s *SQLiteStore) GetMergeCandidates(userID int64) ([]MergeCandidate, error) {
	// Find pairs of topics that are:
	// 1. Same user
	// 2. Not checked for consolidation
	// 3. Close in message ID (heuristic for time proximity)
	// 4. t1 comes before t2
	query := `
		SELECT 
			t1.id, t1.user_id, t1.summary, t1.start_msg_id, t1.end_msg_id, t1.embedding, t1.facts_extracted, t1.is_consolidated, t1.consolidation_checked, t1.created_at,
			t2.id, t2.user_id, t2.summary, t2.start_msg_id, t2.end_msg_id, t2.embedding, t2.facts_extracted, t2.is_consolidated, t2.consolidation_checked, t2.created_at
		FROM topics t1
		JOIN topics t2 ON t1.user_id = t2.user_id
		WHERE t1.user_id = ?
		  AND t1.id < t2.id
		  AND t1.consolidation_checked = 0
		  AND t2.consolidation_checked = 0
		  AND (t2.start_msg_id - t1.end_msg_id) < 100
		  AND (t2.start_msg_id - t1.end_msg_id) > 0
		LIMIT 50
	`

	rows, err := s.db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []MergeCandidate
	for rows.Next() {
		var t1, t2 Topic
		var emb1, emb2 []byte

		if err := rows.Scan(
			&t1.ID, &t1.UserID, &t1.Summary, &t1.StartMsgID, &t1.EndMsgID, &emb1, &t1.FactsExtracted, &t1.IsConsolidated, &t1.ConsolidationChecked, &t1.CreatedAt,
			&t2.ID, &t2.UserID, &t2.Summary, &t2.StartMsgID, &t2.EndMsgID, &emb2, &t2.FactsExtracted, &t2.IsConsolidated, &t2.ConsolidationChecked, &t2.CreatedAt,
		); err != nil {
			return nil, err
		}

		if len(emb1) > 0 {
			_ = json.Unmarshal(emb1, &t1.Embedding)
		}
		if len(emb2) > 0 {
			_ = json.Unmarshal(emb2, &t2.Embedding)
		}

		candidates = append(candidates, MergeCandidate{Topic1: t1, Topic2: t2})
	}

	return candidates, nil
}

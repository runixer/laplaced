package storage

import (
	"os"
)

// TableSize represents the size of a database table.
type TableSize struct {
	Name  string
	Bytes int64
}

// GetDBSize returns the size of the database file in bytes.
func (s *SQLiteStore) GetDBSize() (int64, error) {
	info, err := os.Stat(s.dbPath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// GetTableSizes returns the size of each table in bytes using SQLite's dbstat virtual table.
func (s *SQLiteStore) GetTableSizes() ([]TableSize, error) {
	query := `
		SELECT name, SUM(pgsize) as size_bytes
		FROM dbstat
		WHERE name NOT LIKE 'sqlite_%'
		GROUP BY name
		ORDER BY size_bytes DESC
	`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sizes []TableSize
	for rows.Next() {
		var ts TableSize
		if err := rows.Scan(&ts.Name, &ts.Bytes); err != nil {
			return nil, err
		}
		sizes = append(sizes, ts)
	}
	return sizes, rows.Err()
}

// CleanupFactHistory removes old fact_history records, keeping only the N most recent per user.
// Returns the number of deleted rows.
func (s *SQLiteStore) CleanupFactHistory(keepPerUser int) (int64, error) {
	// Delete records that are not in the top N per user (by id DESC)
	query := `
		DELETE FROM fact_history
		WHERE id NOT IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY id DESC) as rn
				FROM fact_history
			) WHERE rn <= ?
		)
	`
	result, err := s.db.Exec(query, keepPerUser)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CleanupRagLogs removes old rag_logs records, keeping only the N most recent per user.
// Returns the number of deleted rows.
func (s *SQLiteStore) CleanupRagLogs(keepPerUser int) (int64, error) {
	// Delete records that are not in the top N per user (by id DESC)
	query := `
		DELETE FROM rag_logs
		WHERE id NOT IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY id DESC) as rn
				FROM rag_logs
			) WHERE rn <= ?
		)
	`
	result, err := s.db.Exec(query, keepPerUser)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CleanupRerankerLogs removes old reranker_logs records, keeping only the N most recent per user.
// Returns the number of deleted rows.
func (s *SQLiteStore) CleanupRerankerLogs(keepPerUser int) (int64, error) {
	query := `
		DELETE FROM reranker_logs
		WHERE id NOT IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY id DESC) as rn
				FROM reranker_logs
			) WHERE rn <= ?
		)
	`
	result, err := s.db.Exec(query, keepPerUser)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CountOrphanedTopics counts topics with no messages linked to them.
// If userID is 0, counts for all users.
func (s *SQLiteStore) CountOrphanedTopics(userID int64) (int, error) {
	var query string
	var args []any
	if userID == 0 {
		query = `
			SELECT COUNT(*) FROM topics t
			WHERE NOT EXISTS (SELECT 1 FROM history h WHERE h.topic_id = t.id)
		`
	} else {
		query = `
			SELECT COUNT(*) FROM topics t
			WHERE t.user_id = ?
			AND NOT EXISTS (SELECT 1 FROM history h WHERE h.topic_id = t.id)
		`
		args = append(args, userID)
	}
	var count int
	err := s.db.QueryRow(query, args...).Scan(&count)
	return count, err
}

// GetOrphanedTopicIDs returns IDs of topics with no messages linked.
// If userID is 0, returns for all users.
func (s *SQLiteStore) GetOrphanedTopicIDs(userID int64) ([]int64, error) {
	var query string
	var args []any
	if userID == 0 {
		query = `
			SELECT t.id FROM topics t
			WHERE NOT EXISTS (SELECT 1 FROM history h WHERE h.topic_id = t.id)
		`
	} else {
		query = `
			SELECT t.id FROM topics t
			WHERE t.user_id = ?
			AND NOT EXISTS (SELECT 1 FROM history h WHERE h.topic_id = t.id)
		`
		args = append(args, userID)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// OverlappingPair contains information about two overlapping topics.
type OverlappingPair struct {
	Topic1ID      int64
	Topic1Summary string
	Topic2ID      int64
	Topic2Summary string
}

// CountOverlappingTopics counts pairs of topics with overlapping message ranges.
// If userID is 0, counts for all users.
func (s *SQLiteStore) CountOverlappingTopics(userID int64) (int, error) {
	var query string
	var args []any
	if userID == 0 {
		query = `
			SELECT COUNT(*) FROM topics t1
			JOIN topics t2 ON t1.id < t2.id
				AND t1.user_id = t2.user_id
				AND NOT (t1.end_msg_id < t2.start_msg_id OR t1.start_msg_id > t2.end_msg_id)
		`
	} else {
		query = `
			SELECT COUNT(*) FROM topics t1
			JOIN topics t2 ON t1.id < t2.id
				AND t1.user_id = t2.user_id
				AND NOT (t1.end_msg_id < t2.start_msg_id OR t1.start_msg_id > t2.end_msg_id)
			WHERE t1.user_id = ?
		`
		args = append(args, userID)
	}
	var count int
	err := s.db.QueryRow(query, args...).Scan(&count)
	return count, err
}

// GetOverlappingTopics returns pairs of topics with overlapping message ranges.
// If userID is 0, returns for all users.
func (s *SQLiteStore) GetOverlappingTopics(userID int64) ([]OverlappingPair, error) {
	var query string
	var args []any
	if userID == 0 {
		query = `
			SELECT t1.id, t1.summary, t2.id, t2.summary
			FROM topics t1
			JOIN topics t2 ON t1.id < t2.id
				AND t1.user_id = t2.user_id
				AND NOT (t1.end_msg_id < t2.start_msg_id OR t1.start_msg_id > t2.end_msg_id)
			ORDER BY t1.id DESC
			LIMIT 50
		`
	} else {
		query = `
			SELECT t1.id, t1.summary, t2.id, t2.summary
			FROM topics t1
			JOIN topics t2 ON t1.id < t2.id
				AND t1.user_id = t2.user_id
				AND NOT (t1.end_msg_id < t2.start_msg_id OR t1.start_msg_id > t2.end_msg_id)
			WHERE t1.user_id = ?
			ORDER BY t1.id DESC
			LIMIT 50
		`
		args = append(args, userID)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pairs []OverlappingPair
	for rows.Next() {
		var p OverlappingPair
		if err := rows.Scan(&p.Topic1ID, &p.Topic1Summary, &p.Topic2ID, &p.Topic2Summary); err != nil {
			return nil, err
		}
		pairs = append(pairs, p)
	}
	return pairs, rows.Err()
}

// CountFactsOnOrphanedTopics counts facts linked to orphaned topics.
// If userID is 0, counts for all users.
func (s *SQLiteStore) CountFactsOnOrphanedTopics(userID int64) (int, error) {
	var query string
	var args []any
	if userID == 0 {
		query = `
			SELECT COUNT(*) FROM structured_facts f
			WHERE f.topic_id IN (
				SELECT t.id FROM topics t
				WHERE NOT EXISTS (SELECT 1 FROM history h WHERE h.topic_id = t.id)
			)
		`
	} else {
		query = `
			SELECT COUNT(*) FROM structured_facts f
			WHERE f.topic_id IN (
				SELECT t.id FROM topics t
				WHERE t.user_id = ?
				AND NOT EXISTS (SELECT 1 FROM history h WHERE h.topic_id = t.id)
			)
		`
		args = append(args, userID)
	}
	var count int
	err := s.db.QueryRow(query, args...).Scan(&count)
	return count, err
}

// RecalculateTopicRanges recalculates start_msg_id and end_msg_id for all topics based on actual message assignments.
// If userID is 0, recalculates for all users.
func (s *SQLiteStore) RecalculateTopicRanges(userID int64) (int, error) {
	var query string
	var args []any
	if userID == 0 {
		query = `
			UPDATE topics SET
				start_msg_id = COALESCE((SELECT MIN(h.id) FROM history h WHERE h.topic_id = topics.id), start_msg_id),
				end_msg_id = COALESCE((SELECT MAX(h.id) FROM history h WHERE h.topic_id = topics.id), end_msg_id)
		`
	} else {
		query = `
			UPDATE topics SET
				start_msg_id = COALESCE((SELECT MIN(h.id) FROM history h WHERE h.topic_id = topics.id), start_msg_id),
				end_msg_id = COALESCE((SELECT MAX(h.id) FROM history h WHERE h.topic_id = topics.id), end_msg_id)
			WHERE user_id = ?
		`
		args = append(args, userID)
	}
	result, err := s.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// RecalculateTopicSizes recalculates size_chars for all topics based on actual message content.
// If userID is 0, recalculates for all users.
func (s *SQLiteStore) RecalculateTopicSizes(userID int64) (int, error) {
	var query string
	var args []any
	if userID == 0 {
		query = `
			UPDATE topics SET size_chars = (
				SELECT COALESCE(SUM(LENGTH(h.content)), 0)
				FROM history h WHERE h.topic_id = topics.id
			)
		`
	} else {
		query = `
			UPDATE topics SET size_chars = (
				SELECT COALESCE(SUM(LENGTH(h.content)), 0)
				FROM history h WHERE h.topic_id = topics.id
			) WHERE user_id = ?
		`
		args = append(args, userID)
	}
	result, err := s.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// ContaminatedTopic represents a topic containing messages from other users.
type ContaminatedTopic struct {
	TopicID       int64   `json:"topic_id"`
	TopicOwner    int64   `json:"topic_owner"`
	TopicSummary  string  `json:"topic_summary"`
	ForeignUsers  []int64 `json:"foreign_users"`
	ForeignMsgCnt int     `json:"foreign_msg_count"`
	TotalMsgCnt   int     `json:"total_msg_count"`
}

// GetContaminatedTopics finds topics that contain messages from users other than the topic owner.
// If userID is 0, checks all topics; otherwise only topics owned by userID.
func (s *SQLiteStore) GetContaminatedTopics(userID int64) ([]ContaminatedTopic, error) {
	var query string
	var args []any
	if userID == 0 {
		query = `
			SELECT
				t.id,
				t.user_id,
				t.summary,
				GROUP_CONCAT(DISTINCT h.user_id) as all_users,
				COUNT(*) as total_msgs,
				SUM(CASE WHEN h.user_id != t.user_id THEN 1 ELSE 0 END) as foreign_msgs
			FROM topics t
			JOIN history h ON h.topic_id = t.id
			GROUP BY t.id
			HAVING foreign_msgs > 0
			ORDER BY foreign_msgs DESC
		`
	} else {
		query = `
			SELECT
				t.id,
				t.user_id,
				t.summary,
				GROUP_CONCAT(DISTINCT h.user_id) as all_users,
				COUNT(*) as total_msgs,
				SUM(CASE WHEN h.user_id != t.user_id THEN 1 ELSE 0 END) as foreign_msgs
			FROM topics t
			JOIN history h ON h.topic_id = t.id
			WHERE t.user_id = ?
			GROUP BY t.id
			HAVING foreign_msgs > 0
			ORDER BY foreign_msgs DESC
		`
		args = append(args, userID)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ContaminatedTopic
	for rows.Next() {
		var ct ContaminatedTopic
		var allUsersStr string
		if err := rows.Scan(&ct.TopicID, &ct.TopicOwner, &ct.TopicSummary, &allUsersStr, &ct.TotalMsgCnt, &ct.ForeignMsgCnt); err != nil {
			return nil, err
		}
		// Parse foreign users (exclude owner)
		for _, uidStr := range splitComma(allUsersStr) {
			var uid int64
			if _, err := parseID(uidStr, &uid); err == nil && uid != ct.TopicOwner {
				ct.ForeignUsers = append(ct.ForeignUsers, uid)
			}
		}
		results = append(results, ct)
	}
	return results, rows.Err()
}

// CountContaminatedTopics counts topics with cross-user contamination.
// If userID is 0, counts all; otherwise only for specified user.
func (s *SQLiteStore) CountContaminatedTopics(userID int64) (int, error) {
	var query string
	var args []any
	if userID == 0 {
		query = `
			SELECT COUNT(*) FROM (
				SELECT t.id
				FROM topics t
				JOIN history h ON h.topic_id = t.id
				GROUP BY t.id
				HAVING SUM(CASE WHEN h.user_id != t.user_id THEN 1 ELSE 0 END) > 0
			)
		`
	} else {
		query = `
			SELECT COUNT(*) FROM (
				SELECT t.id
				FROM topics t
				JOIN history h ON h.topic_id = t.id
				WHERE t.user_id = ?
				GROUP BY t.id
				HAVING SUM(CASE WHEN h.user_id != t.user_id THEN 1 ELSE 0 END) > 0
			)
		`
		args = append(args, userID)
	}
	var count int
	err := s.db.QueryRow(query, args...).Scan(&count)
	return count, err
}

// FixContaminatedTopics removes foreign messages from contaminated topics by setting their topic_id to NULL.
// Returns the number of messages unlinked.
// If userID is 0, fixes all; otherwise only topics owned by specified user.
func (s *SQLiteStore) FixContaminatedTopics(userID int64) (int64, error) {
	var query string
	var args []any
	if userID == 0 {
		query = `
			UPDATE history
			SET topic_id = NULL
			WHERE topic_id IN (
				SELECT t.id FROM topics t
				JOIN history h ON h.topic_id = t.id
				GROUP BY t.id
				HAVING SUM(CASE WHEN h.user_id != t.user_id THEN 1 ELSE 0 END) > 0
			)
			AND user_id != (SELECT user_id FROM topics WHERE id = history.topic_id)
		`
	} else {
		query = `
			UPDATE history
			SET topic_id = NULL
			WHERE topic_id IN (
				SELECT t.id FROM topics t
				JOIN history h ON h.topic_id = t.id
				WHERE t.user_id = ?
				GROUP BY t.id
				HAVING SUM(CASE WHEN h.user_id != t.user_id THEN 1 ELSE 0 END) > 0
			)
			AND user_id != (SELECT user_id FROM topics WHERE id = history.topic_id)
		`
		args = append(args, userID)
	}
	result, err := s.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Helper to split comma-separated string
func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	return result
}

// Helper to parse ID from string
func parseID(s string, id *int64) (int, error) {
	var n int
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			n = n*10 + int(s[i]-'0')
		}
	}
	*id = int64(n)
	return n, nil
}

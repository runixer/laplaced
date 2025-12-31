package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (s *SQLiteStore) AddFactHistory(h FactHistory) error {
	if h.CreatedAt.IsZero() {
		h.CreatedAt = time.Now()
	}
	query := `
		INSERT INTO fact_history (fact_id, user_id, action, old_content, new_content, reason, category, entity, relation, importance, topic_id, request_input, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.db.Exec(query, h.FactID, h.UserID, h.Action, h.OldContent, h.NewContent, h.Reason, h.Category, h.Entity, h.Relation, h.Importance, h.TopicID, h.RequestInput, h.CreatedAt.UTC().Format("2006-01-02 15:04:05.999"))
	return err
}

func (s *SQLiteStore) UpdateFactHistoryTopic(oldTopicID, newTopicID int64) error {
	query := "UPDATE fact_history SET topic_id = ? WHERE topic_id = ?"
	_, err := s.db.Exec(query, newTopicID, oldTopicID)
	return err
}

func (s *SQLiteStore) GetFactHistory(userID int64, limit int) ([]FactHistory, error) {
	var rows *sql.Rows
	var err error

	if userID != 0 {
		query := `
			SELECT id, fact_id, user_id, action, old_content, new_content, reason, category, entity, relation, importance, topic_id, request_input, created_at
			FROM fact_history
			WHERE user_id = ?
			ORDER BY created_at DESC
			LIMIT ?
		`
		rows, err = s.db.Query(query, userID, limit)
	} else {
		query := `
			SELECT id, fact_id, user_id, action, old_content, new_content, reason, category, entity, relation, importance, topic_id, request_input, created_at
			FROM fact_history
			ORDER BY created_at DESC
			LIMIT ?
		`
		rows, err = s.db.Query(query, limit)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []FactHistory
	for rows.Next() {
		var h FactHistory
		var oldContent, newContent, reason, category, entity, relation, requestInput sql.NullString
		if err := rows.Scan(&h.ID, &h.FactID, &h.UserID, &h.Action, &oldContent, &newContent, &reason, &category, &entity, &relation, &h.Importance, &h.TopicID, &requestInput, &h.CreatedAt); err != nil {
			return nil, err
		}
		if oldContent.Valid {
			h.OldContent = oldContent.String
		}
		if newContent.Valid {
			h.NewContent = newContent.String
		}
		if reason.Valid {
			h.Reason = reason.String
		}
		if category.Valid {
			h.Category = category.String
		}
		if entity.Valid {
			h.Entity = entity.String
		}
		if relation.Valid {
			h.Relation = relation.String
		}
		if requestInput.Valid {
			h.RequestInput = requestInput.String
		}
		history = append(history, h)
	}
	return history, nil
}

func (s *SQLiteStore) GetFactHistoryExtended(filter FactHistoryFilter, limit, offset int, sortBy, sortDir string) (FactHistoryResult, error) {
	var whereClauses []string
	var args []interface{}

	if filter.UserID != 0 {
		whereClauses = append(whereClauses, "user_id = ?")
		args = append(args, filter.UserID)
	}
	if filter.Action != "" {
		whereClauses = append(whereClauses, "action = ?")
		args = append(args, filter.Action)
	}
	if filter.Category != "" {
		whereClauses = append(whereClauses, "category = ?")
		args = append(args, filter.Category)
	}
	if filter.Search != "" {
		whereClauses = append(whereClauses, "(old_content LIKE ? OR new_content LIKE ? OR reason LIKE ? OR entity LIKE ?)")
		searchPattern := "%" + filter.Search + "%"
		args = append(args, searchPattern, searchPattern, searchPattern, searchPattern)
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Count total
	countQuery := "SELECT COUNT(*) FROM fact_history " + whereSQL
	var totalCount int
	if err := s.db.QueryRow(countQuery, args...).Scan(&totalCount); err != nil {
		return FactHistoryResult{}, err
	}

	// Sort
	validSortCols := map[string]bool{
		"created_at": true,
		"user_id":    true,
		"action":     true,
		"fact_id":    true,
		"category":   true,
		"entity":     true,
		"importance": true,
	}
	if !validSortCols[sortBy] {
		sortBy = "created_at"
	}
	if sortDir != "ASC" && sortDir != "DESC" {
		sortDir = "DESC"
	}

	query := fmt.Sprintf(`
		SELECT id, fact_id, user_id, action, old_content, new_content, reason, category, entity, relation, importance, topic_id, request_input, created_at
		FROM fact_history
		%s
		ORDER BY %s %s
		LIMIT ? OFFSET ?
	`, whereSQL, sortBy, sortDir)

	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return FactHistoryResult{}, err
	}
	defer rows.Close()

	var history []FactHistory
	for rows.Next() {
		var h FactHistory
		var oldContent, newContent, reason, category, entity, relation, requestInput sql.NullString
		if err := rows.Scan(&h.ID, &h.FactID, &h.UserID, &h.Action, &oldContent, &newContent, &reason, &category, &entity, &relation, &h.Importance, &h.TopicID, &requestInput, &h.CreatedAt); err != nil {
			return FactHistoryResult{}, err
		}
		if oldContent.Valid {
			h.OldContent = oldContent.String
		}
		if newContent.Valid {
			h.NewContent = newContent.String
		}
		if reason.Valid {
			h.Reason = reason.String
		}
		if category.Valid {
			h.Category = category.String
		}
		if entity.Valid {
			h.Entity = entity.String
		}
		if relation.Valid {
			h.Relation = relation.String
		}
		if requestInput.Valid {
			h.RequestInput = requestInput.String
		}
		history = append(history, h)
	}

	return FactHistoryResult{
		Data:       history,
		TotalCount: totalCount,
	}, nil
}

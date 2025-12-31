package storage

import (
	"database/sql"
)

func (s *SQLiteStore) GetMemoryBank(userID int64) (string, error) {
	query := "SELECT content FROM memory_bank WHERE user_id = ?"
	var content string
	err := s.db.QueryRow(query, userID).Scan(&content)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return content, nil
}

func (s *SQLiteStore) UpdateMemoryBank(userID int64, content string) error {
	query := `
		INSERT INTO memory_bank (user_id, content, updated_at) 
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(user_id) DO UPDATE SET 
			content = excluded.content,
			updated_at = CURRENT_TIMESTAMP
	`
	_, err := s.db.Exec(query, userID, content)
	return err
}

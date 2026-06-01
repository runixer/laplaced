package storage

import (
	"database/sql"
)

func (s *Store) GetMemoryBank(userID ScopeID) (string, error) {
	query := "SELECT content FROM memory_bank WHERE user_id = ?"
	var content string
	err := s.queryRow(query, userID).Scan(&content)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return content, nil
}

func (s *Store) UpdateMemoryBank(userID ScopeID, content string) error {
	query := `
		INSERT INTO memory_bank (user_id, content, updated_at) 
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(user_id) DO UPDATE SET 
			content = excluded.content,
			updated_at = CURRENT_TIMESTAMP
	`
	_, err := s.exec(query, userID, content)
	return err
}

package storage

import (
	"database/sql"
	"fmt"
	"time"
)

func (s *SQLiteStore) UpsertUser(user User) error {
	query := `
		INSERT INTO users (id, username, first_name, last_name, last_seen)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			username = excluded.username,
			first_name = excluded.first_name,
			last_name = excluded.last_name,
			last_seen = excluded.last_seen
	`
	if user.LastSeen.IsZero() {
		user.LastSeen = time.Now()
	}
	_, err := s.db.Exec(query, user.ID, user.Username, user.FirstName, user.LastName, user.LastSeen)
	return err
}

func (s *SQLiteStore) GetAllUsers() ([]User, error) {
	query := "SELECT id, username, first_name, last_name, last_seen FROM users ORDER BY last_seen DESC"
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	knownIDs := make(map[int64]bool)

	for rows.Next() {
		var u User
		var lastSeen sql.NullTime
		if err := rows.Scan(&u.ID, &u.Username, &u.FirstName, &u.LastName, &lastSeen); err != nil {
			return nil, err
		}
		if lastSeen.Valid {
			u.LastSeen = lastSeen.Time
		}
		users = append(users, u)
		knownIDs[u.ID] = true
	}

	// Find users from other tables that are not in users table
	// Check topics
	rowsTopics, err := s.db.Query("SELECT DISTINCT user_id FROM topics")
	if err == nil {
		defer rowsTopics.Close()
		for rowsTopics.Next() {
			var uid int64
			if err := rowsTopics.Scan(&uid); err == nil {
				if !knownIDs[uid] {
					users = append(users, User{
						ID:        uid,
						Username:  fmt.Sprintf("User %d", uid),
						FirstName: "Unknown",
						LastSeen:  time.Now(), // Approximate
					})
					knownIDs[uid] = true
				}
			}
		}
	}

	// Check facts
	rowsFacts, err := s.db.Query("SELECT DISTINCT user_id FROM structured_facts")
	if err == nil {
		defer rowsFacts.Close()
		for rowsFacts.Next() {
			var uid int64
			if err := rowsFacts.Scan(&uid); err == nil {
				if !knownIDs[uid] {
					users = append(users, User{
						ID:        uid,
						Username:  fmt.Sprintf("User %d", uid),
						FirstName: "Unknown",
						LastSeen:  time.Now(),
					})
					knownIDs[uid] = true
				}
			}
		}
	}

	// Check history
	rowsHistory, err := s.db.Query("SELECT DISTINCT user_id FROM history")
	if err == nil {
		defer rowsHistory.Close()
		for rowsHistory.Next() {
			var uid int64
			if err := rowsHistory.Scan(&uid); err == nil {
				if !knownIDs[uid] {
					users = append(users, User{
						ID:        uid,
						Username:  fmt.Sprintf("User %d", uid),
						FirstName: "Unknown",
						LastSeen:  time.Now(),
					})
					knownIDs[uid] = true
				}
			}
		}
	}

	return users, nil
}

func (s *SQLiteStore) ResetUserData(userID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	s.logger.Info("Resetting user data", "user_id", userID)

	// 1. Clear Topics
	if _, err := tx.Exec("DELETE FROM topics WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("delete topics: %w", err)
	}

	// 2. Clear Facts
	if _, err := tx.Exec("DELETE FROM structured_facts WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("delete structured_facts: %w", err)
	}

	// 3. Clear Fact History
	if _, err := tx.Exec("DELETE FROM fact_history WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("delete fact history: %w", err)
	}

	// 4. Clear RAG Logs
	if _, err := tx.Exec("DELETE FROM rag_logs WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("delete rag logs: %w", err)
	}

	// 5. Clear Memory Bank
	if _, err := tx.Exec("DELETE FROM memory_bank WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("delete memory bank: %w", err)
	}

	// 6. Reset History topic_id
	if _, err := tx.Exec("UPDATE history SET topic_id = NULL WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("reset history: %w", err)
	}

	return tx.Commit()
}

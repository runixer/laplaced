package storage

import (
	"context"
	"strings"
	"time"
)

func (s *SQLiteStore) AddMessageToHistory(userID int64, message Message) error {
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now()
	}
	query := "INSERT INTO history (user_id, role, content, topic_id, created_at) VALUES (?, ?, ?, ?, ?)"
	_, err := s.db.Exec(query, userID, message.Role, message.Content, message.TopicID, message.CreatedAt.UTC().Format("2006-01-02 15:04:05.999"))
	return err
}

func (s *SQLiteStore) ImportMessage(userID int64, message Message) error {
	query := "INSERT INTO history (user_id, role, content, created_at, topic_id) VALUES (?, ?, ?, ?, ?)"
	_, err := s.db.Exec(query, userID, message.Role, message.Content, message.CreatedAt.UTC().Format("2006-01-02 15:04:05.999"), message.TopicID)
	return err
}

func (s *SQLiteStore) GetRecentHistory(userID int64, limit int) ([]Message, error) {
	query := `SELECT id, role, content, topic_id FROM history WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`
	rows, err := s.db.Query(query, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []Message
	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.ID, &msg.Role, &msg.Content, &msg.TopicID); err != nil {
			return nil, err
		}
		history = append(history, msg)
	}

	// Reverse needed because we fetched DESC
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}

	return history, nil
}

func (s *SQLiteStore) GetMessagesByIDs(ids []int64) ([]Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	query := "SELECT id, user_id, role, content, created_at, topic_id FROM history WHERE id IN (?" + strings.Repeat(",?", len(ids)-1) + ")"
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.ID, &msg.UserID, &msg.Role, &msg.Content, &msg.CreatedAt, &msg.TopicID); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}

	// Restore order from ids
	msgMap := make(map[int64]Message)
	for _, m := range messages {
		msgMap[m.ID] = m
	}

	var orderedMessages []Message
	for _, id := range ids {
		if m, ok := msgMap[id]; ok {
			orderedMessages = append(orderedMessages, m)
		}
	}

	return orderedMessages, nil
}

func (s *SQLiteStore) ClearHistory(userID int64) error {
	query := "DELETE FROM history WHERE user_id = ?"
	s.logger.Info("clearing history for user", "user_id", userID)
	_, err := s.db.Exec(query, userID)
	return err
}

func (s *SQLiteStore) GetMessagesInRange(ctx context.Context, userID int64, startID, endID int64) ([]Message, error) {
	query := "SELECT id, user_id, role, content, created_at, topic_id FROM history WHERE user_id = ? AND id >= ? AND id <= ? ORDER BY created_at ASC, id ASC"
	rows, err := s.db.QueryContext(ctx, query, userID, startID, endID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.ID, &msg.UserID, &msg.Role, &msg.Content, &msg.CreatedAt, &msg.TopicID); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

func (s *SQLiteStore) GetUnprocessedMessages(userID int64) ([]Message, error) {
	// New logic: Get messages where topic_id IS NULL
	// But we also want to respect the "chunking" logic which might rely on time.
	// Actually, getting all unprocessed messages is fine, the caller (RAG) handles chunking.

	query := "SELECT id, user_id, role, content, created_at, topic_id FROM history WHERE user_id = ? AND topic_id IS NULL ORDER BY created_at ASC, id ASC"
	rows, err := s.db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.ID, &msg.UserID, &msg.Role, &msg.Content, &msg.CreatedAt, &msg.TopicID); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

func (s *SQLiteStore) UpdateMessageTopic(messageID, topicID int64) error {
	query := "UPDATE history SET topic_id = ? WHERE id = ?"
	_, err := s.db.Exec(query, topicID, messageID)
	return err
}

package storage

import (
	"context"
	"fmt"
	"time"
)

func (s *Store) AddMessageToHistory(userID ScopeID, message Message) error {
	_, err := s.AddMessageToHistoryReturningID(userID, message)
	return err
}

// AddMessageToHistoryReturningID inserts a history row and returns that exact
// row's id. This is the race-free primitive for associating a delivered reply;
// callers must not rediscover the row via recent-history ordering.
func (s *Store) AddMessageToHistoryReturningID(userID ScopeID, message Message) (int64, error) {
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now()
	}
	// author/message_id/conversation_id (migration 012) and thread_root (013) are
	// nullable transport attribution; they stay NULL when the caller leaves them
	// unset (DM/Telegram path). trace_id (migration 016) is set on assistant
	// replies so an inbound reaction can resolve the reply to its trace.
	query := "INSERT INTO history (user_id, role, content, topic_id, created_at, author, message_id, conversation_id, thread_root, trace_id, do_not_store) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
	id, err := s.insertReturningID(query, "id", userID, message.Role, message.Content, message.TopicID, s.dialect.BindTime(message.CreatedAt),
		message.Author, message.MessageID, message.ConversationID, message.ThreadRoot, message.TraceID, message.DoNotStore)
	if err != nil {
		return 0, fmt.Errorf("insert history: %w", err)
	}
	return id, nil
}

// SetReplyTransportID back-fills the transport-native message id on the assistant
// reply just stored for a turn, after it has been sent (the id isn't known at
// insert time). It targets the user's most recent assistant row that is still
// unlinked (message_id IS NULL) — which, because message-group turns are
// serialized per user, is exactly the reply we just inserted. Needs no row-id
// read and no trace_id, so it works whether or not tracing is enabled.
// user_id-scoped (Critical Data Invariant). No-op if there is no unlinked reply.
func (s *Store) SetReplyTransportID(userID ScopeID, transportMsgID string) error {
	query := `UPDATE history SET message_id = ?
		WHERE user_id = ? AND id = (
			SELECT MAX(id) FROM history
			WHERE user_id = ? AND role = 'assistant' AND message_id IS NULL
		)`
	_, err := s.exec(query, transportMsgID, userID, userID)
	return err
}

// GetReplyByTransportID returns the assistant reply for a given transport-native
// message id, or (nil, nil) if there is no match for this user. Used by the
// inbound-reaction handler to resolve a reacted message to its trace; a miss
// means the reaction was on a non-bot / unindexed message and is ignored.
func (s *Store) GetReplyByTransportID(userID ScopeID, transportMsgID string) (*Message, error) {
	query := `SELECT id, content, trace_id FROM history
		WHERE user_id = ? AND message_id = ? AND role = 'assistant'
		ORDER BY id DESC LIMIT 1`
	rows, err := s.query(query, userID, transportMsgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, rows.Err()
	}
	var msg Message
	msg.UserID = userID
	msg.Role = "assistant"
	if err := rows.Scan(&msg.ID, &msg.Content, &msg.TraceID); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (s *Store) ImportMessage(userID ScopeID, message Message) error {
	query := "INSERT INTO history (user_id, role, content, created_at, topic_id) VALUES (?, ?, ?, ?, ?)"
	_, err := s.exec(query, userID, message.Role, message.Content, s.dialect.BindTime(message.CreatedAt), message.TopicID)
	return err
}

func (s *Store) GetRecentHistory(userID ScopeID, limit int) ([]Message, error) {
	query := `SELECT id, role, content, topic_id FROM history WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`
	rows, err := s.query(query, userID, limit)
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

func (s *Store) GetMessagesByIDs(userID ScopeID, ids []int64) ([]Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	query, args, err := ExpandIn(
		"SELECT id, user_id, role, content, created_at, topic_id FROM history WHERE user_id = ? AND id IN (?)",
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

func (s *Store) ClearHistory(userID ScopeID) error {
	s.logger.Info("clearing history for user", "user_id", userID)
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("clear history: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := []string{
		`DELETE FROM outbound_delivery_messages WHERE delivery_id IN
			(SELECT id FROM outbound_deliveries WHERE user_id = ?)`,
		`DELETE FROM outbound_delivery_ops WHERE delivery_id IN
			(SELECT id FROM outbound_deliveries WHERE user_id = ?)`,
		`DELETE FROM outbound_deliveries WHERE user_id = ?`,
		`DELETE FROM history_transport_messages WHERE user_id = ?`,
		`DELETE FROM history_artifact_refs WHERE user_id = ?`,
		`DELETE FROM history WHERE user_id = ?`,
	}
	for _, query := range queries {
		if _, err := tx.Exec(s.rebind(query), userID); err != nil {
			return fmt.Errorf("clear history: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("clear history: commit: %w", err)
	}
	return nil
}

func (s *Store) GetMessagesInRange(ctx context.Context, userID ScopeID, startID, endID int64) ([]Message, error) {
	query := "SELECT id, user_id, role, content, created_at, topic_id FROM history WHERE user_id = ? AND id >= ? AND id <= ? ORDER BY created_at ASC, id ASC"
	rows, err := s.queryContext(ctx, query, userID, startID, endID)
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

func (s *Store) GetUnprocessedMessages(userID ScopeID) ([]Message, error) {
	// New logic: Get messages where topic_id IS NULL
	// But we also want to respect the "chunking" logic which might rely on time.
	// Actually, getting all unprocessed messages is fine, the caller (RAG) handles chunking.

	query := "SELECT id, user_id, role, content, created_at, topic_id, do_not_store FROM history WHERE user_id = ? AND topic_id IS NULL ORDER BY created_at ASC, id ASC"
	rows, err := s.query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.ID, &msg.UserID, &msg.Role, &msg.Content, &msg.CreatedAt, &msg.TopicID, &msg.DoNotStore); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

func (s *Store) UpdateMessageTopic(userID ScopeID, messageID, topicID int64) error {
	query := "UPDATE history SET topic_id = ? WHERE id = ? AND user_id = ?"
	_, err := s.exec(query, topicID, messageID, userID)
	return err
}

func (s *Store) GetMessagesByTopicID(ctx context.Context, topicID int64) ([]Message, error) {
	// do-not-store rows are structurally part of the topic (they carry its
	// topic_id) but their content must not resurface: this method feeds the
	// archivist, RAG topic re-injection, the merger, and topic views.
	query := "SELECT id, user_id, role, content, created_at, topic_id FROM history WHERE topic_id = ? AND NOT do_not_store ORDER BY created_at ASC, id ASC"
	rows, err := s.queryContext(ctx, query, topicID)
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

func (s *Store) UpdateMessagesTopicInRange(ctx context.Context, userID ScopeID, startMsgID, endMsgID, topicID int64) error {
	query := "UPDATE history SET topic_id = ? WHERE user_id = ? AND id >= ? AND id <= ?"
	_, err := s.execContext(ctx, query, topicID, userID, startMsgID, endMsgID)
	return err
}

// GetRecentSessionMessages returns the last N unprocessed messages (topic_id IS NULL) for artifact context (v0.6.0).
// Excludes messageIDs to avoid duplicates with MessageGroup messages.
func (s *Store) GetRecentSessionMessages(ctx context.Context, userID ScopeID, limit int, excludeIDs []int64) ([]Message, error) {
	if limit <= 0 {
		return nil, nil
	}

	var query string
	var args []any
	if len(excludeIDs) == 0 {
		query = `SELECT id, user_id, role, content, created_at, topic_id FROM history
			WHERE user_id = ? AND topic_id IS NULL
			ORDER BY created_at DESC, id DESC LIMIT ?`
		args = []any{userID, limit}
	} else {
		var err error
		query, args, err = ExpandIn(
			`SELECT id, user_id, role, content, created_at, topic_id FROM history
				WHERE user_id = ? AND topic_id IS NULL AND id NOT IN (?)
				ORDER BY created_at DESC, id DESC LIMIT ?`,
			userID, excludeIDs, limit,
		)
		if err != nil {
			return nil, err
		}
	}

	rows, err := s.queryContext(ctx, query, args...)
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

	// Reverse to get chronological order
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	return messages, nil
}

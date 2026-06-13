package storage

import (
	"fmt"
	"time"
)

// Flag is one reaction a user added to a bot reply, marking it as a bad
// response (migration 016). It carries the trace_id of the reply so the
// operator can jump straight to the trace that produced it.
type Flag struct {
	ID           int64
	UserID       ScopeID
	HistoryID    *int64  // history.id of the flagged assistant reply (nullable)
	MessageID    string  // transport-native message id the user reacted to
	TraceID      *string // trace that produced the reply (nullable)
	Emoji        string  // the reaction emoji
	ReplyPreview string  // truncated copy of the reply content
	CreatedAt    time.Time
}

// AddFlag records a response flag.
func (s *Store) AddFlag(flag Flag) error {
	if flag.CreatedAt.IsZero() {
		flag.CreatedAt = time.Now()
	}
	query := `INSERT INTO response_flags (user_id, history_id, message_id, trace_id, emoji, reply_preview, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := s.exec(query, flag.UserID, flag.HistoryID, flag.MessageID, flag.TraceID,
		flag.Emoji, flag.ReplyPreview, s.dialect.BindTime(flag.CreatedAt))
	if err != nil {
		return fmt.Errorf("failed to add response flag: %w", err)
	}
	return nil
}

// GetFlags returns the most recent flags for a user, newest first. If userID is
// empty, returns flags across all users (operator/debug listing).
func (s *Store) GetFlags(userID ScopeID, limit int) ([]Flag, error) {
	var query string
	var args []any
	if userID != "" {
		query = `SELECT id, user_id, history_id, message_id, trace_id, emoji, reply_preview, created_at
			FROM response_flags WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`
		args = []any{userID, limit}
	} else {
		query = `SELECT id, user_id, history_id, message_id, trace_id, emoji, reply_preview, created_at
			FROM response_flags ORDER BY created_at DESC, id DESC LIMIT ?`
		args = []any{limit}
	}

	rows, err := s.query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query response flags: %w", err)
	}
	defer rows.Close()

	var flags []Flag
	for rows.Next() {
		var f Flag
		if err := rows.Scan(&f.ID, &f.UserID, &f.HistoryID, &f.MessageID, &f.TraceID,
			&f.Emoji, &f.ReplyPreview, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan response flag: %w", err)
		}
		flags = append(flags, f)
	}
	return flags, rows.Err()
}

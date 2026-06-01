package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Channel is a channel scope: a conversation with many
// participants, as opposed to a person scope. Keyed deterministically by
// (transport, native_id); participants live as people within the scope.
type Channel struct {
	ScopeID     ScopeID
	Transport   string
	NativeID    string
	DisplayName string
	CreatedAt   time.Time
	LastSeen    time.Time
}

// GetOrCreateChannel returns the channel scope id for (transport, nativeID),
// creating the scopes + channels rows on first sight. The id is deterministic
// (PassthroughScopeID), so it matches what ResolveScope produces for a channel.
// displayName is refreshed when non-empty and never clobbered with "".
func (s *Store) GetOrCreateChannel(transport, nativeID, displayName string) (ScopeID, error) {
	sid := PassthroughScopeID(transport, nativeID)

	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("failed to begin channel tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(s.rebind(
		`INSERT INTO scopes (id, scope_type, display_name) VALUES (?, 'channel', ?)
		 ON CONFLICT(id) DO UPDATE SET
			display_name = COALESCE(NULLIF(excluded.display_name, ''), scopes.display_name),
			last_seen = CURRENT_TIMESTAMP`,
	), sid, nullableStr(displayName)); err != nil {
		return "", fmt.Errorf("failed to upsert channel scope %s: %w", sid, err)
	}
	if _, err := tx.Exec(s.rebind(
		`INSERT INTO channels (scope_id, transport, native_id, display_name) VALUES (?, ?, ?, ?)
		 ON CONFLICT(transport, native_id) DO UPDATE SET
			display_name = COALESCE(NULLIF(excluded.display_name, ''), channels.display_name),
			last_seen = CURRENT_TIMESTAMP`,
	), sid, transport, nativeID, nullableStr(displayName)); err != nil {
		return "", fmt.Errorf("failed to upsert channel %s/%s: %w", transport, nativeID, err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("failed to commit channel %s/%s: %w", transport, nativeID, err)
	}
	return sid, nil
}

// GetChannel returns the channel for a scope id, or nil if the scope is not a
// channel.
func (s *Store) GetChannel(scopeID ScopeID) (*Channel, error) {
	query := `
		SELECT scope_id, transport, native_id, display_name, created_at, last_seen
		FROM channels WHERE scope_id = ?
	`
	var c Channel
	var displayName sql.NullString
	var createdAt, lastSeen sql.NullTime
	err := s.queryRow(query, scopeID).Scan(
		&c.ScopeID, &c.Transport, &c.NativeID, &displayName, &createdAt, &lastSeen,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get channel %s: %w", scopeID, err)
	}
	c.DisplayName = displayName.String
	if createdAt.Valid {
		c.CreatedAt = createdAt.Time
	}
	if lastSeen.Valid {
		c.LastSeen = lastSeen.Time
	}
	return &c, nil
}

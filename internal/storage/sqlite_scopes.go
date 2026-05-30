package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Scope maps a transport-native identity to the internal int64 scope id used as
// the partition key (`user_id`) throughout storage. See migration 010.
//
// For Telegram the resolver short-circuits (internal_id == telegram id) and no
// row is ever written; rows here exist only for transports whose native ids are
// not int64 (e.g. Mattermost/Time 26-char user ids).
type Scope struct {
	ID         int64
	Transport  string
	ScopeType  string // "user" | "channel" (only "user" used in v0.10)
	NativeID   string
	InternalID int64
	CreatedAt  time.Time
	LastSeen   time.Time
}

// GetScope returns the scope for (transport, nativeID), or nil if none exists.
func (s *SQLiteStore) GetScope(transport, nativeID string) (*Scope, error) {
	query := `
		SELECT id, transport, scope_type, native_id, internal_id, created_at, last_seen
		FROM scopes WHERE transport = ? AND native_id = ?
	`
	var sc Scope
	var createdAt, lastSeen sql.NullTime
	err := s.db.QueryRow(query, transport, nativeID).Scan(
		&sc.ID, &sc.Transport, &sc.ScopeType, &sc.NativeID, &sc.InternalID, &createdAt, &lastSeen,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get scope %s/%s: %w", transport, nativeID, err)
	}
	if createdAt.Valid {
		sc.CreatedAt = createdAt.Time
	}
	if lastSeen.Valid {
		sc.LastSeen = lastSeen.Time
	}
	return &sc, nil
}

// ResolveScope returns the internal scope id for (transport, nativeID), minting
// a new surrogate on first sight. The surrogate is the row's own autoincrement
// id, so ids are stable and unique within the DB. Concurrency-safe: a racing
// insert that loses the UNIQUE(transport, native_id) constraint falls back to a
// re-read.
func (s *SQLiteStore) ResolveScope(transport, scopeType, nativeID string) (int64, error) {
	if existing, err := s.GetScope(transport, nativeID); err != nil {
		return 0, err
	} else if existing != nil {
		return existing.InternalID, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// internal_id is backfilled to the row id after insert (see below).
	res, err := tx.Exec(
		`INSERT INTO scopes (transport, scope_type, native_id, internal_id) VALUES (?, ?, ?, 0)`,
		transport, scopeType, nativeID,
	)
	if err != nil {
		// Lost a race: another writer inserted the same (transport, native_id).
		// Re-read outside this tx after rollback.
		_ = tx.Rollback()
		existing, getErr := s.GetScope(transport, nativeID)
		if getErr != nil {
			return 0, getErr
		}
		if existing != nil {
			return existing.InternalID, nil
		}
		return 0, fmt.Errorf("failed to insert scope %s/%s: %w", transport, nativeID, err)
	}

	rowID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE scopes SET internal_id = ? WHERE id = ?`, rowID, rowID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return rowID, nil
}

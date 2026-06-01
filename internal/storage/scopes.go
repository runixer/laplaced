package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// IsChannelScope reports whether id names a channel scope (scope_type='channel').
// A scope's type is fixed at mint time, so the result is memoized for the process
// lifetime. Absence of a row means a DM/person scope (Telegram passthrough writes
// no row; Mattermost DMs are scope_type='user'; principals are 'principal'), so
// it returns false.
func (s *Store) IsChannelScope(id ScopeID) (bool, error) {
	if v, ok := s.scopeTypeCache.Load(id); ok {
		return v.(bool), nil
	}
	var scopeType string
	err := s.queryRow(
		`SELECT scope_type FROM scopes WHERE id = ? LIMIT 1`, id,
	).Scan(&scopeType)
	if errors.Is(err, sql.ErrNoRows) {
		s.scopeTypeCache.Store(id, false)
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to read scope_type for %s: %w", id, err)
	}
	isChannel := scopeType == "channel"
	s.scopeTypeCache.Store(id, isChannel)
	return isChannel, nil
}

// Identity maps a transport-native handle to its scope. Many
// identities may point at one principal scope for unified cross-transport memory.
type Identity struct {
	Transport string
	NativeID  string
	ScopeID   ScopeID
	CreatedAt time.Time
	LastSeen  time.Time
}

// GetIdentity returns the identity row for (transport, nativeID), or nil if the
// handle has never been mapped to a scope.
func (s *Store) GetIdentity(transport, nativeID string) (*Identity, error) {
	query := `
		SELECT transport, native_id, scope_id, created_at, last_seen
		FROM identities WHERE transport = ? AND native_id = ?
	`
	var idn Identity
	var createdAt, lastSeen sql.NullTime
	err := s.queryRow(query, transport, nativeID).Scan(
		&idn.Transport, &idn.NativeID, &idn.ScopeID, &createdAt, &lastSeen,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get identity %s/%s: %w", transport, nativeID, err)
	}
	if createdAt.Valid {
		idn.CreatedAt = createdAt.Time
	}
	if lastSeen.Valid {
		idn.LastSeen = lastSeen.Time
	}
	return &idn, nil
}

// PutIdentity upserts the (transport, nativeID) → scopeID mapping. Re-pointing an
// existing handle to a different scope is allowed (the resolver owns that policy).
func (s *Store) PutIdentity(transport, nativeID string, scopeID ScopeID) error {
	_, err := s.exec(
		`INSERT INTO identities (transport, native_id, scope_id) VALUES (?, ?, ?)
		 ON CONFLICT(transport, native_id)
		 DO UPDATE SET scope_id = excluded.scope_id, last_seen = CURRENT_TIMESTAMP`,
		transport, nativeID, scopeID,
	)
	if err != nil {
		return fmt.Errorf("failed to put identity %s/%s: %w", transport, nativeID, err)
	}
	return nil
}

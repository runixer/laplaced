package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Principal is an AD-backed person scope. One principal is one
// human; many transport identities may map to a single principal scope so memory
// follows the person across transports.
type Principal struct {
	ScopeID     ScopeID
	ObjectGUID  string // AD anchor; "" until a Keycloak/LDAP lookup lands
	ADLogin     string // lowercase preferred_username; the external join key
	Email       string
	DisplayName string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PrincipalInput carries the resolved attributes used to dedup-or-create a
// principal. Dedup prefers ObjectGUID (stable across logon renames) and falls
// back to ADLogin. Empty fields are stored as NULL so the partial unique indexes
// on object_guid / ad_login do not treat unknown values as collisions.
type PrincipalInput struct {
	ObjectGUID  string
	ADLogin     string
	Email       string
	DisplayName string
}

// nullableStr binds "" as SQL NULL, so empty optional columns stay NULL (and the
// partial unique indexes on object_guid / ad_login ignore them).
func nullableStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// GetOrCreatePrincipal returns the scope id for the person described by in,
// creating a fresh principal scope if none matches. Dedup order: object_guid
// (when present) then ad_login. On a match, missing attributes are backfilled
// (notably object_guid arriving later) without clobbering existing values
// with empties. The bool reports whether a new principal was created.
func (s *Store) GetOrCreatePrincipal(in PrincipalInput) (ScopeID, bool, error) {
	// Fast path: an existing principal matches a dedup key.
	if sid, found, err := s.findPrincipal(in); err != nil || found {
		return sid, false, err
	}

	// Create. Both INSERTs share one tx, so a unique-index violation rolls both
	// back (no orphaned scopes row). On failure we re-run the lookup: a concurrent
	// writer may have created the principal between our lookup and INSERT, and the
	// partial-unique indexes on object_guid/ad_login would reject the duplicate —
	// treat that as a hit rather than surfacing a spurious error. This is
	// dialect-agnostic (no error-string parsing) and idempotent under races.
	sid := MintScopeID()
	if err := s.insertPrincipal(sid, in); err != nil {
		if sid2, found, lookErr := s.findPrincipal(in); lookErr == nil && found {
			return sid2, false, nil
		}
		return "", false, err
	}
	return sid, true, nil
}

// findPrincipal looks a principal up by object_guid then ad_login, backfilling
// late-arriving attributes on a hit.
func (s *Store) findPrincipal(in PrincipalInput) (ScopeID, bool, error) {
	for _, k := range []struct{ col, val string }{
		{"object_guid", in.ObjectGUID},
		{"ad_login", in.ADLogin},
	} {
		if k.val == "" {
			continue
		}
		sid, ok, err := s.lookupPrincipal(k.col, k.val)
		if err != nil {
			return "", false, err
		}
		if ok {
			if err := s.backfillPrincipal(sid, in); err != nil {
				return "", false, err
			}
			return sid, true, nil
		}
	}
	return "", false, nil
}

// insertPrincipal writes the scopes + principals rows for a fresh principal in a
// single transaction (so a unique violation leaves no orphaned scope).
func (s *Store) insertPrincipal(sid ScopeID, in PrincipalInput) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin principal tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(s.rebind(
		`INSERT INTO scopes (id, scope_type, display_name) VALUES (?, 'principal', ?)`,
	), sid, nullableStr(in.DisplayName)); err != nil {
		return fmt.Errorf("failed to insert principal scope: %w", err)
	}
	if _, err := tx.Exec(s.rebind(
		`INSERT INTO principals (scope_id, object_guid, ad_login, email, display_name)
		 VALUES (?, ?, ?, ?, ?)`,
	), sid, nullableStr(in.ObjectGUID), nullableStr(in.ADLogin),
		nullableStr(in.Email), nullableStr(in.DisplayName)); err != nil {
		return fmt.Errorf("failed to insert principal: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit principal: %w", err)
	}
	return nil
}

// lookupPrincipal finds a principal scope by one of its dedup keys. col is an
// internal constant ("object_guid" | "ad_login"), never user input.
func (s *Store) lookupPrincipal(col, val string) (ScopeID, bool, error) {
	var sid ScopeID
	err := s.queryRow(
		"SELECT scope_id FROM principals WHERE "+col+" = ?", val,
	).Scan(&sid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to look up principal by %s: %w", col, err)
	}
	return sid, true, nil
}

// backfillPrincipal fills in attributes that arrive after creation (e.g. an
// object_guid resolved later) without overwriting existing values with empties.
func (s *Store) backfillPrincipal(sid ScopeID, in PrincipalInput) error {
	_, err := s.exec(
		`UPDATE principals SET
			object_guid  = COALESCE(NULLIF(?, ''), object_guid),
			ad_login     = COALESCE(NULLIF(?, ''), ad_login),
			email        = COALESCE(NULLIF(?, ''), email),
			display_name = COALESCE(NULLIF(?, ''), display_name),
			updated_at   = CURRENT_TIMESTAMP
		 WHERE scope_id = ?`,
		in.ObjectGUID, in.ADLogin, in.Email, in.DisplayName, sid,
	)
	if err != nil {
		return fmt.Errorf("failed to backfill principal %s: %w", sid, err)
	}
	return nil
}

// GetPrincipal returns the principal for a scope id, or nil if the scope is not a
// principal.
func (s *Store) GetPrincipal(scopeID ScopeID) (*Principal, error) {
	query := `
		SELECT scope_id, object_guid, ad_login, email, display_name, created_at, updated_at
		FROM principals WHERE scope_id = ?
	`
	var p Principal
	var objectGUID, adLogin, email, displayName sql.NullString
	var createdAt, updatedAt sql.NullTime
	err := s.queryRow(query, scopeID).Scan(
		&p.ScopeID, &objectGUID, &adLogin, &email, &displayName, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get principal %s: %w", scopeID, err)
	}
	p.ObjectGUID = objectGUID.String
	p.ADLogin = adLogin.String
	p.Email = email.String
	p.DisplayName = displayName.String
	if createdAt.Valid {
		p.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		p.UpdatedAt = updatedAt.Time
	}
	return &p, nil
}

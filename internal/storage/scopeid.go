package storage

import (
	"database/sql/driver"
	"fmt"
	"strconv"

	"github.com/google/uuid"
)

// ScopeID is the memory partition key (tenant): an opaque UUID identifying a
// scope — a person (principal), a channel, or a passthrough identity. It
// replaces the former int64 user_id as the partition key throughout storage.
//
// Representation: a UUID rendered as the canonical 36-char lowercase string.
// On Postgres the physical column is `uuid`; on SQLite it is TEXT. The physical
// column is still named `user_id` (renaming it across ~180 shared SQL statements
// is deferred) — only the Go type and the stored values change.
//
// IMPORTANT: only the *partition* key is a ScopeID. Entity ids — message,
// topic, fact, person, artifact ids, and `people.telegram_id` — remain int64.
type ScopeID string

// String returns the underlying UUID string (handy for metric labels / JSON).
func (s ScopeID) String() string { return string(s) }

// IsZero reports whether the scope id is unset.
func (s ScopeID) IsZero() bool { return s == "" }

// Value implements driver.Valuer so ScopeID is bound as a string parameter.
func (s ScopeID) Value() (driver.Value, error) { return string(s), nil }

// Scan implements sql.Scanner. SQLite's INTEGER affinity coerces numeric scope
// strings (e.g. "123") to integers on storage, so a scope column may scan back
// as int64; UUID scopes scan as string/[]byte. Handle all three so round-trips
// are lossless on both SQLite and Postgres (uuid → string).
func (s *ScopeID) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*s = ""
	case string:
		*s = ScopeID(v)
	case []byte:
		*s = ScopeID(v)
	case int64:
		*s = ScopeID(strconv.FormatInt(v, 10))
	default:
		return fmt.Errorf("cannot scan %T into ScopeID", src)
	}
	return nil
}

// scopeNamespace anchors deterministic passthrough scope ids (uuidv5). This is a
// fixed, arbitrary UUID dedicated to laplaced scopes. DO NOT change it: doing so
// would re-key every passthrough scope and orphan all existing memory.
var scopeNamespace = uuid.MustParse("6f9c1d2e-7a3b-5c4d-8e1f-0a2b3c4d5e6f")

// PassthroughScopeID derives the deterministic scope id for a transport-native
// identity that has no principal resolution — the Telegram path and any
// transport without a configured PrincipalResolver. uuidv5 makes it stable and
// lookup-free: the same (transport, nativeID) always maps to the same scope, so
// no scopes/identities row is needed to recover it.
//
// The one-off Telegram int64→UUID migration MUST use this same function with
// transport "telegram" and the decimal id, or existing memory orphans.
//
// Channel scopes also use this (a channel is keyed deterministically by its
// (transport, native_id)); only principal scopes get a freshly minted id.
func PassthroughScopeID(transport, nativeID string) ScopeID {
	return ScopeID(uuid.NewSHA1(scopeNamespace, []byte(transport+":"+nativeID)).String())
}

// MintScopeID generates a fresh random scope id for a principal — a scope that
// spans many transport identities and is therefore not derivable from any single
// (transport, native_id). Principal dedup happens via the principals table
// (object_guid / ad_login lookup), so the id itself need not be deterministic.
func MintScopeID() ScopeID {
	return ScopeID(uuid.New().String())
}

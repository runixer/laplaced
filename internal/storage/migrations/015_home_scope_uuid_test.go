package migrations

import (
	"database/sql"
	"testing"
)

// seedPreVariantCRow inserts a row keyed by a raw int64 user_id (the legacy int64
// shape) so we can verify the rewrite.
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func scopeOf(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var v string
	if err := db.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return v
}

func TestMigration015_HomeScopeUUID(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Full chain first (015 runs on empty tables → no-op).
	runner := NewRunner(db, testLogger())
	if err := runner.Run(); err != nil {
		t.Fatalf("Run() failed: %v", err)
	}

	// Seed legacy int64 data: raw int64 user_ids across several partition tables,
	// two distinct Telegram users, plus one row already migrated to a UUID.
	const userA, userB = "123456789", "987654321"
	alreadyUUID := PassthroughTelegramScope("555")

	mustExec(t, db, "INSERT INTO users (id, username) VALUES (?, 'a'), (?, 'b'), (?, 'c')", userA, userB, alreadyUUID)
	mustExec(t, db, "INSERT INTO history (user_id, role, content) VALUES (?, 'user', 'hi'), (?, 'user', 'yo')", userA, userB)
	mustExec(t, db, "INSERT INTO memory_bank (user_id, content) VALUES (?, 'mb')", userA)
	mustExec(t, db, "INSERT INTO structured_facts (user_id, relation, category, content, type, importance) VALUES (?, 'is', 'identity', 'x', 'identity', 90)", userA)
	mustExec(t, db, "INSERT INTO topics (user_id, summary, start_msg_id, end_msg_id) VALUES (?, 's', 1, 1)", userB)

	// Run the rewrite (simulating the prod upgrade on populated int64 data).
	runRewrite := func() {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := migrateHomeScopeUUID(tx); err != nil {
			t.Fatalf("migrateHomeScopeUUID: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	runRewrite()

	wantA := PassthroughTelegramScope(userA)
	wantB := PassthroughTelegramScope(userB)

	// Partition keys rewritten to the deterministic UUIDs.
	if got := scopeOf(t, db, "SELECT id FROM users WHERE username='a'"); got != wantA {
		t.Errorf("users.a id = %q, want %q", got, wantA)
	}
	if got := scopeOf(t, db, "SELECT user_id FROM history WHERE content='hi'"); got != wantA {
		t.Errorf("history.hi user_id = %q, want %q", got, wantA)
	}
	if got := scopeOf(t, db, "SELECT user_id FROM memory_bank WHERE content='mb'"); got != wantA {
		t.Errorf("memory_bank user_id = %q, want %q", got, wantA)
	}
	if got := scopeOf(t, db, "SELECT user_id FROM structured_facts WHERE content='x'"); got != wantA {
		t.Errorf("structured_facts user_id = %q, want %q", got, wantA)
	}
	if got := scopeOf(t, db, "SELECT user_id FROM topics WHERE summary='s'"); got != wantB {
		t.Errorf("topics user_id = %q, want %q", got, wantB)
	}

	// The already-UUID row is untouched.
	if got := scopeOf(t, db, "SELECT id FROM users WHERE username='c'"); got != alreadyUUID {
		t.Errorf("already-uuid row changed: got %q, want %q", got, alreadyUUID)
	}

	// Query-path guard (not just schema state): retrieving by the scope id the
	// running code computes (PassthroughTelegramScope == storage.PassthroughScopeID)
	// must find the migrated row. This catches a shape mismatch that schema-only
	// assertions would miss.
	if got := scopeOf(t, db, "SELECT content FROM history WHERE user_id = ?", wantA); got != "hi" {
		t.Errorf("retrieval by computed scope id failed: got %q, want %q", got, "hi")
	}

	// Idempotent: a second pass must not re-transform the now-UUID values.
	runRewrite()
	if got := scopeOf(t, db, "SELECT id FROM users WHERE username='a'"); got != wantA {
		t.Errorf("after re-run users.a id = %q, want %q (not re-keyed)", got, wantA)
	}
	if got := scopeOf(t, db, "SELECT user_id FROM history WHERE content='hi'"); got != wantA {
		t.Errorf("after re-run history.hi user_id = %q, want %q", got, wantA)
	}
}

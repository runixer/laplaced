package storage

import (
	"testing"

	"github.com/runixer/laplaced/internal/storage/migrations"
)

// TestMigration015NamespaceMatchesPassthrough pins migration 015's duplicated
// uuidv5 namespace to storage.PassthroughScopeID. The migrations package cannot
// import storage (import cycle), so the namespace constant is duplicated there;
// if it ever drifts, the home scope rewrite would map int64 ids to UUIDs that the
// running code never computes — orphaning all home memory. This test fails the
// build on any drift.
func TestMigration015NamespaceMatchesPassthrough(t *testing.T) {
	for _, id := range []string{"1", "123", "123456789", "999999999999"} {
		want := string(PassthroughScopeID("telegram", id))
		got := migrations.PassthroughTelegramScope(id)
		if got != want {
			t.Errorf("migration 015 scope for %q = %q, want %q (namespace drift!)", id, got, want)
		}
	}
}

package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmbeddingVersion(t *testing.T) {
	assert.Equal(t, "m:1536", EmbeddingVersion("m", 1536))
	assert.Equal(t, "m", EmbeddingVersion("m", 0), "dim 0 means provider default")
}

// TestEmbeddingVersion_StampedOnWrite guards the invariant that every write
// path carrying an embedding also stamps embedding_version, so freshly
// written rows do not queue for the startup re-embed migration. Regression:
// before this, only the migration itself wrote the stamp, and every restart
// re-embedded all rows created since the previous one.
func TestEmbeddingVersion_StampedOnWrite(t *testing.T) {
	const current = "test-model:1536"
	const other = "other-model:3072"
	userID := ScopeID("123")
	emb := []float32{0.1, 0.2, 0.3}

	setup := func(t *testing.T) (*Store, func()) {
		t.Helper()
		store, cleanup := setupTestDB(t)
		require.NoError(t, store.Init())
		store.SetEmbeddingVersion(current)
		return store, cleanup
	}

	// assertStamped: no candidates under the current version, exactly one
	// under a different version.
	assertStamped := func(t *testing.T, fetch func(string, int) ([]ReembedCandidate, error)) {
		t.Helper()
		cur, err := fetch(current, 0)
		require.NoError(t, err)
		assert.Empty(t, cur, "row written under the current version must not need re-embed")
		oth, err := fetch(other, 0)
		require.NoError(t, err)
		assert.Len(t, oth, 1, "row must qualify for re-embed under a different version")
	}

	t.Run("AddTopic", func(t *testing.T) {
		store, cleanup := setup(t)
		defer cleanup()
		_, err := store.AddTopic(Topic{UserID: userID, Summary: "s", StartMsgID: 1, EndMsgID: 2, Embedding: emb})
		require.NoError(t, err)
		assertStamped(t, store.GetTopicsNeedingReembed)
	})

	t.Run("AddFact and UpdateFact", func(t *testing.T) {
		store, cleanup := setup(t)
		defer cleanup()
		id, err := store.AddFact(Fact{UserID: userID, Relation: "user", Category: "c", Content: "likes tea", Type: "preference", Importance: 50, Embedding: emb})
		require.NoError(t, err)
		assertStamped(t, store.GetFactsNeedingReembed)

		// An update must re-stamp, not leave a stale version behind.
		require.NoError(t, store.UpdateFactEmbeddingVersion(id, emb, "stale"))
		require.NoError(t, store.UpdateFact(Fact{ID: id, UserID: userID, Content: "likes coffee", Type: "preference", Importance: 50, Embedding: emb}))
		assertStamped(t, store.GetFactsNeedingReembed)
	})

	t.Run("AddPerson and UpdatePerson", func(t *testing.T) {
		store, cleanup := setup(t)
		defer cleanup()
		id, err := store.AddPerson(Person{UserID: userID, DisplayName: "John", Embedding: emb})
		require.NoError(t, err)
		assertStamped(t, store.GetPeopleNeedingReembed)

		require.NoError(t, store.UpdatePersonEmbeddingVersion(id, emb, "stale"))
		require.NoError(t, store.UpdatePerson(Person{ID: id, UserID: userID, DisplayName: "John D", Embedding: emb}))
		assertStamped(t, store.GetPeopleNeedingReembed)
	})

	t.Run("UpdateArtifact", func(t *testing.T) {
		store, cleanup := setup(t)
		defer cleanup()
		id, err := store.AddArtifact(Artifact{UserID: userID, MessageID: 1, FileType: "image", FilePath: "p", FileSize: 1, MimeType: "image/jpeg", ContentHash: "h", State: "pending"})
		require.NoError(t, err)

		summary := "a photo"
		require.NoError(t, store.UpdateArtifact(Artifact{ID: id, UserID: userID, State: "ready", Summary: &summary, Embedding: emb}))
		assertStamped(t, store.GetArtifactsNeedingReembed)
	})
}

package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComposePersonEmbeddingText(t *testing.T) {
	user := "johndoe"
	tests := []struct {
		name        string
		displayName string
		username    *string
		aliases     []string
		bio         string
		want        string
	}{
		{"all fields", "John Doe", &user, []string{"Johnny"}, "friend", "John Doe johndoe Johnny friend"},
		{"no username", "John Doe", nil, []string{"Johnny"}, "friend", "John Doe Johnny friend"},
		{"empty username", "John Doe", Ptr(""), nil, "", "John Doe"},
		{"name only", "John Doe", nil, nil, "", "John Doe"},
		{"skips empty aliases", "John", nil, []string{"", "Jo"}, "", "John Jo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ComposePersonEmbeddingText(tt.displayName, tt.username, tt.aliases, tt.bio))
		})
	}
}

// Ptr is a tiny local helper to take the address of a string literal.
func Ptr[T any](v T) *T { return &v }

// TestGetPeopleNeedingReembed_IncludesUsername guards the composition-drift
// bug: the re-embed text MUST include username, exactly as the live write path
// does. When it didn't, every person with a username re-embedded into a
// slightly different vector than the one stored at write time.
func TestGetPeopleNeedingReembed_IncludesUsername(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	username := "johndoe"
	_, err := store.AddPerson(Person{
		UserID:      ScopeID("123"),
		DisplayName: "John Doe",
		Username:    &username,
		Aliases:     []string{"Johnny"},
		Bio:         "a friend",
		Embedding:   []float32{0.1, 0.2, 0.3},
		// embedding_version left unset (NULL) so the row qualifies for re-embed.
	})
	require.NoError(t, err)

	cands, err := store.GetPeopleNeedingReembed("google/gemini-embedding-2:1536", 0)
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.Equal(t, "John Doe johndoe Johnny a friend", cands[0].Content,
		"re-embed text must match the live composer (username included)")
}

package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScopes_ResolveAndGet(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	// Unknown scope returns nil, no error.
	sc, err := store.GetScope("time", "u26charstring")
	require.NoError(t, err)
	assert.Nil(t, sc)

	// First resolve mints a surrogate int64.
	id1, err := store.ResolveScope("time", "user", "u26charstring")
	require.NoError(t, err)
	assert.Greater(t, id1, int64(0))

	// Same native id resolves to the same internal id (stable, no new row).
	id2, err := store.ResolveScope("time", "user", "u26charstring")
	require.NoError(t, err)
	assert.Equal(t, id1, id2)

	// A different native id gets a distinct surrogate.
	idOther, err := store.ResolveScope("time", "user", "another-user")
	require.NoError(t, err)
	assert.NotEqual(t, id1, idOther)

	// GetScope now returns the persisted row.
	sc, err = store.GetScope("time", "u26charstring")
	require.NoError(t, err)
	require.NotNil(t, sc)
	assert.Equal(t, "time", sc.Transport)
	assert.Equal(t, "user", sc.ScopeType)
	assert.Equal(t, "u26charstring", sc.NativeID)
	assert.Equal(t, id1, sc.InternalID)
}

func TestScopes_IsChannelScope(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	userID, err := store.ResolveScope("time", "user", "u26charstring")
	require.NoError(t, err)
	channelID, err := store.ResolveScope("time", "channel", "chan26char")
	require.NoError(t, err)

	// Channel scope reports true; user scope and an unknown id report false.
	isCh, err := store.IsChannelScope(channelID)
	require.NoError(t, err)
	assert.True(t, isCh, "channel scope should report channel")

	isCh, err = store.IsChannelScope(userID)
	require.NoError(t, err)
	assert.False(t, isCh, "user scope should not report channel")

	// Absent row (e.g. Telegram passthrough id) defaults to DM (false).
	isCh, err = store.IsChannelScope(999999)
	require.NoError(t, err)
	assert.False(t, isCh, "unknown scope id should default to DM")
}

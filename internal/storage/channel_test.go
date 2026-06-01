package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannel_GetOrCreate(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	sid, err := store.GetOrCreateChannel("mattermost", "chan26char", "#general")
	require.NoError(t, err)
	assert.NotEmpty(t, sid)
	// Deterministic id — matches ResolveScope's channel id.
	assert.Equal(t, PassthroughScopeID("mattermost", "chan26char"), sid)

	// A channel scope reports as a channel.
	isCh, err := store.IsChannelScope(sid)
	require.NoError(t, err)
	assert.True(t, isCh)

	// Idempotent: same channel resolves to the same scope.
	again, err := store.GetOrCreateChannel("mattermost", "chan26char", "#general")
	require.NoError(t, err)
	assert.Equal(t, sid, again)

	ch, err := store.GetChannel(sid)
	require.NoError(t, err)
	require.NotNil(t, ch)
	assert.Equal(t, "mattermost", ch.Transport)
	assert.Equal(t, "chan26char", ch.NativeID)
	assert.Equal(t, "#general", ch.DisplayName)
}

func TestChannel_DisplayNameRefreshNotClobbered(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	sid, err := store.GetOrCreateChannel("mattermost", "c1", "#general")
	require.NoError(t, err)

	// An empty display name must not wipe the stored one.
	_, err = store.GetOrCreateChannel("mattermost", "c1", "")
	require.NoError(t, err)
	ch, err := store.GetChannel(sid)
	require.NoError(t, err)
	assert.Equal(t, "#general", ch.DisplayName, "empty name should not clobber")

	// A new non-empty name refreshes it.
	_, err = store.GetOrCreateChannel("mattermost", "c1", "#renamed")
	require.NoError(t, err)
	ch, err = store.GetChannel(sid)
	require.NoError(t, err)
	assert.Equal(t, "#renamed", ch.DisplayName)
}

func TestChannel_GetChannel_NotAChannel(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	ch, err := store.GetChannel("nope")
	require.NoError(t, err)
	assert.Nil(t, ch)
}

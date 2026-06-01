package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScopes_IsChannelScope(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	// A channel scope (written by GetOrCreateChannel, scope_type='channel').
	channelID, err := store.GetOrCreateChannel("mattermost", "chan26char", "#general")
	require.NoError(t, err)

	isCh, err := store.IsChannelScope(channelID)
	require.NoError(t, err)
	assert.True(t, isCh, "channel scope should report channel")

	// A passthrough DM id (no scopes row) defaults to DM/person (false).
	isCh, err = store.IsChannelScope(PassthroughScopeID("mattermost", "u26charstring"))
	require.NoError(t, err)
	assert.False(t, isCh, "passthrough DM scope should not report channel")

	// An unknown id also defaults to false.
	isCh, err = store.IsChannelScope("999999")
	require.NoError(t, err)
	assert.False(t, isCh, "unknown scope id should default to DM")
}

func TestIdentities_GetAndPut(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	// Unknown handle returns nil, no error.
	idn, err := store.GetIdentity("mattermost", "u1")
	require.NoError(t, err)
	assert.Nil(t, idn)

	// Map two distinct handles to one principal scope — unified memory.
	const principalScope = ScopeID("11111111-2222-3333-4444-555555555555")
	require.NoError(t, store.PutIdentity("mattermost", "u1", principalScope))
	require.NoError(t, store.PutIdentity("telegram", "42", principalScope))

	for _, tc := range []struct{ transport, native string }{{"mattermost", "u1"}, {"telegram", "42"}} {
		idn, err := store.GetIdentity(tc.transport, tc.native)
		require.NoError(t, err)
		require.NotNil(t, idn)
		assert.Equal(t, principalScope, idn.ScopeID, "both handles resolve to the same scope")
		assert.Equal(t, tc.transport, idn.Transport)
		assert.Equal(t, tc.native, idn.NativeID)
	}

	// Re-pointing a handle to a different scope is allowed (re-link policy).
	const other = ScopeID("99999999-8888-7777-6666-555555555555")
	require.NoError(t, store.PutIdentity("mattermost", "u1", other))
	idn, err = store.GetIdentity("mattermost", "u1")
	require.NoError(t, err)
	require.NotNil(t, idn)
	assert.Equal(t, other, idn.ScopeID)
}

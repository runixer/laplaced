package storage

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrincipal_GetOrCreate_ConcurrentSameLogin proves get-or-create is race-safe:
// many concurrent callers for one ad_login converge on a single scope with no
// error and exactly one principals row (the lookup→INSERT window can't mint two
// scopes or surface a unique-violation to the caller).
func TestPrincipal_GetOrCreate_ConcurrentSameLogin(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	const n = 8
	var wg sync.WaitGroup
	results := make([]ScopeID, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sid, _, err := store.GetOrCreatePrincipal(PrincipalInput{ADLogin: "race.user"})
			results[i], errs[i] = sid, err
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		require.NoErrorf(t, errs[i], "goroutine %d errored", i)
		assert.Equalf(t, results[0], results[i], "goroutine %d got a different scope", i)
		assert.NotEmpty(t, results[i])
	}

	// Exactly one principal row exists for the login — no duplicate mint.
	var count int
	require.NoError(t, store.queryRow(
		"SELECT COUNT(*) FROM principals WHERE ad_login = ?", "race.user",
	).Scan(&count))
	assert.Equal(t, 1, count, "concurrent get-or-create must create exactly one principal")
}

func TestPrincipal_GetOrCreate_DedupByADLogin(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	sid1, created, err := store.GetOrCreatePrincipal(PrincipalInput{
		ADLogin: "k.gruzdev", Email: "k.gruzdev@corp", DisplayName: "K Gruzdev",
	})
	require.NoError(t, err)
	assert.True(t, created, "first call creates the principal")
	assert.NotEmpty(t, sid1)

	// A second handle for the same person (same ad_login) reuses the scope —
	// this is the unified cross-transport memory invariant.
	sid2, created, err := store.GetOrCreatePrincipal(PrincipalInput{ADLogin: "k.gruzdev"})
	require.NoError(t, err)
	assert.False(t, created, "same ad_login must not create a new principal")
	assert.Equal(t, sid1, sid2)

	// A principal scope is NOT a channel.
	isCh, err := store.IsChannelScope(sid1)
	require.NoError(t, err)
	assert.False(t, isCh)
}

func TestPrincipal_GetOrCreate_DedupByObjectGUIDAcrossLoginRename(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	sid1, created, err := store.GetOrCreatePrincipal(PrincipalInput{
		ObjectGUID: "guid-123", ADLogin: "k.gruzdev",
	})
	require.NoError(t, err)
	assert.True(t, created)

	// Same person, login renamed: object_guid still matches → same scope, and
	// the new ad_login is backfilled.
	sid2, created, err := store.GetOrCreatePrincipal(PrincipalInput{
		ObjectGUID: "guid-123", ADLogin: "k.gruzdev2",
	})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, sid1, sid2)

	p, err := store.GetPrincipal(sid1)
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.Equal(t, "k.gruzdev2", p.ADLogin)
}

func TestPrincipal_GetOrCreate_BackfillObjectGUID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	// Created without an object_guid (dedup by ad_login).
	sid1, _, err := store.GetOrCreatePrincipal(PrincipalInput{ADLogin: "alice"})
	require.NoError(t, err)

	// A later Keycloak/LDAP lookup supplies the object_guid. Dedup by ad_login
	// finds the existing scope and backfills the anchor — additive, no re-partition.
	sid2, created, err := store.GetOrCreatePrincipal(PrincipalInput{
		ObjectGUID: "guid-alice", ADLogin: "alice",
	})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, sid1, sid2)

	p, err := store.GetPrincipal(sid1)
	require.NoError(t, err)
	assert.Equal(t, "guid-alice", p.ObjectGUID)
}

func TestPrincipal_DistinctPeopleGetDistinctScopes(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	a, _, err := store.GetOrCreatePrincipal(PrincipalInput{ADLogin: "alice"})
	require.NoError(t, err)
	b, _, err := store.GetOrCreatePrincipal(PrincipalInput{ADLogin: "bob"})
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "different people must never share a scope")

	// Two principals with unknown (empty) keys must also stay distinct.
	x, _, err := store.GetOrCreatePrincipal(PrincipalInput{DisplayName: "Anon One"})
	require.NoError(t, err)
	y, _, err := store.GetOrCreatePrincipal(PrincipalInput{DisplayName: "Anon Two"})
	require.NoError(t, err)
	assert.NotEqual(t, x, y, "empty dedup keys must not collide")
}

func TestPrincipal_GetPrincipal_NotAPrincipal(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	p, err := store.GetPrincipal("nonexistent-scope")
	require.NoError(t, err)
	assert.Nil(t, p)
}

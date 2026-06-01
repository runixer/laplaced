package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScopeID_Scan covers every branch of the sql.Scanner — the riskiest spot,
// because SQLite's INTEGER affinity can hand back a numeric scope as int64 while
// UUID scopes arrive as string/[]byte. All must round-trip losslessly.
func TestScopeID_Scan(t *testing.T) {
	tests := []struct {
		name string
		src  any
		want ScopeID
	}{
		{"nil", nil, ""},
		{"string uuid", "11111111-2222-3333-4444-555555555555", "11111111-2222-3333-4444-555555555555"},
		{"bytes uuid", []byte("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"), "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		{"int64 (sqlite affinity)", int64(123), "123"},
		{"string numeric", "456", "456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s ScopeID
			require.NoError(t, s.Scan(tt.src))
			assert.Equal(t, tt.want, s)
		})
	}
}

func TestScopeID_Scan_UnsupportedType(t *testing.T) {
	var s ScopeID
	assert.Error(t, s.Scan(3.14), "float src must be rejected, not silently coerced")
}

func TestScopeID_Value(t *testing.T) {
	v, err := ScopeID("abc").Value()
	require.NoError(t, err)
	assert.Equal(t, "abc", v) // bound as a string, not the ScopeID type
}

func TestScopeID_IsZero(t *testing.T) {
	assert.True(t, ScopeID("").IsZero())
	assert.False(t, ScopeID("x").IsZero())
}

// TestPassthroughScopeID_Deterministic guards the invariant the whole home
// migration relies on: (transport, nativeID) → a stable UUID, distinct per input.
func TestPassthroughScopeID_Deterministic(t *testing.T) {
	a := PassthroughScopeID("telegram", "123")
	assert.Equal(t, a, PassthroughScopeID("telegram", "123"), "must be stable")
	assert.NotEqual(t, a, PassthroughScopeID("telegram", "124"), "distinct native id")
	assert.NotEqual(t, a, PassthroughScopeID("mattermost", "123"), "transport is part of the key")
	assert.NotEmpty(t, a)
}

// TestMintScopeID_Unique: minted principal ids are non-empty and unique.
func TestMintScopeID_Unique(t *testing.T) {
	a, b := MintScopeID(), MintScopeID()
	assert.NotEmpty(t, a)
	assert.NotEqual(t, a, b)
}

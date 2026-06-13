package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAddAndGetFlags(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := ScopeID("123")
	hid := int64(42)
	trace := "0102030405060708090a0b0c0d0e0f10"

	err := store.AddFlag(Flag{
		UserID:       userID,
		HistoryID:    &hid,
		MessageID:    "4473",
		TraceID:      &trace,
		Emoji:        "👎",
		ReplyPreview: "the bad reply",
	})
	assert.NoError(t, err)

	flags, err := store.GetFlags(userID, 10)
	assert.NoError(t, err)
	if assert.Len(t, flags, 1) {
		f := flags[0]
		assert.Equal(t, userID, f.UserID)
		assert.Equal(t, "4473", f.MessageID)
		assert.Equal(t, "👎", f.Emoji)
		assert.Equal(t, "the bad reply", f.ReplyPreview)
		if assert.NotNil(t, f.TraceID) {
			assert.Equal(t, trace, *f.TraceID)
		}
		if assert.NotNil(t, f.HistoryID) {
			assert.Equal(t, hid, *f.HistoryID)
		}
		assert.False(t, f.CreatedAt.IsZero(), "created_at should be set")
	}
}

func TestGetFlagsNullableTrace(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := ScopeID("123")
	// Flag with no trace/history (e.g. reply predates the feature).
	assert.NoError(t, store.AddFlag(Flag{UserID: userID, MessageID: "1", Emoji: "❤"}))

	flags, err := store.GetFlags(userID, 10)
	assert.NoError(t, err)
	if assert.Len(t, flags, 1) {
		assert.Nil(t, flags[0].TraceID)
		assert.Nil(t, flags[0].HistoryID)
	}
}

// TestFlagUserIsolation proves GetFlags never returns another user's flags.
func TestFlagUserIsolation(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	user1 := ScopeID("100")
	user2 := ScopeID("200")

	assert.NoError(t, store.AddFlag(Flag{UserID: user1, MessageID: "1", Emoji: "👎"}))
	assert.NoError(t, store.AddFlag(Flag{UserID: user2, MessageID: "2", Emoji: "👎"}))
	assert.NoError(t, store.AddFlag(Flag{UserID: user2, MessageID: "3", Emoji: "💩"}))

	t.Run("Get - only returns own flags", func(t *testing.T) {
		f1, err := store.GetFlags(user1, 10)
		assert.NoError(t, err)
		assert.Len(t, f1, 1)
		for _, f := range f1 {
			assert.Equal(t, user1, f.UserID)
		}

		f2, err := store.GetFlags(user2, 10)
		assert.NoError(t, err)
		assert.Len(t, f2, 2)
		for _, f := range f2 {
			assert.Equal(t, user2, f.UserID)
		}
	})

	t.Run("cross-user listing (empty scope) sees all", func(t *testing.T) {
		all, err := store.GetFlags("", 10)
		assert.NoError(t, err)
		assert.Len(t, all, 3)
	})
}

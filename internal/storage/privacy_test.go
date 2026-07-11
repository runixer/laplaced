package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrivacyMode_SetGet(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := ScopeID("123")

	enabled, err := store.GetPrivacyMode(userID)
	assert.NoError(t, err)
	assert.False(t, enabled, "missing users row means off")

	assert.NoError(t, store.SetPrivacyMode(userID, true))
	enabled, err = store.GetPrivacyMode(userID)
	assert.NoError(t, err)
	assert.True(t, enabled)

	assert.NoError(t, store.SetPrivacyMode(userID, false))
	enabled, err = store.GetPrivacyMode(userID)
	assert.NoError(t, err)
	assert.False(t, enabled)
}

func TestDoNotStore_ExcludedFromTopicMessages(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	ctx := context.Background()
	userID := ScopeID("123")

	require.NoError(t, store.AddMessageToHistory(userID, Message{Role: "user", Content: "public message"}))
	require.NoError(t, store.AddMessageToHistory(userID, Message{Role: "user", Content: "secret message", DoNotStore: true}))
	require.NoError(t, store.AddMessageToHistory(userID, Message{Role: "assistant", Content: "secret reply", DoNotStore: true}))

	// GetUnprocessedMessages returns all rows with the flag populated —
	// chunking needs the full session for boundaries and topic ranges.
	unprocessed, err := store.GetUnprocessedMessages(userID)
	require.NoError(t, err)
	require.Len(t, unprocessed, 3)
	assert.False(t, unprocessed[0].DoNotStore)
	assert.True(t, unprocessed[1].DoNotStore)
	assert.True(t, unprocessed[2].DoNotStore)

	// Assign all three to a topic (as the chunk pipeline's range update does).
	require.NoError(t, store.UpdateMessagesTopicInRange(ctx, userID, unprocessed[0].ID, unprocessed[2].ID, 42))

	// The topic read side must not resurface flagged content: it feeds the
	// archivist, RAG re-injection, and the merger.
	topicMsgs, err := store.GetMessagesByTopicID(ctx, 42)
	require.NoError(t, err)
	require.Len(t, topicMsgs, 1)
	assert.Equal(t, "public message", topicMsgs[0].Content)
}

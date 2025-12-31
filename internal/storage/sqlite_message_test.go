package storage

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHistory(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	store.Init()

	userID := int64(123)
	messages := []Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
	}

	// Add messages
	for _, msg := range messages {
		err := store.AddMessageToHistory(userID, msg)
		assert.NoError(t, err)
	}

	// Get history
	history, err := store.GetMessagesInRange(context.Background(), userID, 0, math.MaxInt64)
	assert.NoError(t, err)
	assert.Equal(t, len(messages), len(history))

	// Update expected IDs (DB assigns 1-based IDs)
	for i := range messages {
		messages[i].ID = int64(i + 1)
		messages[i].UserID = userID
		messages[i].CreatedAt = history[i].CreatedAt
	}
	assert.Equal(t, messages, history)

	// Clear history
	err = store.ClearHistory(userID)
	assert.NoError(t, err)

	// Get history again
	history, err = store.GetMessagesInRange(context.Background(), userID, 0, math.MaxInt64)
	assert.NoError(t, err)
	assert.Empty(t, history)
}

func TestImportMessage(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	store.Init()

	userID := int64(123)
	createdAt := time.Now().Add(-1 * time.Hour).UTC()
	msg := Message{
		Role:      "user",
		Content:   "Imported message",
		CreatedAt: createdAt,
	}

	err := store.ImportMessage(userID, msg)
	assert.NoError(t, err)

	history, err := store.GetMessagesInRange(context.Background(), userID, 0, math.MaxInt64)
	assert.NoError(t, err)
	assert.Len(t, history, 1)
	assert.Equal(t, "Imported message", history[0].Content)
	// SQLite stores time with less precision or different timezone handling, so we check InDelta or just that it's close
	assert.WithinDuration(t, createdAt, history[0].CreatedAt, time.Second)
}

func TestGetRecentHistory(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	store.Init()

	userID := int64(123)
	for i := 0; i < 10; i++ {
		store.AddMessageToHistory(userID, Message{Role: "user", Content: "msg"})
		time.Sleep(10 * time.Millisecond) // Ensure order
	}

	// Get recent 5
	recent, err := store.GetRecentHistory(userID, 5)
	assert.NoError(t, err)
	assert.Len(t, recent, 5)

	// Should be the last 5 messages (IDs 6-10)
	// GetRecentHistory returns them in chronological order (oldest to newest)
	assert.Equal(t, int64(6), recent[0].ID)
	assert.Equal(t, int64(10), recent[4].ID)
}

func TestGetMessagesByIDs(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	store.Init()

	userID := int64(123)
	var ids []int64
	for i := 0; i < 5; i++ {
		store.AddMessageToHistory(userID, Message{Role: "user", Content: "msg"})
		ids = append(ids, int64(i+1))
	}

	// Get subset
	targetIDs := []int64{1, 3, 5}
	msgs, err := store.GetMessagesByIDs(targetIDs)
	assert.NoError(t, err)
	assert.Len(t, msgs, 3)
	assert.Equal(t, int64(1), msgs[0].ID)
	assert.Equal(t, int64(3), msgs[1].ID)
	assert.Equal(t, int64(5), msgs[2].ID)
}

func TestUpdateMessageTopic(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	store.Init()

	userID := int64(123)
	store.AddMessageToHistory(userID, Message{Role: "user", Content: "msg"})

	// Initially topic_id is null
	msgs, _ := store.GetMessagesInRange(context.Background(), userID, 0, math.MaxInt64)
	assert.Nil(t, msgs[0].TopicID)

	// Update topic
	topicID := int64(99)
	err := store.UpdateMessageTopic(msgs[0].ID, topicID)
	assert.NoError(t, err)

	// Verify
	msgs, _ = store.GetMessagesInRange(context.Background(), userID, 0, math.MaxInt64)
	assert.NotNil(t, msgs[0].TopicID)
	assert.Equal(t, topicID, *msgs[0].TopicID)
}

func TestGetUnprocessedMessages(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	store.Init()

	userID := int64(123)
	store.AddMessageToHistory(userID, Message{Role: "user", Content: "processed", TopicID: new(int64)}) // Has topic
	store.AddMessageToHistory(userID, Message{Role: "user", Content: "unprocessed"})                    // No topic

	msgs, err := store.GetUnprocessedMessages(userID)
	assert.NoError(t, err)
	assert.Len(t, msgs, 1)
	assert.Equal(t, "unprocessed", msgs[0].Content)
}

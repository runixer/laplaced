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
	_ = store.Init()

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
	_ = store.Init()

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
	_ = store.Init()

	userID := int64(123)
	for i := 0; i < 10; i++ {
		_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: "msg"})
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
	_ = store.Init()

	userID := int64(123)
	for i := 0; i < 5; i++ {
		_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: "msg"})
	}

	// Get subset
	targetIDs := []int64{1, 3, 5}
	msgs, err := store.GetMessagesByIDs(userID, targetIDs)
	assert.NoError(t, err)
	assert.Len(t, msgs, 3)
	assert.Equal(t, int64(1), msgs[0].ID)
	assert.Equal(t, int64(3), msgs[1].ID)
	assert.Equal(t, int64(5), msgs[2].ID)
}

func TestUpdateMessageTopic(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: "msg"})

	// Initially topic_id is null
	msgs, _ := store.GetMessagesInRange(context.Background(), userID, 0, math.MaxInt64)
	assert.Nil(t, msgs[0].TopicID)

	// Update topic
	topicID := int64(99)
	err := store.UpdateMessageTopic(userID, msgs[0].ID, topicID)
	assert.NoError(t, err)

	// Verify
	msgs, _ = store.GetMessagesInRange(context.Background(), userID, 0, math.MaxInt64)
	assert.NotNil(t, msgs[0].TopicID)
	assert.Equal(t, topicID, *msgs[0].TopicID)
}

func TestGetUnprocessedMessages(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: "processed", TopicID: new(int64)}) // Has topic
	_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: "unprocessed"})                    // No topic

	msgs, err := store.GetUnprocessedMessages(userID)
	assert.NoError(t, err)
	assert.Len(t, msgs, 1)
	assert.Equal(t, "unprocessed", msgs[0].Content)
}

func TestGetMessagesByTopicID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	otherUserID := int64(456)
	topicID := int64(10)

	t.Run("get messages by topic ID", func(t *testing.T) {
		// Add messages with topic_id
		for i := 0; i < 3; i++ {
			_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: "msg", TopicID: &topicID})
		}

		// Add message without topic_id
		_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: "no topic"})

		// Add message for other user with same topic (cross-user topic access)
		_ = store.AddMessageToHistory(otherUserID, Message{Role: "user", Content: "other user msg", TopicID: &topicID})

		// GetMessagesByTopicID is cross-user (no user_id filter)
		msgs, err := store.GetMessagesByTopicID(context.Background(), topicID)
		assert.NoError(t, err)
		assert.Len(t, msgs, 4) // 3 for user1 + 1 for user2

		// All should have the same topic_id
		for _, msg := range msgs {
			assert.Equal(t, topicID, *msg.TopicID)
		}
	})

	t.Run("empty result for non-existent topic", func(t *testing.T) {
		msgs, err := store.GetMessagesByTopicID(context.Background(), 999)
		assert.NoError(t, err)
		assert.Empty(t, msgs)
	})
}

func TestUpdateMessagesTopicInRange(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	otherUserID := int64(456)
	topicID := int64(10)

	t.Run("update messages in range", func(t *testing.T) {
		// Add 5 messages for user1
		for i := 0; i < 5; i++ {
			_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: "msg"})
		}

		// Add 2 messages for user2 (interleaved IDs)
		for i := 0; i < 2; i++ {
			_ = store.AddMessageToHistory(otherUserID, Message{Role: "user", Content: "other msg"})
		}

		// Update user1's messages 2-4 to topic
		err := store.UpdateMessagesTopicInRange(context.Background(), userID, 2, 4, topicID)
		assert.NoError(t, err)

		// Verify user1's messages were updated
		msgs, _ := store.GetMessagesInRange(context.Background(), userID, 0, 100)
		for _, msg := range msgs {
			if msg.ID >= 2 && msg.ID <= 4 {
				assert.Equal(t, topicID, *msg.TopicID, "message %d should have topic set", msg.ID)
			} else {
				assert.Nil(t, msg.TopicID, "message %d should not have topic", msg.ID)
			}
		}

		// Verify user2's messages were NOT updated (user isolation)
		otherMsgs, _ := store.GetMessagesInRange(context.Background(), otherUserID, 0, 100)
		for _, msg := range otherMsgs {
			assert.Nil(t, msg.TopicID, "other user's messages should not be updated")
		}
	})

	t.Run("update with invalid range", func(t *testing.T) {
		// startID > endID - should just not update anything
		err := store.UpdateMessagesTopicInRange(context.Background(), userID, 100, 50, topicID)
		assert.NoError(t, err) // No error, just no rows affected
	})
}

func TestGetRecentSessionMessages(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)

	t.Run("get recent session messages", func(t *testing.T) {
		// Add 5 unprocessed messages
		for i := 0; i < 5; i++ {
			_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: "msg"})
		}

		// Add 2 processed messages (with topic)
		topicID := int64(10)
		_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: "processed", TopicID: &topicID})
		_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: "processed2", TopicID: &topicID})

		// Get recent 3 session messages
		msgs, err := store.GetRecentSessionMessages(context.Background(), userID, 3, nil)
		assert.NoError(t, err)
		assert.Len(t, msgs, 3)

		// All should have nil topic_id (unprocessed)
		for _, msg := range msgs {
			assert.Nil(t, msg.TopicID, "session messages should not have topic")
		}

		// Should be in chronological order (oldest to newest)
		assert.True(t, msgs[0].ID < msgs[1].ID)
		assert.True(t, msgs[1].ID < msgs[2].ID)
	})

	t.Run("exclude message IDs", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// Add 5 unprocessed messages
		for i := 0; i < 5; i++ {
			_ = store2.AddMessageToHistory(userID, Message{Role: "user", Content: "msg"})
		}

		// Get all 5 to get their IDs
		allMsgs, _ := store2.GetMessagesInRange(context.Background(), userID, 0, 100)
		var unprocessedIDs []int64
		for _, msg := range allMsgs {
			if msg.TopicID == nil {
				unprocessedIDs = append(unprocessedIDs, msg.ID)
			}
		}

		// Get recent 3, excluding the first 2 IDs
		excludeIDs := unprocessedIDs[:2]
		msgs, err := store2.GetRecentSessionMessages(context.Background(), userID, 10, excludeIDs)
		assert.NoError(t, err)
		assert.Len(t, msgs, 3) // Remaining 3 unprocessed messages

		// Verify excluded IDs are not in results
		for _, msg := range msgs {
			assert.NotContains(t, excludeIDs, msg.ID)
		}
	})

	t.Run("limit of zero returns nil", func(t *testing.T) {
		msgs, err := store.GetRecentSessionMessages(context.Background(), userID, 0, nil)
		assert.NoError(t, err)
		assert.Nil(t, msgs)
	})

	t.Run("negative limit returns nil", func(t *testing.T) {
		msgs, err := store.GetRecentSessionMessages(context.Background(), userID, -1, nil)
		assert.NoError(t, err)
		assert.Nil(t, msgs)
	})

	t.Run("respects user isolation", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		otherUserID := int64(456)

		// Add messages for both users
		_ = store2.AddMessageToHistory(userID, Message{Role: "user", Content: "user1 msg"})
		_ = store2.AddMessageToHistory(otherUserID, Message{Role: "user", Content: "user2 msg"})
		_ = store2.AddMessageToHistory(userID, Message{Role: "user", Content: "user1 msg2"})

		// Get for user1 only
		msgs, err := store2.GetRecentSessionMessages(context.Background(), userID, 10, nil)
		assert.NoError(t, err)
		assert.Len(t, msgs, 2)

		// All messages should belong to user1
		for _, msg := range msgs {
			assert.Equal(t, userID, msg.UserID)
		}
	})
}

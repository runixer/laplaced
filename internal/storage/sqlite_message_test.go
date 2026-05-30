package storage

import (
	"context"
	"fmt"
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

// TestMessageUserIsolation tests that users cannot access or modify other users' messages.
// This is CRITICAL because message IDs are GLOBAL auto-increment, not per-user.
func TestMessageUserIsolation(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	user1ID := int64(100)
	user2ID := int64(200)

	// Add messages for user1
	for i := 0; i < 3; i++ {
		_ = store.AddMessageToHistory(user1ID, Message{Role: "user", Content: fmt.Sprintf("User1 msg %d", i)})
	}

	// Add messages for user2 (IDs will be interleaved: 1,2,3 for user1, 4,5 for user2)
	for i := 0; i < 2; i++ {
		_ = store.AddMessageToHistory(user2ID, Message{Role: "user", Content: fmt.Sprintf("User2 msg %d", i)})
	}

	// Get the actual IDs
	user1Msgs, _ := store.GetMessagesInRange(context.Background(), user1ID, 0, 100)
	user2Msgs, _ := store.GetMessagesInRange(context.Background(), user2ID, 0, 100)

	t.Run("GetMessagesInRange - only returns own messages", func(t *testing.T) {
		// User1 should only see their own messages
		msgs, err := store.GetMessagesInRange(context.Background(), user1ID, 0, 100)
		assert.NoError(t, err)
		assert.Len(t, msgs, 3, "user1 should only see their 3 messages")
		for _, msg := range msgs {
			assert.Equal(t, user1ID, msg.UserID, "all messages should belong to user1")
		}

		// User2 should only see their own messages
		msgs, err = store.GetMessagesInRange(context.Background(), user2ID, 0, 100)
		assert.NoError(t, err)
		assert.Len(t, msgs, 2, "user2 should only see their 2 messages")
		for _, msg := range msgs {
			assert.Equal(t, user2ID, msg.UserID, "all messages should belong to user2")
		}
	})

	t.Run("GetMessagesByIDs - cannot access other users' messages by ID", func(t *testing.T) {
		// User1 tries to get user2's message by ID
		user2FirstID := user2Msgs[0].ID
		msgs, err := store.GetMessagesByIDs(user1ID, []int64{user2FirstID})
		assert.NoError(t, err)
		assert.Empty(t, msgs, "user1 should not be able to get user2's message by ID")

		// User1 can only get their own messages
		user1FirstID := user1Msgs[0].ID
		msgs, err = store.GetMessagesByIDs(user1ID, []int64{user1FirstID})
		assert.NoError(t, err)
		assert.Len(t, msgs, 1)
		assert.Equal(t, user1FirstID, msgs[0].ID)

		// User1 tries to get both IDs - should only get their own
		msgs, err = store.GetMessagesByIDs(user1ID, []int64{user1FirstID, user2FirstID})
		assert.NoError(t, err)
		assert.Len(t, msgs, 1, "should only return own message, not other user's")
		assert.Equal(t, user1ID, msgs[0].UserID)
	})

	t.Run("UpdateMessageTopic - cannot modify other users' messages", func(t *testing.T) {
		// Get user1's first message
		user1Msg, _ := store.GetMessagesInRange(context.Background(), user1ID, 0, 1)
		user1MsgID := user1Msg[0].ID

		// User2 tries to update user1's message topic
		topicID := int64(99)
		err := store.UpdateMessageTopic(user2ID, user1MsgID, topicID)
		assert.NoError(t, err) // No error, but WHERE clause filters by user_id

		// Verify user1's message topic was NOT updated
		msgs, _ := store.GetMessagesInRange(context.Background(), user1ID, 0, 1)
		assert.Nil(t, msgs[0].TopicID, "user1's message topic should not be updated by user2")

		// User1 can update their own message
		err = store.UpdateMessageTopic(user1ID, user1MsgID, topicID)
		assert.NoError(t, err)

		msgs, _ = store.GetMessagesInRange(context.Background(), user1ID, 0, 1)
		assert.NotNil(t, msgs[0].TopicID)
		assert.Equal(t, topicID, *msgs[0].TopicID, "user1's message topic should be updated by user1")
	})

	t.Run("UpdateMessagesTopicInRange - only affects specified user", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// Add 5 messages for user1
		for i := 0; i < 5; i++ {
			_ = store2.AddMessageToHistory(user1ID, Message{Role: "user", Content: "msg"})
		}

		// Add 5 messages for user2 (interleaved IDs)
		for i := 0; i < 5; i++ {
			_ = store2.AddMessageToHistory(user2ID, Message{Role: "user", Content: "msg"})
		}

		// Update user1's messages in range 3-7 (may include some of user2's IDs!)
		topicID := int64(10)
		err := store2.UpdateMessagesTopicInRange(context.Background(), user1ID, 3, 7, topicID)
		assert.NoError(t, err)

		// Verify user1's messages were updated
		user1Updated, _ := store2.GetMessagesInRange(context.Background(), user1ID, 3, 7)
		assert.Greater(t, len(user1Updated), 0, "user1 should have messages in range")
		for _, msg := range user1Updated {
			assert.NotNil(t, msg.TopicID, "user1's messages in range should have topic")
			assert.Equal(t, topicID, *msg.TopicID)
		}

		// Verify user2's messages were NOT updated (CRITICAL!)
		user2All, _ := store2.GetMessagesInRange(context.Background(), user2ID, 0, 100)
		for _, msg := range user2All {
			if msg.ID >= 3 && msg.ID <= 7 {
				assert.Nil(t, msg.TopicID, "user2's message with ID %d in range should NOT be updated", msg.ID)
			}
		}
	})

	t.Run("ClearHistory - only affects specified user", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// Add messages for both users
		_ = store2.AddMessageToHistory(user1ID, Message{Role: "user", Content: "user1 msg"})
		_ = store2.AddMessageToHistory(user2ID, Message{Role: "user", Content: "user2 msg"})
		_ = store2.AddMessageToHistory(user1ID, Message{Role: "assistant", Content: "user1 response"})
		_ = store2.AddMessageToHistory(user2ID, Message{Role: "assistant", Content: "user2 response"})

		// Clear user1's history only
		err := store2.ClearHistory(user1ID)
		assert.NoError(t, err)

		// Verify user1's messages are gone
		user1After, _ := store2.GetMessagesInRange(context.Background(), user1ID, 0, 100)
		assert.Empty(t, user1After, "user1's messages should be deleted")

		// Verify user2's messages are intact
		user2After, _ := store2.GetMessagesInRange(context.Background(), user2ID, 0, 100)
		assert.Len(t, user2After, 2, "user2's messages should still exist")
	})

	t.Run("GetUnprocessedMessages - only returns own messages", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		topicID := int64(10)

		// Add processed and unprocessed messages for both users
		_ = store2.AddMessageToHistory(user1ID, Message{Role: "user", Content: "u1 processed", TopicID: &topicID})
		_ = store2.AddMessageToHistory(user1ID, Message{Role: "user", Content: "u1 unprocessed"})
		_ = store2.AddMessageToHistory(user2ID, Message{Role: "user", Content: "u2 processed", TopicID: &topicID})
		_ = store2.AddMessageToHistory(user2ID, Message{Role: "user", Content: "u2 unprocessed"})

		// Get unprocessed for user1
		msgs, err := store2.GetUnprocessedMessages(user1ID)
		assert.NoError(t, err)
		assert.Len(t, msgs, 1, "user1 should have 1 unprocessed message")
		assert.Equal(t, "u1 unprocessed", msgs[0].Content)
		assert.Equal(t, user1ID, msgs[0].UserID)
	})

	t.Run("GetRecentHistory - only returns own messages", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// Add messages for both users
		_ = store2.AddMessageToHistory(user1ID, Message{Role: "user", Content: "u1 msg1"})
		_ = store2.AddMessageToHistory(user2ID, Message{Role: "user", Content: "u2 msg1"})
		_ = store2.AddMessageToHistory(user1ID, Message{Role: "user", Content: "u1 msg2"})

		// Get recent for user1
		msgs, err := store2.GetRecentHistory(user1ID, 10)
		assert.NoError(t, err)
		assert.Len(t, msgs, 2, "user1 should see 2 messages")
		// Verify content - should only be user1's messages
		for _, msg := range msgs {
			assert.Contains(t, msg.Content, "u1", "should only contain user1's messages")
			assert.NotContains(t, msg.Content, "u2", "should not contain user2's messages")
		}

		// Get recent for user2 - should only see their own message
		msgs2, err := store2.GetRecentHistory(user2ID, 10)
		assert.NoError(t, err)
		assert.Len(t, msgs2, 1, "user2 should see 1 message")
		assert.Equal(t, "u2 msg1", msgs2[0].Content)
	})
}

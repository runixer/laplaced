package storage

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetAllUsers(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	store.Init()

	// 1. Add a regular user
	user1 := User{ID: 1, Username: "user1", FirstName: "User", LastName: "One"}
	err := store.UpsertUser(user1)
	assert.NoError(t, err)

	// 2. Add a topic for a user NOT in users table
	topic := Topic{UserID: 2, Summary: "Topic 1", StartMsgID: 1, EndMsgID: 2}
	_, err = store.AddTopic(topic)
	assert.NoError(t, err)

	// 3. Add a fact for another user NOT in users table
	fact := Fact{UserID: 3, Content: "Fact 1", Type: "context", Importance: 50}
	_, err = store.AddFact(fact)
	assert.NoError(t, err)

	// 4. Add a message for another user NOT in users table
	msg := Message{Role: "user", Content: "Hello"}
	err = store.AddMessageToHistory(4, msg)
	assert.NoError(t, err)

	// 5. Get all users
	users, err := store.GetAllUsers()
	assert.NoError(t, err)

	// Verify we have 4 users
	assert.Equal(t, 4, len(users))

	// Verify user 1 is correct
	var found1, found2, found3, found4 bool
	for _, u := range users {
		switch u.ID {
		case 1:
			found1 = true
			assert.Equal(t, "user1", u.Username)
		case 2:
			found2 = true
			assert.Equal(t, "User 2", u.Username)
			assert.Equal(t, "Unknown", u.FirstName)
		case 3:
			found3 = true
			assert.Equal(t, "User 3", u.Username)
			assert.Equal(t, "Unknown", u.FirstName)
		case 4:
			found4 = true
			assert.Equal(t, "User 4", u.Username)
			assert.Equal(t, "Unknown", u.FirstName)
		}
	}
	assert.True(t, found1, "User 1 not found")
	assert.True(t, found2, "User 2 not found")
	assert.True(t, found3, "User 3 not found")
	assert.True(t, found4, "User 4 not found")
}

func TestResetUserData(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	store.Init()

	userID := int64(123)

	// Add data across tables
	store.UpsertUser(User{ID: userID, Username: "test"})
	store.AddTopic(Topic{UserID: userID, Summary: "Topic"})
	store.AddFact(Fact{UserID: userID, Content: "Fact"})
	store.AddMessageToHistory(userID, Message{Role: "user", Content: "msg", TopicID: new(int64)}) // TopicID not null
	store.AddRAGLog(RAGLog{UserID: userID, OriginalQuery: "q"})
	store.UpdateMemoryBank(userID, "memory")

	// Reset
	err := store.ResetUserData(userID)
	assert.NoError(t, err)

	// Verify
	// Topics cleared
	topics, _ := store.GetTopics(userID)
	assert.Empty(t, topics)

	// Facts cleared
	facts, _ := store.GetFacts(userID)
	assert.Empty(t, facts)

	// RAG logs cleared
	logs, _ := store.GetRAGLogs(userID, 10)
	assert.Empty(t, logs)

	// Memory bank cleared
	mb, _ := store.GetMemoryBank(userID)
	assert.Empty(t, mb)

	// History NOT cleared, but topic_id reset
	msgs, _ := store.GetMessagesInRange(context.Background(), userID, 0, math.MaxInt64)
	assert.NotEmpty(t, msgs)
	assert.Nil(t, msgs[0].TopicID)
}

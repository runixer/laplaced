package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGetTopicsExtended(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(1)

	// Create topics
	// Topic 1: Small, no facts, not consolidated
	t1 := Topic{UserID: userID, Summary: "Topic 1", StartMsgID: 1, EndMsgID: 2, CreatedAt: time.Now().Add(-3 * time.Hour)}
	id1, err := store.AddTopic(t1)
	assert.NoError(t, err)

	// Topic 2: Large, has facts, consolidated
	t2 := Topic{UserID: userID, Summary: "Topic 2", StartMsgID: 3, EndMsgID: 10, IsConsolidated: true, CreatedAt: time.Now().Add(-2 * time.Hour)}
	id2, err := store.AddTopic(t2)
	assert.NoError(t, err)

	// Topic 3: Medium, has facts, not consolidated
	t3 := Topic{UserID: userID, Summary: "Topic 3", StartMsgID: 11, EndMsgID: 15, CreatedAt: time.Now().Add(-1 * time.Hour)}
	id3, err := store.AddTopic(t3)
	assert.NoError(t, err)

	// Add messages (history)
	// Topic 1: 2 messages
	_ = store.AddMessageToHistory(userID, Message{TopicID: &id1, Content: "msg1"})
	_ = store.AddMessageToHistory(userID, Message{TopicID: &id1, Content: "msg2"})

	// Topic 2: 8 messages
	for i := 0; i < 8; i++ {
		_ = store.AddMessageToHistory(userID, Message{TopicID: &id2, Content: "msg"})
	}

	// Topic 3: 5 messages
	for i := 0; i < 5; i++ {
		_ = store.AddMessageToHistory(userID, Message{TopicID: &id3, Content: "msg"})
	}

	// Add facts
	// Topic 2: 3 facts
	_, _ = store.AddFact(Fact{UserID: userID, TopicID: &id2, Content: "f1", Relation: "r1"})
	_, _ = store.AddFact(Fact{UserID: userID, TopicID: &id2, Content: "f2", Relation: "r2"})
	_, _ = store.AddFact(Fact{UserID: userID, TopicID: &id2, Content: "f3", Relation: "r3"})

	// Topic 3: 1 fact
	_, _ = store.AddFact(Fact{UserID: userID, TopicID: &id3, Content: "f4", Relation: "r4"})

	// Test 1: Pagination
	filter := TopicFilter{UserID: userID}
	res, err := store.GetTopicsExtended(filter, 2, 0, "created_at", "ASC")
	assert.NoError(t, err)
	assert.Equal(t, 3, res.TotalCount)
	assert.Equal(t, 2, len(res.Data))
	assert.Equal(t, id1, res.Data[0].ID)
	assert.Equal(t, id2, res.Data[1].ID)

	// Test 2: Filter HasFacts = true
	hasFacts := true
	filter = TopicFilter{UserID: userID, HasFacts: &hasFacts}
	res, err = store.GetTopicsExtended(filter, 10, 0, "created_at", "ASC")
	assert.NoError(t, err)
	assert.Equal(t, 2, res.TotalCount) // Topic 2 and 3
	assert.Equal(t, 2, len(res.Data))
	for _, topic := range res.Data {
		assert.True(t, topic.FactsCount > 0)
	}

	// Test 3: Filter HasFacts = false
	hasFacts = false
	filter = TopicFilter{UserID: userID, HasFacts: &hasFacts}
	res, err = store.GetTopicsExtended(filter, 10, 0, "created_at", "ASC")
	assert.NoError(t, err)
	assert.Equal(t, 1, res.TotalCount) // Topic 1
	assert.Equal(t, id1, res.Data[0].ID)
	assert.Equal(t, 0, res.Data[0].FactsCount)

	// Test 4: Filter IsConsolidated = true
	isConsolidated := true
	filter = TopicFilter{UserID: userID, IsConsolidated: &isConsolidated}
	res, err = store.GetTopicsExtended(filter, 10, 0, "created_at", "ASC")
	assert.NoError(t, err)
	assert.Equal(t, 1, res.TotalCount) // Topic 2
	assert.Equal(t, id2, res.Data[0].ID)

	// Test 5: Sort by Size (MessageCount) DESC
	filter = TopicFilter{UserID: userID}
	res, err = store.GetTopicsExtended(filter, 10, 0, "size", "DESC")
	assert.NoError(t, err)
	assert.Equal(t, 3, len(res.Data))
	assert.Equal(t, id2, res.Data[0].ID) // 8 msgs
	assert.Equal(t, id3, res.Data[1].ID) // 5 msgs
	assert.Equal(t, id1, res.Data[2].ID) // 2 msgs
	assert.Equal(t, 8, res.Data[0].MessageCount)

	// Test 6: Sort by FactsCount DESC
	res, err = store.GetTopicsExtended(filter, 10, 0, "facts_count", "DESC")
	assert.NoError(t, err)
	assert.Equal(t, 3, len(res.Data))
	assert.Equal(t, id2, res.Data[0].ID) // 3 facts
	assert.Equal(t, id3, res.Data[1].ID) // 1 fact
	assert.Equal(t, id1, res.Data[2].ID) // 0 facts
	assert.Equal(t, 3, res.Data[0].FactsCount)
}

func TestTopicCRUD(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	topic := Topic{
		UserID:     userID,
		Summary:    "My Topic",
		StartMsgID: 1,
		EndMsgID:   10,
	}

	// 1. Create
	id, err := store.CreateTopic(topic)
	assert.NoError(t, err)
	assert.NotZero(t, id)

	// 2. Get All
	topics, err := store.GetAllTopics()
	assert.NoError(t, err)
	assert.Len(t, topics, 1)
	assert.Equal(t, "My Topic", topics[0].Summary)

	// 3. Get by User
	userTopics, err := store.GetTopics(userID)
	assert.NoError(t, err)
	assert.Len(t, userTopics, 1)
	assert.Equal(t, id, userTopics[0].ID)

	// 4. Update Flags
	err = store.SetTopicFactsExtracted(userID, id, true)
	assert.NoError(t, err)
	err = store.SetTopicConsolidationChecked(userID, id, true)
	assert.NoError(t, err)

	// Verify Flags
	userTopics, _ = store.GetTopics(userID)
	assert.True(t, userTopics[0].FactsExtracted)
	assert.True(t, userTopics[0].ConsolidationChecked)

	// 5. Delete
	err = store.DeleteTopic(userID, id)
	assert.NoError(t, err)

	// Verify Delete
	topics, _ = store.GetAllTopics()
	assert.Empty(t, topics)
}

func TestGetLastTopicEndMessageID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	_, _ = store.AddTopic(Topic{UserID: userID, EndMsgID: 10})
	_, _ = store.AddTopic(Topic{UserID: userID, EndMsgID: 20})
	_, _ = store.AddTopic(Topic{UserID: userID, EndMsgID: 5})

	maxID, err := store.GetLastTopicEndMessageID(userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(20), maxID)

	// No topics
	maxID, err = store.GetLastTopicEndMessageID(999)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), maxID)
}

func TestGetTopicsByIDs(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	id1, _ := store.AddTopic(Topic{UserID: userID, Summary: "Topic 1", Embedding: []float32{1.0, 0.0}})
	_, _ = store.AddTopic(Topic{UserID: userID, Summary: "Topic 2", Embedding: []float32{0.0, 1.0}})
	id3, _ := store.AddTopic(Topic{UserID: userID, Summary: "Topic 3", Embedding: []float32{0.5, 0.5}})

	// Get specific IDs
	topics, err := store.GetTopicsByIDs(userID, []int64{id1, id3})
	assert.NoError(t, err)
	assert.Len(t, topics, 2)

	// Verify returned topics
	summaries := make(map[string]bool)
	for _, t := range topics {
		summaries[t.Summary] = true
	}
	assert.True(t, summaries["Topic 1"])
	assert.True(t, summaries["Topic 3"])
	assert.False(t, summaries["Topic 2"])

	// Empty IDs returns nil
	topics, err = store.GetTopicsByIDs(userID, []int64{})
	assert.NoError(t, err)
	assert.Nil(t, topics)

	// Non-existent IDs returns empty
	topics, err = store.GetTopicsByIDs(userID, []int64{999, 888})
	assert.NoError(t, err)
	assert.Empty(t, topics)
}

func TestGetTopicsPendingFacts(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	// Pending
	_, _ = store.AddTopic(Topic{UserID: userID, Summary: "Pending", FactsExtracted: false})
	// Done
	_, _ = store.AddTopic(Topic{UserID: userID, Summary: "Done", FactsExtracted: true})

	pending, err := store.GetTopicsPendingFacts(userID)
	assert.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.Equal(t, "Pending", pending[0].Summary)
}

func TestGetMergeCandidates(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)

	// T1: 1-10
	t1 := Topic{UserID: userID, StartMsgID: 1, EndMsgID: 10, CreatedAt: time.Now().Add(-2 * time.Hour)}
	id1, _ := store.AddTopic(t1)

	// T2: 11-20 (Adjacent to T1)
	t2 := Topic{UserID: userID, StartMsgID: 11, EndMsgID: 20, CreatedAt: time.Now().Add(-1 * time.Hour)}
	id2, _ := store.AddTopic(t2)

	// T3: 100-110 (Far from T2)
	t3 := Topic{UserID: userID, StartMsgID: 100, EndMsgID: 110, CreatedAt: time.Now()}
	_, _ = store.AddTopic(t3)

	// T4: 21-30 (Adjacent to T2)
	t4 := Topic{UserID: userID, StartMsgID: 21, EndMsgID: 30, CreatedAt: time.Now()}
	id4, _ := store.AddTopic(t4)

	// 1. Get Candidates
	candidates, err := store.GetMergeCandidates(userID)
	assert.NoError(t, err)

	// Should find (T1, T2) and (T2, T4)
	// T2(11) - T1(10) = 1 < 100
	// T4(21) - T2(20) = 1 < 100
	// T3(100) - T2(20) = 80 < 100 (So T2-T3 is also a candidate!)

	foundT1T2 := false
	foundT2T4 := false
	for _, c := range candidates {
		if c.Topic1.ID == id1 && c.Topic2.ID == id2 {
			foundT1T2 = true
		}
		if c.Topic1.ID == id2 && c.Topic2.ID == id4 {
			foundT2T4 = true
		}
	}
	assert.True(t, foundT1T2, "Should find T1-T2 candidate")
	assert.True(t, foundT2T4, "Should find T2-T4 candidate")

	// 2. Mark T1 as checked
	_ = store.SetTopicConsolidationChecked(userID, id1, true)
	candidates, err = store.GetMergeCandidates(userID)
	assert.NoError(t, err)

	// Should NOT find T1-T2 anymore
	foundT1T2 = false
	for _, c := range candidates {
		if c.Topic1.ID == id1 && c.Topic2.ID == id2 {
			foundT1T2 = true
		}
	}
	assert.False(t, foundT1T2, "Should not find T1-T2 after check")
}

func TestAddTopicWithoutMessageUpdate(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	topic := Topic{
		UserID:     userID,
		Summary:    "Test Topic",
		StartMsgID: 1,
		EndMsgID:   10,
	}

	t.Run("create topic without updating messages", func(t *testing.T) {
		id, err := store.AddTopicWithoutMessageUpdate(topic)
		assert.NoError(t, err)
		assert.NotZero(t, id)

		// Verify topic was created
		topics, err := store.GetTopics(userID)
		assert.NoError(t, err)
		assert.Len(t, topics, 1)
		assert.Equal(t, "Test Topic", topics[0].Summary)
	})

	t.Run("compare with AddTopic behavior", func(t *testing.T) {
		// AddTopicWithoutMessageUpdate should NOT update messages
		_, err := store.AddTopicWithoutMessageUpdate(topic)
		assert.NoError(t, err)

		// Create some messages with topic_id = nil
		for i := int64(1); i <= 5; i++ {
			err := store.AddMessageToHistory(userID, Message{Content: "msg", TopicID: nil})
			assert.NoError(t, err)
		}

		// Check messages - they should NOT have topic_id set yet
		messages, err := store.GetRecentHistory(userID, 100)
		assert.NoError(t, err)
		assert.Len(t, messages, 5)
		for _, msg := range messages {
			assert.Nil(t, msg.TopicID, "AddTopicWithoutMessageUpdate should not update messages")
		}

		// Now use AddTopic - it SHOULD update messages
		topic2 := Topic{UserID: userID, Summary: "Test Topic 2", StartMsgID: 6, EndMsgID: 10}
		id2, err := store.AddTopic(topic2)
		assert.NoError(t, err)

		// Re-fetch messages to get updated topic_id
		messages, _ = store.GetRecentHistory(userID, 100)

		// Messages 6-10 should now have topic_id
		for _, msg := range messages {
			if msg.ID >= 6 && msg.ID <= 10 {
				assert.Equal(t, &id2, msg.TopicID, "AddTopic should update messages in range")
			} else {
				assert.Nil(t, msg.TopicID, "Messages outside range should not be updated")
			}
		}
	})
}

func TestDeleteAllTopics(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	otherUserID := int64(456)

	// Create topics for user 1
	for i := 0; i < 3; i++ {
		_, err := store.AddTopic(Topic{UserID: userID, Summary: fmt.Sprintf("Topic %d", i)})
		assert.NoError(t, err)
	}

	// Create topic for user 2
	_, err := store.AddTopic(Topic{UserID: otherUserID, Summary: "Other Topic"})
	assert.NoError(t, err)

	t.Run("delete all topics for user", func(t *testing.T) {
		err := store.DeleteAllTopics(userID)
		assert.NoError(t, err)

		// Verify user's topics are deleted
		topics, err := store.GetTopics(userID)
		assert.NoError(t, err)
		assert.Empty(t, topics)

		// Verify other user's topics are intact
		otherTopics, err := store.GetTopics(otherUserID)
		assert.NoError(t, err)
		assert.Len(t, otherTopics, 1)
	})

	t.Run("delete all topics when none exist", func(t *testing.T) {
		// Should not error even if no topics exist
		err := store.DeleteAllTopics(999)
		assert.NoError(t, err)
	})
}

func TestDeleteTopicCascade(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	otherUserID := int64(456)

	t.Run("cascade delete removes facts and history", func(t *testing.T) {
		// Create a topic
		topicID, err := store.AddTopic(Topic{UserID: userID, Summary: "Test Topic", StartMsgID: 1, EndMsgID: 5})
		assert.NoError(t, err)

		// Add facts linked to this topic
		fact1 := Fact{UserID: userID, TopicID: &topicID, Content: "Fact 1", Relation: "is", Type: "context", Importance: 50}
		fact2 := Fact{UserID: userID, TopicID: &topicID, Content: "Fact 2", Relation: "is", Type: "identity", Importance: 100}
		_, err = store.AddFact(fact1)
		assert.NoError(t, err)
		_, err = store.AddFact(fact2)
		assert.NoError(t, err)

		// Add messages (simulated - normally AddTopic would set topic_id)
		for i := int64(1); i <= 5; i++ {
			_ = store.AddMessageToHistory(userID, Message{Content: "msg", TopicID: &topicID})
		}

		// Verify facts and messages exist
		factsBefore, _ := store.GetFacts(userID)
		assert.GreaterOrEqual(t, len(factsBefore), 2)

		// Cascade delete
		err = store.DeleteTopicCascade(userID, topicID)
		assert.NoError(t, err)

		// Verify topic is deleted
		topics, _ := store.GetTopics(userID)
		assert.Empty(t, topics)

		// Verify facts linked to topic are deleted
		factsAfter, _ := store.GetFacts(userID)
		for _, f := range factsAfter {
			assert.NotEqual(t, &topicID, f.TopicID, "facts linked to topic should be deleted")
		}

		// Verify fact_history is also cleaned up
		factHistory, err := store.GetFactHistory(userID, 100)
		assert.NoError(t, err)
		for _, fh := range factHistory {
			assert.NotEqual(t, &topicID, fh.TopicID, "fact history should be cleaned up")
		}
	})

	t.Run("user isolation - different user", func(t *testing.T) {
		// Create topic for other user
		otherTopicID, err := store.AddTopic(Topic{UserID: otherUserID, Summary: "Other Topic"})
		assert.NoError(t, err)

		// Try to delete as user 123 - should not error (DeleteTopicCascade returns nil if rows=0)
		// But verify the topic still exists for other user
		err = store.DeleteTopicCascade(userID, otherTopicID)
		assert.NoError(t, err)

		otherTopics, _ := store.GetTopics(otherUserID)
		assert.Len(t, otherTopics, 1, "other user's topic should still exist")
		assert.Equal(t, otherTopicID, otherTopics[0].ID)
	})

	t.Run("non-existent topic", func(t *testing.T) {
		// DeleteTopicCascade returns nil even if topic doesn't exist (rows affected = 0)
		err := store.DeleteTopicCascade(userID, 99999)
		assert.NoError(t, err)

		// Verify topic count - just count, don't care about specific topics
		initialTopics, _ := store.GetTopics(userID)
		initialCount := len(initialTopics)

		_ = store.DeleteTopicCascade(userID, 99999)

		finalTopics, _ := store.GetTopics(userID)
		finalCount := len(finalTopics)
		assert.Equal(t, initialCount, finalCount, "non-existent delete should not affect topic count")
	})
}

func TestGetTopicsAfterID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	// Create topics for different users
	user1ID := int64(100)
	user2ID := int64(200)

	id1, _ := store.AddTopic(Topic{UserID: user1ID, Summary: "Topic 1"})
	_, _ = store.AddTopic(Topic{UserID: user1ID, Summary: "Topic 2"})
	id3, _ := store.AddTopic(Topic{UserID: user2ID, Summary: "Topic 3"})

	t.Run("get topics after ID 1", func(t *testing.T) {
		topics, err := store.GetTopicsAfterID(id1)
		assert.NoError(t, err)
		assert.Len(t, topics, 2, "should return topics with ID > id1")

		// Verify ordering by ID ASC
		assert.True(t, topics[0].ID < topics[1].ID)
	})

	t.Run("get topics after last ID", func(t *testing.T) {
		topics, err := store.GetTopicsAfterID(id3)
		assert.NoError(t, err)
		assert.Empty(t, topics)
	})

	t.Run("get topics after non-existent ID", func(t *testing.T) {
		topics, err := store.GetTopicsAfterID(9999)
		assert.NoError(t, err)
		assert.Empty(t, topics)
	})

	t.Run("includes topics from all users", func(t *testing.T) {
		// GetTopicsAfterID is cross-user
		topics, err := store.GetTopicsAfterID(0)
		assert.NoError(t, err)
		assert.Len(t, topics, 3, "should return all topics across users")
	})
}

// TestTopicUserIsolation tests that users cannot access or modify other users' topics.
func TestTopicUserIsolation(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	user1ID := int64(100)
	user2ID := int64(200)

	// Create topics for user1
	user1TopicIDs := make([]int64, 3)
	for i := 0; i < 3; i++ {
		id, err := store.AddTopic(Topic{UserID: user1ID, Summary: fmt.Sprintf("User1 Topic %d", i)})
		assert.NoError(t, err)
		user1TopicIDs[i] = id
	}

	// Create topics for user2
	user2TopicIDs := make([]int64, 2)
	for i := 0; i < 2; i++ {
		id, err := store.AddTopic(Topic{UserID: user2ID, Summary: fmt.Sprintf("User2 Topic %d", i)})
		assert.NoError(t, err)
		user2TopicIDs[i] = id
	}

	t.Run("GetTopics - only returns own topics", func(t *testing.T) {
		user1Topics, err := store.GetTopics(user1ID)
		assert.NoError(t, err)
		assert.Len(t, user1Topics, 3, "user1 should only see their 3 topics")
		for _, topic := range user1Topics {
			assert.Equal(t, user1ID, topic.UserID, "all topics should belong to user1")
		}

		user2Topics, err := store.GetTopics(user2ID)
		assert.NoError(t, err)
		assert.Len(t, user2Topics, 2, "user2 should only see their 2 topics")
		for _, topic := range user2Topics {
			assert.Equal(t, user2ID, topic.UserID, "all topics should belong to user2")
		}
	})

	t.Run("GetTopicsByIDs - cannot access other users' topics by ID", func(t *testing.T) {
		// User1 tries to get user2's topic by ID
		topics, err := store.GetTopicsByIDs(user1ID, user2TopicIDs)
		assert.NoError(t, err)
		assert.Empty(t, topics, "user1 should not be able to get user2's topics by ID")

		// User1 can only get their own topics
		topics, err = store.GetTopicsByIDs(user1ID, user1TopicIDs)
		assert.NoError(t, err)
		assert.Len(t, topics, 3)

		// User1 tries to get mixed IDs - should only get their own
		mixedIDs := append(user1TopicIDs[:1], user2TopicIDs[0])
		topics, err = store.GetTopicsByIDs(user1ID, mixedIDs)
		assert.NoError(t, err)
		assert.Len(t, topics, 1, "should only return own topic, not other user's")
		assert.Equal(t, user1ID, topics[0].UserID)
	})

	t.Run("SetTopicFactsExtracted - cannot modify other users' topics", func(t *testing.T) {
		// Get user1's first topic
		user1Topics, _ := store.GetTopics(user1ID)
		user1TopicID := user1Topics[0].ID

		// User2 tries to set facts extracted on user1's topic
		err := store.SetTopicFactsExtracted(user2ID, user1TopicID, true)
		assert.NoError(t, err) // No error, but WHERE clause filters by user_id

		// Verify user1's topic flag was NOT updated
		user1TopicsAfter, _ := store.GetTopics(user1ID)
		for _, topic := range user1TopicsAfter {
			if topic.ID == user1TopicID {
				assert.False(t, topic.FactsExtracted, "user1's topic flag should not be updated by user2")
			}
		}

		// User1 can update their own topic
		err = store.SetTopicFactsExtracted(user1ID, user1TopicID, true)
		assert.NoError(t, err)

		user1TopicsAfter, _ = store.GetTopics(user1ID)
		for _, topic := range user1TopicsAfter {
			if topic.ID == user1TopicID {
				assert.True(t, topic.FactsExtracted, "user1's topic flag should be updated by user1")
			}
		}
	})

	t.Run("SetTopicConsolidationChecked - cannot modify other users' topics", func(t *testing.T) {
		// Get user1's first topic
		user1Topics, _ := store.GetTopics(user1ID)
		user1TopicID := user1Topics[0].ID

		// User2 tries to set consolidation checked on user1's topic
		err := store.SetTopicConsolidationChecked(user2ID, user1TopicID, true)
		assert.NoError(t, err) // No error, but WHERE clause filters by user_id

		// Verify user1's topic flag was NOT updated
		user1TopicsAfter, _ := store.GetTopics(user1ID)
		for _, topic := range user1TopicsAfter {
			if topic.ID == user1TopicID {
				assert.False(t, topic.ConsolidationChecked, "user1's topic flag should not be updated by user2")
			}
		}

		// User1 can update their own topic
		err = store.SetTopicConsolidationChecked(user1ID, user1TopicID, true)
		assert.NoError(t, err)

		user1TopicsAfter, _ = store.GetTopics(user1ID)
		for _, topic := range user1TopicsAfter {
			if topic.ID == user1TopicID {
				assert.True(t, topic.ConsolidationChecked, "user1's topic flag should be updated by user1")
			}
		}
	})

	t.Run("DeleteTopic - cannot delete other users' topics", func(t *testing.T) {
		// Get initial counts
		user1TopicsBefore, _ := store.GetTopics(user1ID)
		user1CountBefore := len(user1TopicsBefore)

		user2TopicsBefore, _ := store.GetTopics(user2ID)
		user2CountBefore := len(user2TopicsBefore)

		// User2 tries to delete user1's first topic
		user1Topics, _ := store.GetTopics(user1ID)
		user1TopicID := user1Topics[0].ID

		err := store.DeleteTopic(user2ID, user1TopicID)
		assert.NoError(t, err) // No error, but WHERE clause filters by user_id

		// Verify user1's topic was NOT deleted
		user1TopicsAfter, _ := store.GetTopics(user1ID)
		assert.Len(t, user1TopicsAfter, user1CountBefore, "user1's topic count should not change")

		// User1 can delete their own topic
		err = store.DeleteTopic(user1ID, user1TopicID)
		assert.NoError(t, err)

		user1TopicsAfter, _ = store.GetTopics(user1ID)
		assert.Len(t, user1TopicsAfter, user1CountBefore-1, "user1's topic should be deleted by user1")

		// Verify user2's topics are intact
		user2TopicsAfter, _ := store.GetTopics(user2ID)
		assert.Len(t, user2TopicsAfter, user2CountBefore, "user2's topic count should not change")
	})

	t.Run("DeleteTopicCascade - only affects own topics", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// Create topics with same IDs but different users
		topic1, _ := store2.AddTopic(Topic{UserID: user1ID, Summary: "User1 Topic", StartMsgID: 1, EndMsgID: 5})
		_, _ = store2.AddTopic(Topic{UserID: user2ID, Summary: "User2 Topic", StartMsgID: 6, EndMsgID: 10})

		// Add facts to user1's topic
		fact := Fact{UserID: user1ID, TopicID: &topic1, Content: "Fact", Relation: "is", Type: "context", Importance: 50}
		_, _ = store2.AddFact(fact)

		// User2 tries to cascade delete user1's topic
		err := store2.DeleteTopicCascade(user2ID, topic1)
		assert.NoError(t, err)

		// Verify user1's topic still exists
		user1Topics, _ := store2.GetTopics(user1ID)
		assert.Len(t, user1Topics, 1, "user1's topic should not be deleted by user2")

		// Verify facts still exist
		facts, _ := store2.GetFacts(user1ID)
		assert.Greater(t, len(facts), 0, "facts should still exist")
	})

	t.Run("GetTopicsPendingFacts - only returns own topics", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// Create pending topics for both users
		_, _ = store2.AddTopic(Topic{UserID: user1ID, Summary: "User1 Pending", FactsExtracted: false})
		_, _ = store2.AddTopic(Topic{UserID: user2ID, Summary: "User2 Pending", FactsExtracted: false})
		_, _ = store2.AddTopic(Topic{UserID: user1ID, Summary: "User1 Done", FactsExtracted: true})

		// Get pending for user1
		pending, err := store2.GetTopicsPendingFacts(user1ID)
		assert.NoError(t, err)
		assert.Len(t, pending, 1, "user1 should have 1 pending topic")
		assert.Equal(t, user1ID, pending[0].UserID)
		assert.NotContains(t, pending[0].Summary, "User2", "should not contain user2's topic")
	})

	t.Run("GetMergeCandidates - only returns own topics", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// Create adjacent topics for user1
		_, _ = store2.AddTopic(Topic{UserID: user1ID, StartMsgID: 1, EndMsgID: 10})
		_, _ = store2.AddTopic(Topic{UserID: user1ID, StartMsgID: 11, EndMsgID: 20})

		// Create adjacent topics for user2
		_, _ = store2.AddTopic(Topic{UserID: user2ID, StartMsgID: 21, EndMsgID: 30})
		_, _ = store2.AddTopic(Topic{UserID: user2ID, StartMsgID: 31, EndMsgID: 40})

		// Get candidates for user1
		candidates, err := store2.GetMergeCandidates(user1ID)
		assert.NoError(t, err)
		// All candidates should only involve user1's topics
		for _, c := range candidates {
			assert.Equal(t, user1ID, c.Topic1.UserID, "candidate topic1 should belong to user1")
			assert.Equal(t, user1ID, c.Topic2.UserID, "candidate topic2 should belong to user1")
		}
	})

	t.Run("GetLastTopicEndMessageID - only returns own max", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// User1's topic ends at message 10
		_, _ = store2.AddTopic(Topic{UserID: user1ID, EndMsgID: 10})
		// User2's topic ends at message 100
		_, _ = store2.AddTopic(Topic{UserID: user2ID, EndMsgID: 100})

		// Get last end message ID for user1
		maxID, err := store2.GetLastTopicEndMessageID(user1ID)
		assert.NoError(t, err)
		assert.Equal(t, int64(10), maxID, "user1 should only see their own max end message ID")
	})
}

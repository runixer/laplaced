package storage

import (
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
	_, _ = store.AddFact(Fact{UserID: userID, TopicID: &id2, Content: "f1", Entity: "e1", Relation: "r1"})
	_, _ = store.AddFact(Fact{UserID: userID, TopicID: &id2, Content: "f2", Entity: "e2", Relation: "r2"})
	_, _ = store.AddFact(Fact{UserID: userID, TopicID: &id2, Content: "f3", Entity: "e3", Relation: "r3"})

	// Topic 3: 1 fact
	_, _ = store.AddFact(Fact{UserID: userID, TopicID: &id3, Content: "f4", Entity: "e4", Relation: "r4"})

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
	err = store.SetTopicFactsExtracted(id, true)
	assert.NoError(t, err)
	err = store.SetTopicConsolidationChecked(id, true)
	assert.NoError(t, err)

	// Verify Flags
	userTopics, _ = store.GetTopics(userID)
	assert.True(t, userTopics[0].FactsExtracted)
	assert.True(t, userTopics[0].ConsolidationChecked)

	// 5. Delete
	err = store.DeleteTopic(id)
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
	_ = store.SetTopicConsolidationChecked(id1, true)
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

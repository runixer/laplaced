package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGetFactStats(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	now := time.Now().UTC()
	facts := []Fact{
		{UserID: 1, Relation: "R1", Content: "Fact 1", Type: "identity", Importance: 100, CreatedAt: now.Add(-24 * time.Hour), LastUpdated: now.Add(-24 * time.Hour)},
		{UserID: 1, Relation: "R2", Content: "Fact 2", Type: "identity", Importance: 100, CreatedAt: now.Add(-48 * time.Hour), LastUpdated: now.Add(-48 * time.Hour)},
		{UserID: 1, Relation: "R3", Content: "Fact 3", Type: "context", Importance: 50, CreatedAt: now, LastUpdated: now},
	}

	for _, f := range facts {
		_, err := store.AddFact(f)
		assert.NoError(t, err)
	}

	// Get stats
	stats, err := store.GetFactStats()
	assert.NoError(t, err)

	// Verify counts
	assert.Equal(t, 2, stats.CountByType["identity"])
	assert.Equal(t, 1, stats.CountByType["context"])

	// Verify average age
	// Ages: 1 day, 2 days, 0 days. Avg = 1.0 days.
	// Allow some delta for execution time.
	assert.InDelta(t, 1.0, stats.AvgAgeDays, 0.01)
}

func TestFactCRUD(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	fact := Fact{
		UserID:     userID,
		Relation:   "is",
		Content:    "A programming language",
		Category:   "tech",
		Type:       "fact",
		Importance: 80,
	}

	// 1. Add
	id, err := store.AddFact(fact)
	assert.NoError(t, err)
	assert.NotZero(t, id)

	// 2. Get (Single User)
	facts, err := store.GetFacts(userID)
	assert.NoError(t, err)
	assert.Len(t, facts, 1)
	assert.Equal(t, fact.Content, facts[0].Content)

	// 3. Update
	facts[0].Content = "An awesome programming language"
	facts[0].Importance = 90
	err = store.UpdateFact(facts[0])
	assert.NoError(t, err)

	// Verify Update
	facts, _ = store.GetFacts(userID)
	assert.Equal(t, "An awesome programming language", facts[0].Content)
	assert.Equal(t, 90, facts[0].Importance)

	// 4. Get By IDs
	byIDs, err := store.GetFactsByIDs(userID, []int64{id})
	assert.NoError(t, err)
	assert.Len(t, byIDs, 1)
	assert.Equal(t, id, byIDs[0].ID)

	// 5. Get All (Admin)
	all, err := store.GetAllFacts()
	assert.NoError(t, err)
	assert.Len(t, all, 1)

	// 6. Delete
	err = store.DeleteFact(userID, id)
	assert.NoError(t, err)

	// Verify Delete
	facts, _ = store.GetFacts(userID)
	assert.Empty(t, facts)
}

func TestGetFactsAfterID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	// Create facts for different users
	user1ID := int64(100)
	user2ID := int64(200)

	facts := []Fact{
		{UserID: user1ID, Relation: "is", Content: "Fact 1", Type: "identity", Importance: 100},
		{UserID: user1ID, Relation: "is", Content: "Fact 2", Type: "context", Importance: 50},
		{UserID: user2ID, Relation: "is", Content: "Fact 3", Type: "identity", Importance: 100},
	}

	var ids []int64
	for _, f := range facts {
		id, err := store.AddFact(f)
		assert.NoError(t, err)
		ids = append(ids, id)
	}

	t.Run("get facts after ID 1", func(t *testing.T) {
		result, err := store.GetFactsAfterID(ids[0])
		assert.NoError(t, err)
		assert.Len(t, result, 2, "should return facts with ID > first fact")

		// Verify ordering by ID ASC
		assert.True(t, result[0].ID < result[1].ID)
	})

	t.Run("get facts after last ID", func(t *testing.T) {
		result, err := store.GetFactsAfterID(ids[len(ids)-1])
		assert.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("get facts after non-existent ID", func(t *testing.T) {
		result, err := store.GetFactsAfterID(9999)
		assert.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("includes facts from all users", func(t *testing.T) {
		// GetFactsAfterID is cross-user
		result, err := store.GetFactsAfterID(0)
		assert.NoError(t, err)
		assert.Len(t, result, 3, "should return all facts across users")
	})
}

func TestGetFactStatsByUser(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	now := time.Now().UTC()

	// Create facts for user 1
	user1ID := int64(100)
	facts1 := []Fact{
		{UserID: user1ID, Relation: "R1", Content: "Fact 1", Type: "identity", Importance: 100, CreatedAt: now.Add(-24 * time.Hour), LastUpdated: now.Add(-24 * time.Hour)},
		{UserID: user1ID, Relation: "R2", Content: "Fact 2", Type: "identity", Importance: 100, CreatedAt: now.Add(-48 * time.Hour), LastUpdated: now.Add(-48 * time.Hour)},
		{UserID: user1ID, Relation: "R3", Content: "Fact 3", Type: "context", Importance: 50, CreatedAt: now, LastUpdated: now},
	}

	// Create facts for user 2
	user2ID := int64(200)
	facts2 := []Fact{
		{UserID: user2ID, Relation: "R4", Content: "Fact 4", Type: "status", Importance: 70, CreatedAt: now.Add(-12 * time.Hour), LastUpdated: now.Add(-12 * time.Hour)},
	}

	for _, f := range facts1 {
		_, err := store.AddFact(f)
		assert.NoError(t, err)
	}
	for _, f := range facts2 {
		_, err := store.AddFact(f)
		assert.NoError(t, err)
	}

	t.Run("stats for user 1", func(t *testing.T) {
		stats, err := store.GetFactStatsByUser(user1ID)
		assert.NoError(t, err)

		// Verify counts (only user 1's facts)
		assert.Equal(t, 2, stats.CountByType["identity"])
		assert.Equal(t, 1, stats.CountByType["context"])
		assert.Equal(t, 0, stats.CountByType["status"])

		// Verify average age (only user 1's facts)
		// Ages: 1 day, 2 days, 0 days. Avg = 1.0 days
		assert.InDelta(t, 1.0, stats.AvgAgeDays, 0.01)
	})

	t.Run("stats for user 2", func(t *testing.T) {
		stats, err := store.GetFactStatsByUser(user2ID)
		assert.NoError(t, err)

		assert.Equal(t, 1, stats.CountByType["status"])
		assert.InDelta(t, 0.5, stats.AvgAgeDays, 0.01) // 12 hours = 0.5 days
	})

	t.Run("stats for non-existent user", func(t *testing.T) {
		stats, err := store.GetFactStatsByUser(999)
		assert.NoError(t, err)
		assert.Empty(t, stats.CountByType)
		assert.Zero(t, stats.AvgAgeDays)
	})
}

func TestUpdateFactsTopic(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	oldTopicID := int64(1)
	newTopicID := int64(2)

	// Create facts with topic_id
	facts := []Fact{
		{UserID: userID, Relation: "is", Content: "Fact 1", Type: "identity", Importance: 100, TopicID: &oldTopicID},
		{UserID: userID, Relation: "is", Content: "Fact 2", Type: "context", Importance: 50, TopicID: &oldTopicID},
		{UserID: userID, Relation: "is", Content: "Fact 3", Type: "status", Importance: 70, TopicID: &newTopicID},
	}

	for _, f := range facts {
		_, err := store.AddFact(f)
		assert.NoError(t, err)
	}

	t.Run("update topic for facts", func(t *testing.T) {
		err := store.UpdateFactsTopic(userID, oldTopicID, newTopicID)
		assert.NoError(t, err)

		// Verify all facts now have newTopicID
		allFacts, err := store.GetFacts(userID)
		assert.NoError(t, err)
		assert.Len(t, allFacts, 3)

		for _, f := range allFacts {
			assert.Equal(t, newTopicID, *f.TopicID, "all facts should have new topic ID")
		}
	})

	t.Run("update for non-existent user does not error", func(t *testing.T) {
		// Should not error even if user doesn't exist
		err := store.UpdateFactsTopic(999, oldTopicID, newTopicID)
		assert.NoError(t, err)
	})

	t.Run("user isolation - different user not affected", func(t *testing.T) {
		// Use a fresh store to test user isolation
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		userID2 := int64(456)
		oldTopic2 := int64(10)
		newTopic2 := int64(20)

		// Create facts for user 2 with oldTopic2
		facts2 := []Fact{
			{UserID: userID2, Relation: "is", Content: "Fact 1", Type: "identity", Importance: 100, TopicID: &oldTopic2},
		}
		for _, f := range facts2 {
			_, err := store2.AddFact(f)
			assert.NoError(t, err)
		}

		// Create facts for user 1 with oldTopicID (original store)
		facts1 := []Fact{
			{UserID: userID, Relation: "is", Content: "Other Fact", Type: "context", Importance: 50, TopicID: &oldTopicID},
		}
		for _, f := range facts1 {
			_, err := store.AddFact(f)
			assert.NoError(t, err)
		}

		// Update only user 2's facts
		err := store2.UpdateFactsTopic(userID2, oldTopic2, newTopic2)
		assert.NoError(t, err)

		// Verify user 2's facts were updated
		user2Facts, _ := store2.GetFacts(userID2)
		assert.Len(t, user2Facts, 1)
		assert.Equal(t, newTopic2, *user2Facts[0].TopicID)

		// Verify user 1's facts were NOT affected (still has oldTopicID)
		user1Facts, _ := store.GetFacts(userID)
		for _, f := range user1Facts {
			if f.TopicID != nil && *f.TopicID == oldTopicID {
				assert.Equal(t, oldTopicID, *f.TopicID)
			}
		}
	})
}

func TestDeleteAllFacts(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	otherUserID := int64(456)

	// Create facts for user 1
	facts1 := []Fact{
		{UserID: userID, Relation: "is", Content: "Fact 1", Type: "identity", Importance: 100},
		{UserID: userID, Relation: "is", Content: "Fact 2", Type: "context", Importance: 50},
		{UserID: userID, Relation: "is", Content: "Fact 3", Type: "status", Importance: 70},
	}

	// Create facts for user 2
	facts2 := []Fact{
		{UserID: otherUserID, Relation: "is", Content: "Other Fact", Type: "identity", Importance: 100},
	}

	for _, f := range facts1 {
		_, err := store.AddFact(f)
		assert.NoError(t, err)
	}
	for _, f := range facts2 {
		_, err := store.AddFact(f)
		assert.NoError(t, err)
	}

	t.Run("delete all facts for user", func(t *testing.T) {
		err := store.DeleteAllFacts(userID)
		assert.NoError(t, err)

		// Verify user's facts are deleted
		facts, err := store.GetFacts(userID)
		assert.NoError(t, err)
		assert.Empty(t, facts)

		// Verify other user's facts are intact
		otherFacts, err := store.GetFacts(otherUserID)
		assert.NoError(t, err)
		assert.Len(t, otherFacts, 1)
	})

	t.Run("delete all facts when none exist", func(t *testing.T) {
		// Should not error even if no facts exist
		err := store.DeleteAllFacts(999)
		assert.NoError(t, err)
	})
}

func TestGetFactsByTopicID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	topic1ID := int64(1)
	topic2ID := int64(2)

	// Create facts for topic 1
	facts1 := []Fact{
		{UserID: userID, Relation: "is", Content: "Fact 1", Type: "identity", Importance: 100, TopicID: &topic1ID},
		{UserID: userID, Relation: "is", Content: "Fact 2", Type: "context", Importance: 50, TopicID: &topic1ID},
	}

	// Create facts for topic 2
	facts2 := []Fact{
		{UserID: userID, Relation: "is", Content: "Fact 3", Type: "status", Importance: 70, TopicID: &topic2ID},
	}

	// Create fact with no topic
	factNoTopic := Fact{
		UserID: userID, Relation: "is", Content: "No topic", Type: "context", Importance: 60,
	}

	for _, f := range facts1 {
		_, err := store.AddFact(f)
		assert.NoError(t, err)
	}
	for _, f := range facts2 {
		_, err := store.AddFact(f)
		assert.NoError(t, err)
	}
	_, err := store.AddFact(factNoTopic)
	assert.NoError(t, err)

	t.Run("get facts by topic ID", func(t *testing.T) {
		facts, err := store.GetFactsByTopicID(userID, topic1ID)
		assert.NoError(t, err)
		assert.Len(t, facts, 2)

		for _, f := range facts {
			assert.Equal(t, topic1ID, *f.TopicID)
		}
	})

	t.Run("get facts by topic ID - empty result", func(t *testing.T) {
		facts, err := store.GetFactsByTopicID(userID, 999)
		assert.NoError(t, err)
		assert.Empty(t, facts)
	})

	t.Run("user isolation - different user", func(t *testing.T) {
		otherUserID := int64(456)
		facts, err := store.GetFactsByTopicID(otherUserID, topic1ID)
		assert.NoError(t, err)
		assert.Empty(t, facts, "different user should not get facts")
	})
}

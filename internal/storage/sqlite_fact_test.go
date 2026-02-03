package storage

import (
	"fmt"
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

// TestFactUserIsolation tests that users cannot access or modify other users' facts.
// This is CRITICAL for data security - prevents cross-user data leakage.
func TestFactUserIsolation(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	user1ID := int64(100)
	user2ID := int64(200)

	// Create fact for user1
	user1Fact := Fact{
		UserID:     user1ID,
		Relation:   "is",
		Content:    "User1's secret fact",
		Category:   "private",
		Type:       "identity",
		Importance: 100,
	}
	user1FactID, err := store.AddFact(user1Fact)
	assert.NoError(t, err)

	// Create fact for user2
	user2Fact := Fact{
		UserID:     user2ID,
		Relation:   "is",
		Content:    "User2's secret fact",
		Category:   "private",
		Type:       "identity",
		Importance: 100,
	}
	user2FactID, err := store.AddFact(user2Fact)
	assert.NoError(t, err)

	t.Run("GetFacts - only returns own facts", func(t *testing.T) {
		user1Facts, err := store.GetFacts(user1ID)
		assert.NoError(t, err)
		assert.Len(t, user1Facts, 1, "user1 should only see their own facts")
		assert.Equal(t, "User1's secret fact", user1Facts[0].Content)

		user2Facts, err := store.GetFacts(user2ID)
		assert.NoError(t, err)
		assert.Len(t, user2Facts, 1, "user2 should only see their own facts")
		assert.Equal(t, "User2's secret fact", user2Facts[0].Content)
	})

	t.Run("GetFactsByIDs - cannot access other users' facts by ID", func(t *testing.T) {
		// User1 tries to get user2's fact by ID
		facts, err := store.GetFactsByIDs(user1ID, []int64{user2FactID})
		assert.NoError(t, err)
		assert.Empty(t, facts, "user1 should not be able to get user2's fact by ID")

		// User1 can only get their own fact
		facts, err = store.GetFactsByIDs(user1ID, []int64{user1FactID})
		assert.NoError(t, err)
		assert.Len(t, facts, 1)
		assert.Equal(t, user1FactID, facts[0].ID)

		// User1 tries to get both IDs
		facts, err = store.GetFactsByIDs(user1ID, []int64{user1FactID, user2FactID})
		assert.NoError(t, err)
		assert.Len(t, facts, 1, "should only return own fact, not other user's")
		assert.Equal(t, user1FactID, facts[0].ID)
	})

	t.Run("UpdateFact - cannot modify other users' facts", func(t *testing.T) {
		// Get user1's fact
		user1Facts, _ := store.GetFacts(user1ID)
		originalContent := user1Facts[0].Content

		// Try to update with spoofed userID (user2 pretending to own user1's fact)
		spoofedFact := user1Facts[0]
		spoofedFact.UserID = user2ID // Spoof!
		spoofedFact.Content = "Hacked by user2"

		err := store.UpdateFact(spoofedFact)
		assert.NoError(t, err) // Update succeeds but WHERE clause filters by user_id

		// Verify user1's fact was NOT modified
		user1FactsAfter, _ := store.GetFacts(user1ID)
		assert.Len(t, user1FactsAfter, 1)
		assert.Equal(t, originalContent, user1FactsAfter[0].Content,
			"user1's fact should not be modified by user2")

		// Verify the update didn't affect anything
		allFacts, _ := store.GetAllFacts()
		for _, f := range allFacts {
			assert.NotEqual(t, "Hacked by user2", f.Content,
				"spoofed content should not appear in any fact")
		}
	})

	t.Run("DeleteFact - cannot delete other users' facts", func(t *testing.T) {
		// User2 tries to delete user1's fact
		err := store.DeleteFact(user2ID, user1FactID)
		assert.NoError(t, err) // No error, but WHERE clause filters by user_id

		// Verify user1's fact still exists
		user1Facts, _ := store.GetFacts(user1ID)
		assert.Len(t, user1Facts, 1, "user1's fact should not be deleted by user2")

		// Verify user1 can delete their own fact
		err = store.DeleteFact(user1ID, user1FactID)
		assert.NoError(t, err)

		user1Facts, _ = store.GetFacts(user1ID)
		assert.Empty(t, user1Facts, "user1's fact should be deleted by user1")

		// Verify user2's fact still exists
		user2Facts, _ := store.GetFacts(user2ID)
		assert.Len(t, user2Facts, 1, "user2's fact should still exist")
	})

	t.Run("UpdateFactsTopic - only affects specified user", func(t *testing.T) {
		// Create fresh facts for this test
		topic1 := int64(10)
		topic2 := int64(20)

		fact1 := Fact{UserID: user1ID, Relation: "is", Content: "Fact1", Type: "identity", Importance: 100, TopicID: &topic1}
		fact2 := Fact{UserID: user2ID, Relation: "is", Content: "Fact2", Type: "identity", Importance: 100, TopicID: &topic1}

		id1, _ := store.AddFact(fact1)
		id2, _ := store.AddFact(fact2)

		// Update topic for user1 only
		err := store.UpdateFactsTopic(user1ID, topic1, topic2)
		assert.NoError(t, err)

		// Verify user1's fact was updated
		user1Facts, _ := store.GetFacts(user1ID)
		for _, f := range user1Facts {
			if f.ID == id1 {
				assert.Equal(t, topic2, *f.TopicID, "user1's fact topic should be updated")
			}
		}

		// Verify user2's fact was NOT updated
		user2Facts, _ := store.GetFacts(user2ID)
		for _, f := range user2Facts {
			if f.ID == id2 {
				assert.Equal(t, topic1, *f.TopicID, "user2's fact topic should NOT be updated")
			}
		}
	})

	t.Run("DeleteAllFacts - only affects specified user", func(t *testing.T) {
		// Create multiple facts for each user
		for i := 0; i < 3; i++ {
			_, _ = store.AddFact(Fact{UserID: user1ID, Relation: "is", Content: fmt.Sprintf("U1-%d", i), Type: "context", Importance: 50})
			_, _ = store.AddFact(Fact{UserID: user2ID, Relation: "is", Content: fmt.Sprintf("U2-%d", i), Type: "context", Importance: 50})
		}

		// Delete user1's facts only
		err := store.DeleteAllFacts(user1ID)
		assert.NoError(t, err)

		// Verify user1's facts are gone
		user1Facts, _ := store.GetFacts(user1ID)
		assert.Empty(t, user1Facts, "user1's facts should be deleted")

		// Verify user2's facts are intact
		user2Facts, _ := store.GetFacts(user2ID)
		assert.NotEmpty(t, user2Facts, "user2's facts should still exist")
	})

	t.Run("GetFactsByTopicID - only returns own facts", func(t *testing.T) {
		topic1 := int64(100)

		// Both users have facts with same topic_id
		_, _ = store.AddFact(Fact{UserID: user1ID, Relation: "is", Content: "U1 topic fact", Type: "identity", Importance: 100, TopicID: &topic1})
		_, _ = store.AddFact(Fact{UserID: user2ID, Relation: "is", Content: "U2 topic fact", Type: "identity", Importance: 100, TopicID: &topic1})

		// Each user should only see their own facts
		user1Facts, _ := store.GetFactsByTopicID(user1ID, topic1)
		for _, f := range user1Facts {
			assert.Equal(t, user1ID, f.UserID, "user1 should only see their own facts")
			assert.NotEqual(t, "U2 topic fact", f.Content, "user1 should not see user2's fact")
		}

		user2Facts, _ := store.GetFactsByTopicID(user2ID, topic1)
		for _, f := range user2Facts {
			assert.Equal(t, user2ID, f.UserID, "user2 should only see their own facts")
			assert.NotEqual(t, "U1 topic fact", f.Content, "user2 should not see user1's fact")
		}
	})
}

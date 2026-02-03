package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFactHistory(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	now := time.Now().UTC()

	h1 := FactHistory{
		FactID:     1,
		UserID:     userID,
		Action:     "add",
		NewContent: "Fact 1",
		Category:   "bio",
		CreatedAt:  now.Add(-2 * time.Hour),
	}
	h2 := FactHistory{
		FactID:     1,
		UserID:     userID,
		Action:     "update",
		OldContent: "Fact 1",
		NewContent: "Fact 1 Updated",
		Category:   "bio",
		CreatedAt:  now.Add(-1 * time.Hour),
	}

	// 1. Add
	err := store.AddFactHistory(h1)
	assert.NoError(t, err)
	err = store.AddFactHistory(h2)
	assert.NoError(t, err)

	// 2. Get (Simple)
	history, err := store.GetFactHistory(userID, 10)
	assert.NoError(t, err)
	assert.Len(t, history, 2)
	assert.Equal(t, "update", history[0].Action) // DESC order
	assert.Equal(t, "add", history[1].Action)

	// 3. Get Extended (Filter by Action)
	filter := FactHistoryFilter{UserID: userID, Action: "add"}
	res, err := store.GetFactHistoryExtended(filter, 10, 0, "created_at", "DESC")
	assert.NoError(t, err)
	assert.Equal(t, 1, res.TotalCount)
	assert.Equal(t, "add", res.Data[0].Action)

	// 4. Get Extended (Search)
	filter = FactHistoryFilter{UserID: userID, Search: "Updated"}
	res, err = store.GetFactHistoryExtended(filter, 10, 0, "created_at", "DESC")
	assert.NoError(t, err)
	assert.Equal(t, 1, res.TotalCount)
	assert.Equal(t, "Fact 1 Updated", res.Data[0].NewContent)
}

func TestUpdateFactHistoryTopic(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	oldTopicID := int64(10)
	newTopicID := int64(20)

	t.Run("update topic for fact history entries", func(t *testing.T) {
		// Add fact history with old topic
		h1 := FactHistory{
			FactID:   1,
			UserID:   userID,
			Action:   "add",
			TopicID:  &oldTopicID,
			Category: "bio",
		}
		h2 := FactHistory{
			FactID:   2,
			UserID:   userID,
			Action:   "update",
			TopicID:  &oldTopicID,
			Category: "context",
		}

		_ = store.AddFactHistory(h1)
		_ = store.AddFactHistory(h2)

		// Add fact history with different topic
		h3 := FactHistory{
			FactID:   3,
			UserID:   userID,
			Action:   "delete",
			TopicID:  &newTopicID,
			Category: "identity",
		}
		_ = store.AddFactHistory(h3)

		// Update topic from old to new
		err := store.UpdateFactHistoryTopic(oldTopicID, newTopicID)
		assert.NoError(t, err)

		// Verify all old topic entries now have new topic
		history, _ := store.GetFactHistory(userID, 100)
		for _, h := range history {
			assert.Equal(t, newTopicID, *h.TopicID, "all entries should have new topic ID")
		}
	})

	t.Run("update with non-existent topic", func(t *testing.T) {
		// Should not error even if no entries with that topic exist
		err := store.UpdateFactHistoryTopic(999, 888)
		assert.NoError(t, err)
	})

	t.Run("update is cross-user", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		userID2 := int64(456)
		oldTopic2 := int64(100)
		newTopic2 := int64(200)

		// Add fact history for both users with same topic
		h1 := FactHistory{
			FactID:  1,
			UserID:  userID,
			Action:  "add",
			TopicID: &oldTopic2,
		}
		h2 := FactHistory{
			FactID:  2,
			UserID:  userID2,
			Action:  "add",
			TopicID: &oldTopic2,
		}
		_ = store2.AddFactHistory(h1)
		_ = store2.AddFactHistory(h2)

		// UpdateFactHistoryTopic affects all users (no user_id filter)
		err := store2.UpdateFactHistoryTopic(oldTopic2, newTopic2)
		assert.NoError(t, err)

		// Verify both users' fact history were updated
		history1, _ := store2.GetFactHistory(userID, 100)
		history2, _ := store2.GetFactHistory(userID2, 100)

		assert.Len(t, history1, 1)
		assert.Len(t, history2, 1)
		assert.Equal(t, newTopic2, *history1[0].TopicID)
		assert.Equal(t, newTopic2, *history2[0].TopicID)
	})
}

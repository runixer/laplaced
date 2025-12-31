package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGetFactStats(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	store.Init()

	now := time.Now().UTC()
	facts := []Fact{
		{UserID: 1, Entity: "E1", Relation: "R1", Content: "Fact 1", Type: "identity", Importance: 100, CreatedAt: now.Add(-24 * time.Hour), LastUpdated: now.Add(-24 * time.Hour)},
		{UserID: 1, Entity: "E2", Relation: "R2", Content: "Fact 2", Type: "identity", Importance: 100, CreatedAt: now.Add(-48 * time.Hour), LastUpdated: now.Add(-48 * time.Hour)},
		{UserID: 1, Entity: "E3", Relation: "R3", Content: "Fact 3", Type: "context", Importance: 50, CreatedAt: now, LastUpdated: now},
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
	store.Init()

	userID := int64(123)
	fact := Fact{
		UserID:     userID,
		Entity:     "Go",
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
	byIDs, err := store.GetFactsByIDs([]int64{id})
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

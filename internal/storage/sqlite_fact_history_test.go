package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFactHistory(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	store.Init()

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

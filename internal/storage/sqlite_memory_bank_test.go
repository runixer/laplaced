package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMemoryBank(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)

	// 1. Get Empty
	content, err := store.GetMemoryBank(userID)
	assert.NoError(t, err)
	assert.Empty(t, content)

	// 2. Update
	err = store.UpdateMemoryBank(userID, "Initial memory")
	assert.NoError(t, err)

	// 3. Get
	content, err = store.GetMemoryBank(userID)
	assert.NoError(t, err)
	assert.Equal(t, "Initial memory", content)

	// 4. Update Again
	err = store.UpdateMemoryBank(userID, "Updated memory")
	assert.NoError(t, err)

	// 5. Get Again
	content, err = store.GetMemoryBank(userID)
	assert.NoError(t, err)
	assert.Equal(t, "Updated memory", content)
}

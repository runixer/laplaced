package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRecoverArtifactStates_TimestampCheck(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(1)

	// Artifact 1: OLD - in 'processing' for 15 minutes (should be recovered)
	oldArtifact := Artifact{
		UserID:       userID,
		MessageID:    100,
		FileType:     "image",
		FilePath:     "/test/old.jpg",
		FileSize:     1024,
		MimeType:     "image/jpeg",
		OriginalName: "old.jpg",
		ContentHash:  "oldhash123",
		State:        "processing",
	}
	id1, err := store.AddArtifact(oldArtifact)
	assert.NoError(t, err)
	// Set created_at to 15 minutes ago via SQL
	setArtifactCreatedAt(t, store, id1, -15*time.Minute)

	// Artifact 2: RECENT - in 'processing' for 5 minutes (should NOT be recovered)
	recentArtifact := Artifact{
		UserID:       userID,
		MessageID:    101,
		FileType:     "image",
		FilePath:     "/test/recent.jpg",
		FileSize:     2048,
		MimeType:     "image/jpeg",
		OriginalName: "recent.jpg",
		ContentHash:  "recentHash456",
		State:        "processing",
	}
	id2, err := store.AddArtifact(recentArtifact)
	assert.NoError(t, err)
	// Set created_at to 5 minutes ago via SQL
	setArtifactCreatedAt(t, store, id2, -5*time.Minute)

	// Artifact 3: OLD 'pending' state (should remain unchanged)
	pendingArtifact := Artifact{
		UserID:       userID,
		MessageID:    102,
		FileType:     "image",
		FilePath:     "/test/pending.jpg",
		FileSize:     512,
		MimeType:     "image/jpeg",
		OriginalName: "pending.jpg",
		ContentHash:  "pendingHash789",
		State:        "pending",
	}
	id3, err := store.AddArtifact(pendingArtifact)
	assert.NoError(t, err)
	// Set created_at to 20 minutes ago via SQL
	setArtifactCreatedAt(t, store, id3, -20*time.Minute)

	// Run recovery with 10 minute threshold
	err = store.RecoverArtifactStates(10 * time.Minute)
	assert.NoError(t, err)

	// Verify results
	// Artifact 1 (OLD processing) should be recovered to 'pending'
	a1, err := store.GetArtifact(userID, id1)
	assert.NoError(t, err)
	assert.Equal(t, "pending", a1.State, "old processing artifact should be recovered")

	// Artifact 2 (RECENT processing) should remain 'processing'
	a2, err := store.GetArtifact(userID, id2)
	assert.NoError(t, err)
	assert.Equal(t, "processing", a2.State, "recent processing artifact should NOT be recovered")

	// Artifact 3 (pending) should remain 'pending'
	a3, err := store.GetArtifact(userID, id3)
	assert.NoError(t, err)
	assert.Equal(t, "pending", a3.State, "pending artifact should remain unchanged")
}

func TestRecoverArtifactStates_NoArtifactsToRecover(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(1)

	// Only recent artifacts in 'processing'
	recentArtifact := Artifact{
		UserID:       userID,
		MessageID:    100,
		FileType:     "image",
		FilePath:     "/test/recent.jpg",
		FileSize:     1024,
		MimeType:     "image/jpeg",
		OriginalName: "recent.jpg",
		ContentHash:  "recentHash",
		State:        "processing",
	}
	_, err := store.AddArtifact(recentArtifact)
	assert.NoError(t, err)
	// Set created_at to 5 minutes ago via SQL
	setArtifactCreatedAt(t, store, 1, -5*time.Minute)

	// Run recovery - should not recover anything (10 min threshold)
	err = store.RecoverArtifactStates(10 * time.Minute)
	assert.NoError(t, err)

	// Verify still in 'processing'
	a, err := store.GetArtifact(userID, 1)
	assert.NoError(t, err)
	assert.Equal(t, "processing", a.State)
}

func TestRecoverArtifactStates_AllOldArtifacts(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(1)

	// Multiple old artifacts in 'processing'
	for i := 0; i < 3; i++ {
		artifact := Artifact{
			UserID:       userID,
			MessageID:    int64(100 + i),
			FileType:     "image",
			FilePath:     "/test/image.jpg",
			FileSize:     1024,
			MimeType:     "image/jpeg",
			OriginalName: "image.jpg",
			ContentHash:  fmt.Sprintf("hash%d", i),
			State:        "processing",
		}
		_, err := store.AddArtifact(artifact)
		assert.NoError(t, err)
		// Set created_at to 15 minutes ago via SQL
		setArtifactCreatedAt(t, store, int64(i+1), -15*time.Minute)
	}

	// Run recovery with 10 minute threshold
	err := store.RecoverArtifactStates(10 * time.Minute)
	assert.NoError(t, err)

	// Verify all were recovered to 'pending'
	filter := ArtifactFilter{UserID: userID, State: "pending"}
	artifacts, total, err := store.GetArtifacts(filter, 10, 0)
	assert.NoError(t, err)
	assert.Equal(t, 3, int(total))
	assert.Equal(t, 3, len(artifacts))

	for _, a := range artifacts {
		assert.Equal(t, "pending", a.State)
	}
}

// Helper function to set created_at directly in DB for testing
func setArtifactCreatedAt(t *testing.T, store *SQLiteStore, artifactID int64, offset time.Duration) {
	query := `UPDATE artifacts SET created_at = datetime('now', ?) WHERE id = ?`
	minutes := int(offset.Minutes())
	_, err := store.db.Exec(query, fmt.Sprintf("%d minutes", minutes), artifactID)
	assert.NoError(t, err, "failed to set created_at for testing")
}

// TestGetArtifacts_RequiresUserID verifies that GetArtifacts enforces user isolation.
func TestGetArtifacts_RequiresUserID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	// Attempt to call GetArtifacts without UserID
	filter := ArtifactFilter{State: "ready"}
	_, _, err := store.GetArtifacts(filter, 100, 0)

	// Should return error
	assert.Error(t, err, "GetArtifacts should require UserID")
	assert.Contains(t, err.Error(), "UserID required")
}

// TestGetArtifacts_UserIsolation verifies that GetArtifacts only returns artifacts for the specified user.
func TestGetArtifacts_UserIsolation(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	user1ID := int64(100)
	user2ID := int64(200)

	// Create artifact for user 1
	artifact1 := Artifact{
		UserID:       user1ID,
		MessageID:    1,
		FileType:     "image",
		FilePath:     "/user1/file.jpg",
		FileSize:     1024,
		MimeType:     "image/jpeg",
		OriginalName: "user1_file.jpg",
		ContentHash:  "hash1",
		State:        "ready",
	}
	id1, err := store.AddArtifact(artifact1)
	assert.NoError(t, err)

	// Create artifact for user 2
	artifact2 := Artifact{
		UserID:       user2ID,
		MessageID:    2,
		FileType:     "pdf",
		FilePath:     "/user2/doc.pdf",
		FileSize:     2048,
		MimeType:     "application/pdf",
		OriginalName: "user2_doc.pdf",
		ContentHash:  "hash2",
		State:        "ready",
	}
	id2, err := store.AddArtifact(artifact2)
	assert.NoError(t, err)

	// Query for user 1 artifacts
	filter1 := ArtifactFilter{UserID: user1ID, State: "ready"}
	artifacts1, total1, err := store.GetArtifacts(filter1, 100, 0)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), total1, "user 1 should have 1 artifact")
	assert.Len(t, artifacts1, 1, "user 1 should have 1 artifact")
	assert.Equal(t, id1, artifacts1[0].ID, "user 1 should get their own artifact")

	// Query for user 2 artifacts
	filter2 := ArtifactFilter{UserID: user2ID, State: "ready"}
	artifacts2, total2, err := store.GetArtifacts(filter2, 100, 0)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), total2, "user 2 should have 1 artifact")
	assert.Len(t, artifacts2, 1, "user 2 should have 1 artifact")
	assert.Equal(t, id2, artifacts2[0].ID, "user 2 should get their own artifact")
}

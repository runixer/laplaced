package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestGetPendingArtifacts(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)

	// Create pending artifact
	pendingArtifact := Artifact{
		UserID:       userID,
		MessageID:    1,
		FileType:     "image",
		FilePath:     "/test/pending.jpg",
		FileSize:     1024,
		MimeType:     "image/jpeg",
		OriginalName: "pending.jpg",
		ContentHash:  "pendinghash",
		State:        "pending",
	}
	_, err := store.AddArtifact(pendingArtifact)
	assert.NoError(t, err)

	// Create ready artifact (should not be included)
	readyArtifact := Artifact{
		UserID:       userID,
		MessageID:    2,
		FileType:     "image",
		FilePath:     "/test/ready.jpg",
		FileSize:     2048,
		MimeType:     "image/jpeg",
		OriginalName: "ready.jpg",
		ContentHash:  "readyhash",
		State:        "ready",
	}
	_, err = store.AddArtifact(readyArtifact)
	assert.NoError(t, err)

	// Create failed artifact (eligible for retry with backoff)
	errMsg := "processing failed"
	failedArtifact := Artifact{
		UserID:       userID,
		MessageID:    3,
		FileType:     "pdf",
		FilePath:     "/test/failed.pdf",
		FileSize:     4096,
		MimeType:     "application/pdf",
		OriginalName: "failed.pdf",
		ContentHash:  "failedhash",
		State:        "failed",
		ErrorMessage: &errMsg,
		RetryCount:   0,
	}
	id3, err := store.AddArtifact(failedArtifact)
	assert.NoError(t, err)
	// Set last_failed_at to 2 minutes ago (beyond 1 min backoff for retry 0)
	setArtifactLastFailedAt(t, store, id3, -2*time.Minute)

	t.Run("get pending artifacts", func(t *testing.T) {
		pending, err := store.GetPendingArtifacts(userID, 3)
		assert.NoError(t, err)
		assert.Len(t, pending, 2, "should return pending and retry-eligible failed")

		// Find pending and failed artifacts
		var foundPending, foundFailed bool
		for _, a := range pending {
			if a.State == "pending" {
				foundPending = true
			}
			if a.State == "failed" {
				foundFailed = true
			}
		}
		assert.True(t, foundPending, "should include pending artifact")
		assert.True(t, foundFailed, "should include failed artifact within backoff")
	})

	t.Run("failed artifact not yet eligible for retry", func(t *testing.T) {
		// Create another failed artifact with last_failed_at = 30 seconds ago
		recentFailed := Artifact{
			UserID:       userID,
			MessageID:    4,
			FileType:     "voice",
			FilePath:     "/test/recent.ogg",
			FileSize:     512,
			MimeType:     "audio/ogg",
			OriginalName: "recent.ogg",
			ContentHash:  "recentfailedhash",
			State:        "failed",
			ErrorMessage: &errMsg,
			RetryCount:   0,
		}
		id4, err := store.AddArtifact(recentFailed)
		assert.NoError(t, err)
		setArtifactLastFailedAt(t, store, id4, -30*time.Second)

		pending, err := store.GetPendingArtifacts(userID, 3)
		assert.NoError(t, err)
		assert.Len(t, pending, 2, "should not include recently failed artifact")
	})

	t.Run("max retries exhausted", func(t *testing.T) {
		// Use a fresh database for this test to avoid accumulation
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// Create failed artifact with retry_count = 3 (max)
		exhaustedFailed := Artifact{
			UserID:       userID,
			MessageID:    5,
			FileType:     "document",
			FilePath:     "/test/exhausted.txt",
			FileSize:     256,
			MimeType:     "text/plain",
			OriginalName: "exhausted.txt",
			ContentHash:  "exhaustedhash",
			State:        "failed",
			ErrorMessage: &errMsg,
		}
		exhaustedID, err := store2.AddArtifact(exhaustedFailed)
		assert.NoError(t, err)
		// Set retry_count directly via SQL since AddArtifact doesn't persist it
		setArtifactRetryCount(t, store2, exhaustedID, 3)

		pending, err := store2.GetPendingArtifacts(userID, 3)
		assert.NoError(t, err)
		assert.Len(t, pending, 0, "should not include exhausted retries")

		// Also verify retry_count 2 with backoff IS included
		failedRetry2 := Artifact{
			UserID:       userID,
			MessageID:    6,
			FileType:     "document",
			FilePath:     "/test/retry2.txt",
			FileSize:     128,
			MimeType:     "text/plain",
			OriginalName: "retry2.txt",
			ContentHash:  "retry2hash",
			State:        "failed",
			ErrorMessage: &errMsg,
		}
		retry2ID, err := store2.AddArtifact(failedRetry2)
		assert.NoError(t, err)
		setArtifactRetryCount(t, store2, retry2ID, 2)
		// Set last_failed_at > 30 min ago to be eligible
		setArtifactLastFailedAt(t, store2, retry2ID, -31*time.Minute)

		pending2, err := store2.GetPendingArtifacts(userID, 3)
		assert.NoError(t, err)
		assert.Len(t, pending2, 1, "should include retry_count=2 with sufficient backoff")
		assert.Equal(t, retry2ID, pending2[0].ID)
		assert.Equal(t, 2, pending2[0].RetryCount)
	})
}

func TestUpdateArtifact(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)

	artifact := Artifact{
		UserID:       userID,
		MessageID:    1,
		FileType:     "image",
		FilePath:     "/test/image.jpg",
		FileSize:     1024,
		MimeType:     "image/jpeg",
		OriginalName: "image.jpg",
		ContentHash:  "hash123",
		State:        "pending",
	}
	id, err := store.AddArtifact(artifact)
	assert.NoError(t, err)

	t.Run("update to ready state", func(t *testing.T) {
		summary := "A beautiful sunset photo"
		keywords := `["sunset", "nature", "photo"]`
		entities := `["sunset", "nature"]`
		ragHints := `["what time was sunset?", "describe the colors"]`
		embedding := []float32{0.1, 0.2, 0.3}

		updated := Artifact{
			ID:         id,
			UserID:     userID,
			State:      "ready",
			Summary:    &summary,
			Keywords:   &keywords,
			Entities:   &entities,
			RAGHints:   &ragHints,
			Embedding:  embedding,
			RetryCount: 0,
		}

		err := store.UpdateArtifact(updated)
		assert.NoError(t, err)

		// Verify update
		fetched, err := store.GetArtifact(userID, id)
		assert.NoError(t, err)
		assert.Equal(t, "ready", fetched.State)
		assert.Equal(t, summary, *fetched.Summary)
		assert.Equal(t, keywords, *fetched.Keywords)
		assert.Equal(t, entities, *fetched.Entities)
		assert.Equal(t, ragHints, *fetched.RAGHints)
		assert.NotNil(t, fetched.ProcessedAt)
		assert.Equal(t, 3, len(fetched.Embedding))
	})

	t.Run("update to failed state", func(t *testing.T) {
		errMsg := "LLM timeout"
		lastFailed := time.Now().UTC()

		failed := Artifact{
			ID:           id,
			UserID:       userID,
			State:        "failed",
			ErrorMessage: &errMsg,
			RetryCount:   1,
			LastFailedAt: &lastFailed,
		}

		err := store.UpdateArtifact(failed)
		assert.NoError(t, err)

		// Verify update
		fetched, err := store.GetArtifact(userID, id)
		assert.NoError(t, err)
		assert.Equal(t, "failed", fetched.State)
		assert.Equal(t, errMsg, *fetched.ErrorMessage)
		assert.Equal(t, 1, fetched.RetryCount)
		assert.NotNil(t, fetched.LastFailedAt)
	})

	t.Run("update non-existent artifact returns error", func(t *testing.T) {
		nonExistent := Artifact{
			ID:     99999,
			UserID: userID,
			State:  "ready",
		}

		err := store.UpdateArtifact(nonExistent)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

func TestGetArtifactsByIDs(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	userID2 := int64(456)

	// Create artifacts for user 1
	artifact1 := Artifact{
		UserID:       userID,
		MessageID:    1,
		FileType:     "image",
		FilePath:     "/test/img1.jpg",
		FileSize:     1024,
		MimeType:     "image/jpeg",
		OriginalName: "img1.jpg",
		ContentHash:  "hash1",
		State:        "ready",
	}
	id1, err := store.AddArtifact(artifact1)
	assert.NoError(t, err)

	artifact2 := Artifact{
		UserID:       userID,
		MessageID:    2,
		FileType:     "pdf",
		FilePath:     "/test/doc1.pdf",
		FileSize:     2048,
		MimeType:     "application/pdf",
		OriginalName: "doc1.pdf",
		ContentHash:  "hash2",
		State:        "ready",
	}
	id2, err := store.AddArtifact(artifact2)
	assert.NoError(t, err)

	// Create artifact for user 2 (same ID range but different user)
	artifact3 := Artifact{
		UserID:       userID2,
		MessageID:    3,
		FileType:     "image",
		FilePath:     "/test/img2.jpg",
		FileSize:     512,
		MimeType:     "image/jpeg",
		OriginalName: "img2.jpg",
		ContentHash:  "hash3",
		State:        "ready",
	}
	id3, err := store.AddArtifact(artifact3)
	assert.NoError(t, err)

	t.Run("get multiple artifacts by IDs", func(t *testing.T) {
		artifacts, err := store.GetArtifactsByIDs(userID, []int64{id1, id2})
		assert.NoError(t, err)
		assert.Len(t, artifacts, 2)

		// Should be ordered by ID ASC
		assert.Equal(t, id1, artifacts[0].ID)
		assert.Equal(t, id2, artifacts[1].ID)
	})

	t.Run("get single artifact by ID", func(t *testing.T) {
		artifacts, err := store.GetArtifactsByIDs(userID, []int64{id1})
		assert.NoError(t, err)
		assert.Len(t, artifacts, 1)
		assert.Equal(t, id1, artifacts[0].ID)
	})

	t.Run("empty ID list returns empty", func(t *testing.T) {
		artifacts, err := store.GetArtifactsByIDs(userID, []int64{})
		assert.NoError(t, err)
		assert.Nil(t, artifacts)
	})

	t.Run("user isolation - different user gets nothing", func(t *testing.T) {
		// Try to get user 2's artifact as user 1
		artifacts, err := store.GetArtifactsByIDs(userID, []int64{id3})
		assert.NoError(t, err)
		assert.Len(t, artifacts, 0)
	})

	t.Run("mixed valid and invalid IDs", func(t *testing.T) {
		artifacts, err := store.GetArtifactsByIDs(userID, []int64{id1, 99999, id2})
		assert.NoError(t, err)
		assert.Len(t, artifacts, 2, "should only return existing artifacts")
	})
}

func TestIncrementContextLoadCount(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)

	// Create artifacts
	artifact1 := Artifact{
		UserID:       userID,
		MessageID:    1,
		FileType:     "image",
		FilePath:     "/test/img1.jpg",
		FileSize:     1024,
		MimeType:     "image/jpeg",
		OriginalName: "img1.jpg",
		ContentHash:  "hash1",
		State:        "ready",
	}
	id1, err := store.AddArtifact(artifact1)
	assert.NoError(t, err)

	artifact2 := Artifact{
		UserID:       userID,
		MessageID:    2,
		FileType:     "pdf",
		FilePath:     "/test/doc1.pdf",
		FileSize:     2048,
		MimeType:     "application/pdf",
		OriginalName: "doc1.pdf",
		ContentHash:  "hash2",
		State:        "ready",
	}
	id2, err := store.AddArtifact(artifact2)
	assert.NoError(t, err)

	t.Run("increment single artifact", func(t *testing.T) {
		err := store.IncrementContextLoadCount(userID, []int64{id1})
		assert.NoError(t, err)

		// Verify
		fetched, err := store.GetArtifact(userID, id1)
		assert.NoError(t, err)
		assert.Equal(t, 1, fetched.ContextLoadCount)
		assert.NotNil(t, fetched.LastLoadedAt)
	})

	t.Run("increment multiple artifacts", func(t *testing.T) {
		err := store.IncrementContextLoadCount(userID, []int64{id1, id2})
		assert.NoError(t, err)

		// Verify id1 (should be 2 now: 1 from previous test + 1)
		fetched1, _ := store.GetArtifact(userID, id1)
		assert.Equal(t, 2, fetched1.ContextLoadCount)

		// Verify id2 (should be 1)
		fetched2, _ := store.GetArtifact(userID, id2)
		assert.Equal(t, 1, fetched2.ContextLoadCount)
	})

	t.Run("empty ID list does nothing", func(t *testing.T) {
		err := store.IncrementContextLoadCount(userID, []int64{})
		assert.NoError(t, err)
	})
}

func TestUpdateMessageID(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)

	artifact := Artifact{
		UserID:       userID,
		MessageID:    0, // Initially 0
		FileType:     "image",
		FilePath:     "/test/image.jpg",
		FileSize:     1024,
		MimeType:     "image/jpeg",
		OriginalName: "image.jpg",
		ContentHash:  "hash123",
		State:        "ready",
	}
	id, err := store.AddArtifact(artifact)
	assert.NoError(t, err)

	t.Run("update message ID successfully", func(t *testing.T) {
		newMessageID := int64(456)
		err := store.UpdateMessageID(userID, id, newMessageID)
		assert.NoError(t, err)

		// Verify
		fetched, err := store.GetArtifact(userID, id)
		assert.NoError(t, err)
		assert.Equal(t, newMessageID, fetched.MessageID)
	})

	t.Run("update again", func(t *testing.T) {
		newMessageID := int64(789)
		err := store.UpdateMessageID(userID, id, newMessageID)
		assert.NoError(t, err)

		fetched, _ := store.GetArtifact(userID, id)
		assert.Equal(t, newMessageID, fetched.MessageID)
	})

	t.Run("non-existent artifact returns error", func(t *testing.T) {
		err := store.UpdateMessageID(userID, 99999, 123)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("wrong user ID returns error", func(t *testing.T) {
		err := store.UpdateMessageID(999, id, 123)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

// Helper function to set last_failed_at directly in DB for testing
func setArtifactLastFailedAt(t *testing.T, store *SQLiteStore, artifactID int64, offset time.Duration) {
	query := `UPDATE artifacts SET last_failed_at = datetime('now', ?) WHERE id = ?`
	minutes := int(offset.Minutes())
	_, err := store.db.Exec(query, fmt.Sprintf("%d minutes", minutes), artifactID)
	assert.NoError(t, err, "failed to set last_failed_at for testing")
}

// Helper function to set retry_count directly in DB for testing
func setArtifactRetryCount(t *testing.T, store *SQLiteStore, artifactID int64, retryCount int) {
	query := `UPDATE artifacts SET retry_count = ? WHERE id = ?`
	_, err := store.db.Exec(query, retryCount, artifactID)
	assert.NoError(t, err, "failed to set retry_count for testing")
}

// addSessionMessage inserts a history row for the given user and returns its id.
// topicID==nil keeps it in active session (topic_id IS NULL).
func addSessionMessage(t *testing.T, store *SQLiteStore, userID int64, topicID *int64) int64 {
	t.Helper()
	res, err := store.db.Exec(
		"INSERT INTO history (user_id, role, content, topic_id) VALUES (?, ?, ?, ?)",
		userID, "user", "test message", topicID,
	)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// TestGetSessionArtifacts verifies that session-active artifacts are returned correctly,
// including filters for user isolation, message_id, state, age, and topic assignment.
func TestGetSessionArtifacts(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	require.NoError(t, store.Init())

	ctx := context.Background()
	user1ID := int64(100)
	user2ID := int64(200)

	t.Run("returns only session-active artifacts of caller", func(t *testing.T) {
		// user1 has a session message + ready artifact
		msg1 := addSessionMessage(t, store, user1ID, nil)
		a1, err := store.AddArtifact(Artifact{
			UserID: user1ID, MessageID: msg1, FileType: "image",
			FilePath: "/u1/a.png", FileSize: 100, MimeType: "image/png",
			OriginalName: "a.png", ContentHash: "h_a", State: "ready",
		})
		require.NoError(t, err)

		// user2 has its own session message + artifact
		msg2 := addSessionMessage(t, store, user2ID, nil)
		_, err = store.AddArtifact(Artifact{
			UserID: user2ID, MessageID: msg2, FileType: "image",
			FilePath: "/u2/b.png", FileSize: 100, MimeType: "image/png",
			OriginalName: "b.png", ContentHash: "h_b", State: "ready",
		})
		require.NoError(t, err)

		got, err := store.GetSessionArtifacts(ctx, user1ID, 10, 24*time.Hour)
		require.NoError(t, err)
		require.Len(t, got, 1, "user1 sees only its own session artifact")
		assert.Equal(t, a1, got[0].ID)
		assert.Equal(t, user1ID, got[0].UserID)
	})

	t.Run("excludes artifacts with message_id=0 (in-flight assignment)", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		require.NoError(t, store.Init())

		_, err := store.AddArtifact(Artifact{
			UserID: user1ID, MessageID: 0, FileType: "image",
			FilePath: "/u1/inflight.png", FileSize: 100, MimeType: "image/png",
			OriginalName: "inflight.png", ContentHash: "h_inflight", State: "ready",
		})
		require.NoError(t, err)

		got, err := store.GetSessionArtifacts(ctx, user1ID, 10, 24*time.Hour)
		require.NoError(t, err)
		assert.Empty(t, got, "in-flight artifact (message_id=0) must be excluded")
	})

	t.Run("excludes artifacts whose message has been archived to a topic", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		require.NoError(t, store.Init())

		topicID := int64(7)
		msgArchived := addSessionMessage(t, store, user1ID, &topicID)
		_, err := store.AddArtifact(Artifact{
			UserID: user1ID, MessageID: msgArchived, FileType: "image",
			FilePath: "/u1/archived.png", FileSize: 100, MimeType: "image/png",
			OriginalName: "archived.png", ContentHash: "h_archived", State: "ready",
		})
		require.NoError(t, err)

		got, err := store.GetSessionArtifacts(ctx, user1ID, 10, 24*time.Hour)
		require.NoError(t, err)
		assert.Empty(t, got, "archived artifact must not be returned as session")
	})

	t.Run("excludes pending and processing artifacts (state != ready)", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		require.NoError(t, store.Init())

		msg := addSessionMessage(t, store, user1ID, nil)
		_, err := store.AddArtifact(Artifact{
			UserID: user1ID, MessageID: msg, FileType: "image",
			FilePath: "/u1/pending.png", FileSize: 100, MimeType: "image/png",
			OriginalName: "pending.png", ContentHash: "h_pending", State: "pending",
		})
		require.NoError(t, err)

		got, err := store.GetSessionArtifacts(ctx, user1ID, 10, 24*time.Hour)
		require.NoError(t, err)
		assert.Empty(t, got, "pending artifact must not be returned")
	})

	t.Run("respects maxAge cutoff", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		require.NoError(t, store.Init())

		msg := addSessionMessage(t, store, user1ID, nil)
		stale, err := store.AddArtifact(Artifact{
			UserID: user1ID, MessageID: msg, FileType: "image",
			FilePath: "/u1/stale.png", FileSize: 100, MimeType: "image/png",
			OriginalName: "stale.png", ContentHash: "h_stale", State: "ready",
		})
		require.NoError(t, err)
		setArtifactCreatedAt(t, store, stale, -25*time.Hour)

		got, err := store.GetSessionArtifacts(ctx, user1ID, 10, 24*time.Hour)
		require.NoError(t, err)
		assert.Empty(t, got, "artifact older than maxAge must be excluded")

		// Same query with wider maxAge picks it up.
		got, err = store.GetSessionArtifacts(ctx, user1ID, 10, 48*time.Hour)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, stale, got[0].ID)
	})

	t.Run("orders by created_at DESC and respects limit", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		require.NoError(t, store.Init())

		var ids []int64
		for i := 0; i < 4; i++ {
			msg := addSessionMessage(t, store, user1ID, nil)
			id, err := store.AddArtifact(Artifact{
				UserID: user1ID, MessageID: msg, FileType: "image",
				FilePath: fmt.Sprintf("/u1/order%d.png", i), FileSize: 100,
				MimeType: "image/png", OriginalName: fmt.Sprintf("order%d.png", i),
				ContentHash: fmt.Sprintf("h_order_%d", i), State: "ready",
			})
			require.NoError(t, err)
			ids = append(ids, id)
			setArtifactCreatedAt(t, store, id, -time.Duration(i)*time.Minute)
		}

		got, err := store.GetSessionArtifacts(ctx, user1ID, 2, 24*time.Hour)
		require.NoError(t, err)
		require.Len(t, got, 2)
		// Newest two: ids[0] and ids[1] (offsets 0, -1m) — DESC order returns newest first.
		assert.Equal(t, ids[0], got[0].ID, "newest first")
		assert.Equal(t, ids[1], got[1].ID)
	})

	t.Run("rejects empty userID", func(t *testing.T) {
		_, err := store.GetSessionArtifacts(ctx, 0, 10, 24*time.Hour)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "UserID required")
	})

	t.Run("non-positive limit returns nil", func(t *testing.T) {
		got, err := store.GetSessionArtifacts(ctx, user1ID, 0, 24*time.Hour)
		require.NoError(t, err)
		assert.Nil(t, got)
	})
}

// TestArtifactUserIsolation tests comprehensive user isolation for artifacts.
func TestArtifactUserIsolation(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	user1ID := int64(100)
	user2ID := int64(200)

	// Create artifacts for user1
	user1Artifacts := make([]int64, 3)
	for i := 0; i < 3; i++ {
		artifact := Artifact{
			UserID:       user1ID,
			MessageID:    int64(i + 1),
			FileType:     "image",
			FilePath:     fmt.Sprintf("/user1/img%d.jpg", i),
			FileSize:     1024,
			MimeType:     "image/jpeg",
			OriginalName: fmt.Sprintf("img%d.jpg", i),
			ContentHash:  fmt.Sprintf("hash1_%d", i),
			State:        "ready",
		}
		id, err := store.AddArtifact(artifact)
		assert.NoError(t, err)
		user1Artifacts[i] = id
	}

	// Create artifacts for user2
	user2Artifacts := make([]int64, 2)
	for i := 0; i < 2; i++ {
		artifact := Artifact{
			UserID:       user2ID,
			MessageID:    int64(i + 10),
			FileType:     "pdf",
			FilePath:     fmt.Sprintf("/user2/doc%d.pdf", i),
			FileSize:     2048,
			MimeType:     "application/pdf",
			OriginalName: fmt.Sprintf("doc%d.pdf", i),
			ContentHash:  fmt.Sprintf("hash2_%d", i),
			State:        "ready",
		}
		id, err := store.AddArtifact(artifact)
		assert.NoError(t, err)
		user2Artifacts[i] = id
	}

	t.Run("GetArtifact - user cannot get other user's artifact by ID", func(t *testing.T) {
		// User1 tries to get user2's artifact
		artifact, err := store.GetArtifact(user1ID, user2Artifacts[0])
		assert.NoError(t, err, "no error for non-existent artifact")
		assert.Nil(t, artifact, "user1 should not be able to get user2's artifact")

		// User1 can get their own artifact
		artifact, err = store.GetArtifact(user1ID, user1Artifacts[0])
		assert.NoError(t, err)
		assert.Equal(t, user1ID, artifact.UserID)
		assert.Equal(t, user1Artifacts[0], artifact.ID)
	})

	t.Run("GetPendingArtifacts - only returns own artifacts", func(t *testing.T) {
		// Create a fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// Create pending artifacts for both users
		pending1 := Artifact{
			UserID:    user1ID,
			MessageID: 1,
			FileType:  "image",
			FilePath:  "/u1/pending.jpg",
			FileSize:  1024,
			MimeType:  "image/jpeg",
			State:     "pending",
		}
		_, _ = store2.AddArtifact(pending1)

		pending2 := Artifact{
			UserID:    user2ID,
			MessageID: 2,
			FileType:  "pdf",
			FilePath:  "/u2/pending.pdf",
			FileSize:  2048,
			MimeType:  "application/pdf",
			State:     "pending",
		}
		_, _ = store2.AddArtifact(pending2)

		// Get pending for user1
		pending, err := store2.GetPendingArtifacts(user1ID, 10)
		assert.NoError(t, err)
		assert.Len(t, pending, 1, "user1 should only see their own pending artifact")
		assert.Equal(t, user1ID, pending[0].UserID)
	})

	t.Run("UpdateArtifact - user cannot update other user's artifact", func(t *testing.T) {
		summary := "Updated summary"
		updated := Artifact{
			ID:      user1Artifacts[0],
			UserID:  user2ID, // Try to update user1's artifact as user2
			State:   "ready",
			Summary: &summary,
		}

		err := store.UpdateArtifact(updated)
		assert.Error(t, err, "user2 should not be able to update user1's artifact")
		assert.Contains(t, err.Error(), "not found")

		// Verify user1's artifact was NOT updated
		artifact, _ := store.GetArtifact(user1ID, user1Artifacts[0])
		assert.Nil(t, artifact.Summary, "artifact should not be updated by other user")

		// User1 can update their own artifact
		updated.UserID = user1ID
		err = store.UpdateArtifact(updated)
		assert.NoError(t, err)

		artifact, _ = store.GetArtifact(user1ID, user1Artifacts[0])
		assert.Equal(t, summary, *artifact.Summary, "artifact should be updated by owner")
	})

	t.Run("IncrementContextLoadCount - user cannot increment other user's artifact", func(t *testing.T) {
		// User2 tries to increment user1's artifact counter
		err := store.IncrementContextLoadCount(user2ID, []int64{user1Artifacts[0]})
		assert.NoError(t, err) // No error, but SQL won't find the artifact

		// Verify user1's artifact counter was NOT incremented
		artifact, _ := store.GetArtifact(user1ID, user1Artifacts[0])
		assert.Equal(t, 0, artifact.ContextLoadCount, "counter should not be incremented by other user")

		// User1 can increment their own artifact
		err = store.IncrementContextLoadCount(user1ID, []int64{user1Artifacts[0]})
		assert.NoError(t, err)

		artifact, _ = store.GetArtifact(user1ID, user1Artifacts[0])
		assert.Equal(t, 1, artifact.ContextLoadCount, "counter should be incremented by owner")
	})

	t.Run("UpdateMessageID - user cannot update other user's artifact", func(t *testing.T) {
		// User2 tries to update user1's artifact message_id
		err := store.UpdateMessageID(user2ID, user1Artifacts[0], 999)
		assert.Error(t, err, "user2 should not be able to update user1's artifact")
		assert.Contains(t, err.Error(), "not found")

		// Verify user1's artifact message_id was NOT updated
		artifact, _ := store.GetArtifact(user1ID, user1Artifacts[0])
		assert.NotEqual(t, int64(999), artifact.MessageID, "message_id should not be updated by other user")

		// User1 can update their own artifact
		newMessageID := int64(555)
		err = store.UpdateMessageID(user1ID, user1Artifacts[0], newMessageID)
		assert.NoError(t, err)

		artifact, _ = store.GetArtifact(user1ID, user1Artifacts[0])
		assert.Equal(t, newMessageID, artifact.MessageID, "message_id should be updated by owner")
	})

	t.Run("RecoverArtifactStates - only affects artifacts in processing state", func(t *testing.T) {
		// Create fresh store for this test
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// Create old processing artifacts for both users
		artifact1 := Artifact{
			UserID:    user1ID,
			MessageID: 1,
			FileType:  "image",
			FilePath:  "/u1/proc.jpg",
			FileSize:  1024,
			MimeType:  "image/jpeg",
			State:     "processing",
		}
		id1, _ := store2.AddArtifact(artifact1)
		setArtifactCreatedAt(t, store2, id1, -15*time.Minute)

		artifact2 := Artifact{
			UserID:    user2ID,
			MessageID: 2,
			FileType:  "pdf",
			FilePath:  "/u2/proc.pdf",
			FileSize:  2048,
			MimeType:  "application/pdf",
			State:     "processing",
		}
		id2, _ := store2.AddArtifact(artifact2)
		setArtifactCreatedAt(t, store2, id2, -15*time.Minute)

		// Run recovery
		_ = store2.RecoverArtifactStates(10 * time.Minute)

		// Both artifacts should be recovered (this is cross-user by design for background processing)
		a1, _ := store2.GetArtifact(user1ID, id1)
		assert.Equal(t, "pending", a1.State)

		a2, _ := store2.GetArtifact(user2ID, id2)
		assert.Equal(t, "pending", a2.State)
	})
}

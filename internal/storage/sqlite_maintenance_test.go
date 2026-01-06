package storage

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetDBSize(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "test-db-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store, err := NewSQLiteStore(logger, dbPath)
	require.NoError(t, err)
	defer store.Close()

	err = store.Init()
	require.NoError(t, err)

	// Get DB size
	size, err := store.GetDBSize()
	require.NoError(t, err)
	assert.Greater(t, size, int64(0), "DB size should be greater than 0")
}

func TestGetTableSizes(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "test-db-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store, err := NewSQLiteStore(logger, dbPath)
	require.NoError(t, err)
	defer store.Close()

	err = store.Init()
	require.NoError(t, err)

	// Insert some data
	_, err = store.db.Exec("INSERT INTO users (id, username) VALUES (1, 'testuser')")
	require.NoError(t, err)

	// Get table sizes
	sizes, err := store.GetTableSizes()
	require.NoError(t, err)
	assert.NotEmpty(t, sizes, "Table sizes should not be empty")

	// Check that known tables exist
	tableNames := make(map[string]bool)
	for _, ts := range sizes {
		tableNames[ts.Name] = true
	}
	assert.True(t, tableNames["users"] || tableNames["history"] || tableNames["topics"],
		"Should have at least one expected table")
}

func TestCleanupFactHistory(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "test-db-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store, err := NewSQLiteStore(logger, dbPath)
	require.NoError(t, err)
	defer store.Close()

	err = store.Init()
	require.NoError(t, err)

	// Insert test data: 5 records for user 1, 3 records for user 2
	for i := 0; i < 5; i++ {
		err = store.AddFactHistory(FactHistory{
			FactID:     int64(i + 1),
			UserID:     1,
			Action:     "add",
			NewContent: "fact content",
		})
		require.NoError(t, err)
	}
	for i := 0; i < 3; i++ {
		err = store.AddFactHistory(FactHistory{
			FactID:     int64(i + 10),
			UserID:     2,
			Action:     "add",
			NewContent: "fact content",
		})
		require.NoError(t, err)
	}

	// Keep 2 per user - should delete 3 for user 1, 1 for user 2 = 4 total
	deleted, err := store.CleanupFactHistory(2)
	require.NoError(t, err)
	assert.Equal(t, int64(4), deleted, "Should delete 4 records (3 from user 1, 1 from user 2)")

	// Verify remaining records
	var count int
	err = store.db.QueryRow("SELECT COUNT(*) FROM fact_history").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 4, count, "Should have 4 records remaining (2 per user)")
}

func TestCleanupRagLogs(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "test-db-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store, err := NewSQLiteStore(logger, dbPath)
	require.NoError(t, err)
	defer store.Close()

	err = store.Init()
	require.NoError(t, err)

	// Insert test data: 4 records for user 1, 2 records for user 2
	for i := 0; i < 4; i++ {
		err = store.AddRAGLog(RAGLog{
			UserID:        1,
			OriginalQuery: "test query",
		})
		require.NoError(t, err)
	}
	for i := 0; i < 2; i++ {
		err = store.AddRAGLog(RAGLog{
			UserID:        2,
			OriginalQuery: "test query",
		})
		require.NoError(t, err)
	}

	// Keep 2 per user - should delete 2 for user 1, 0 for user 2 = 2 total
	deleted, err := store.CleanupRagLogs(2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted, "Should delete 2 records (2 from user 1)")

	// Verify remaining records
	var count int
	err = store.db.QueryRow("SELECT COUNT(*) FROM rag_logs").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 4, count, "Should have 4 records remaining (2 for user 1, 2 for user 2)")
}

func TestContaminatedTopicsDetection(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "test-db-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store, err := NewSQLiteStore(logger, dbPath)
	require.NoError(t, err)
	defer store.Close()

	err = store.Init()
	require.NoError(t, err)

	user1 := int64(100)
	user2 := int64(200)

	// Create topic owned by user1
	topicID, err := store.AddTopic(Topic{
		UserID:     user1,
		Summary:    "User1's topic",
		StartMsgID: 1,
		EndMsgID:   10,
	})
	require.NoError(t, err)

	t.Run("detects contamination when ALL messages from foreign user", func(t *testing.T) {
		// Add messages from user2 to user1's topic (simulating contamination)
		// This is the edge case: topic owner has NO messages, only foreign user
		_, err = store.db.Exec("INSERT INTO history (user_id, role, content, topic_id) VALUES (?, 'user', 'msg1', ?)", user2, topicID)
		require.NoError(t, err)
		_, err = store.db.Exec("INSERT INTO history (user_id, role, content, topic_id) VALUES (?, 'user', 'msg2', ?)", user2, topicID)
		require.NoError(t, err)

		// Should detect contamination
		count, err := store.CountContaminatedTopics(0)
		require.NoError(t, err)
		assert.Equal(t, 1, count, "Should detect 1 contaminated topic")

		topics, err := store.GetContaminatedTopics(0)
		require.NoError(t, err)
		require.Len(t, topics, 1)
		assert.Equal(t, topicID, topics[0].TopicID)
		assert.Equal(t, user1, topics[0].TopicOwner)
		assert.Equal(t, 2, topics[0].ForeignMsgCnt)
		assert.Contains(t, topics[0].ForeignUsers, user2)
	})

	t.Run("detects contamination when mixed messages", func(t *testing.T) {
		// Add a message from the actual owner
		_, err = store.db.Exec("INSERT INTO history (user_id, role, content, topic_id) VALUES (?, 'user', 'owner msg', ?)", user1, topicID)
		require.NoError(t, err)

		// Should still detect contamination (2 foreign + 1 owner)
		count, err := store.CountContaminatedTopics(0)
		require.NoError(t, err)
		assert.Equal(t, 1, count, "Should still detect 1 contaminated topic")

		topics, err := store.GetContaminatedTopics(0)
		require.NoError(t, err)
		require.Len(t, topics, 1)
		assert.Equal(t, 3, topics[0].TotalMsgCnt)
		assert.Equal(t, 2, topics[0].ForeignMsgCnt)
	})
}

func TestFixContaminatedTopics(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "test-db-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store, err := NewSQLiteStore(logger, dbPath)
	require.NoError(t, err)
	defer store.Close()

	err = store.Init()
	require.NoError(t, err)

	user1 := int64(100)
	user2 := int64(200)

	// Create topic owned by user1
	topicID, err := store.AddTopic(Topic{
		UserID:     user1,
		Summary:    "User1's topic",
		StartMsgID: 1,
		EndMsgID:   10,
	})
	require.NoError(t, err)

	t.Run("fixes contamination when ALL messages from foreign user", func(t *testing.T) {
		// Add messages ONLY from user2 to user1's topic (edge case)
		_, err = store.db.Exec("INSERT INTO history (user_id, role, content, topic_id) VALUES (?, 'user', 'foreign1', ?)", user2, topicID)
		require.NoError(t, err)
		_, err = store.db.Exec("INSERT INTO history (user_id, role, content, topic_id) VALUES (?, 'user', 'foreign2', ?)", user2, topicID)
		require.NoError(t, err)

		// Verify contamination detected
		count, err := store.CountContaminatedTopics(0)
		require.NoError(t, err)
		assert.Equal(t, 1, count, "Should detect 1 contaminated topic before fix")

		// Fix contamination
		unlinked, err := store.FixContaminatedTopics(0)
		require.NoError(t, err)
		assert.Equal(t, int64(2), unlinked, "Should unlink 2 foreign messages")

		// Verify no more contamination
		count, err = store.CountContaminatedTopics(0)
		require.NoError(t, err)
		assert.Equal(t, 0, count, "Should have 0 contaminated topics after fix")

		// Verify messages are unlinked (topic_id = NULL)
		var nullCount int
		err = store.db.QueryRow("SELECT COUNT(*) FROM history WHERE topic_id IS NULL AND user_id = ?", user2).Scan(&nullCount)
		require.NoError(t, err)
		assert.Equal(t, 2, nullCount, "Foreign messages should have topic_id = NULL")
	})
}

func TestCleanupNoRecordsToDelete(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "test-db-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	store, err := NewSQLiteStore(logger, dbPath)
	require.NoError(t, err)
	defer store.Close()

	err = store.Init()
	require.NoError(t, err)

	// Insert just 2 records
	for i := 0; i < 2; i++ {
		err = store.AddFactHistory(FactHistory{
			FactID:     int64(i + 1),
			UserID:     1,
			Action:     "add",
			NewContent: "fact content",
		})
		require.NoError(t, err)
	}

	// Keep 5 per user - should delete 0
	deleted, err := store.CleanupFactHistory(5)
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted, "Should delete 0 records")
}

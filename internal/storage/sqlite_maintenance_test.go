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

func TestCleanupAgentLogs(t *testing.T) {
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

	// Insert test data: 4 records for user 1 agent "laplace", 3 for user 1 agent "reranker"
	for i := 0; i < 4; i++ {
		err = store.AddAgentLog(AgentLog{
			UserID:    1,
			AgentType: "laplace",
		})
		require.NoError(t, err)
	}
	for i := 0; i < 3; i++ {
		err = store.AddAgentLog(AgentLog{
			UserID:    1,
			AgentType: "reranker",
		})
		require.NoError(t, err)
	}

	// Keep 2 per user per agent - should delete 2 for laplace, 1 for reranker = 3 total
	deleted, err := store.CleanupAgentLogs(2)
	require.NoError(t, err)
	assert.Equal(t, int64(3), deleted, "Should delete 3 records (2 from laplace, 1 from reranker)")

	// Verify remaining records
	var count int
	err = store.db.QueryRow("SELECT COUNT(*) FROM agent_logs").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 4, count, "Should have 4 records remaining (2 per agent)")
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

func TestCountAgentLogs(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	t.Run("count with no logs", func(t *testing.T) {
		count, err := store.CountAgentLogs()
		assert.NoError(t, err)
		assert.Zero(t, count)
	})

	t.Run("count with logs", func(t *testing.T) {
		// Add some agent logs
		for i := 0; i < 5; i++ {
			_ = store.AddAgentLog(AgentLog{
				UserID:    1,
				AgentType: "laplace",
			})
		}

		count, err := store.CountAgentLogs()
		assert.NoError(t, err)
		assert.Equal(t, int64(5), count)
	})
}

func TestCountFactHistory(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	t.Run("count with no history", func(t *testing.T) {
		count, err := store.CountFactHistory()
		assert.NoError(t, err)
		assert.Zero(t, count)
	})

	t.Run("count with history", func(t *testing.T) {
		// Add some fact history
		for i := 0; i < 3; i++ {
			_ = store.AddFactHistory(FactHistory{
				FactID:     int64(i),
				UserID:     1,
				Action:     "add",
				NewContent: "fact",
			})
		}

		count, err := store.CountFactHistory()
		assert.NoError(t, err)
		assert.Equal(t, int64(3), count)
	})
}

func TestOrphanedTopics(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)
	otherUserID := int64(456)

	t.Run("count and get orphaned topics", func(t *testing.T) {
		// Create a topic with no messages (orphaned)
		orphanTopicID, _ := store.AddTopic(Topic{
			UserID:     userID,
			Summary:    "Orphaned topic",
			StartMsgID: 1,
			EndMsgID:   10,
		})

		// Create a topic with messages (not orphaned)
		validTopicID, _ := store.AddTopic(Topic{
			UserID:     userID,
			Summary:    "Valid topic",
			StartMsgID: 11,
			EndMsgID:   20,
		})
		// Add a message to the valid topic
		_ = store.AddMessageToHistory(userID, Message{
			Role:    "user",
			Content: "test message",
			TopicID: &validTopicID,
		})

		// Count orphaned topics (all users)
		count, err := store.CountOrphanedTopics(0)
		assert.NoError(t, err)
		assert.Equal(t, 1, count)

		// Count orphaned topics for specific user
		count, err = store.CountOrphanedTopics(userID)
		assert.NoError(t, err)
		assert.Equal(t, 1, count)

		// Get orphaned topic IDs
		ids, err := store.GetOrphanedTopicIDs(0)
		assert.NoError(t, err)
		assert.Len(t, ids, 1)
		assert.Contains(t, ids, orphanTopicID)

		// Get orphaned topic IDs for specific user
		ids, err = store.GetOrphanedTopicIDs(userID)
		assert.NoError(t, err)
		assert.Len(t, ids, 1)
	})

	t.Run("no orphaned topics", func(t *testing.T) {
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// Create a topic with messages
		topicID, _ := store2.AddTopic(Topic{
			UserID:     otherUserID,
			Summary:    "Valid topic",
			StartMsgID: 1,
			EndMsgID:   10,
		})
		_ = store2.AddMessageToHistory(otherUserID, Message{
			Role:    "user",
			Content: "test message",
			TopicID: &topicID,
		})

		count, err := store2.CountOrphanedTopics(0)
		assert.NoError(t, err)
		assert.Zero(t, count)

		ids, err := store2.GetOrphanedTopicIDs(0)
		assert.NoError(t, err)
		assert.Empty(t, ids)
	})
}

func TestOverlappingTopics(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)

	t.Run("detects overlapping topics", func(t *testing.T) {
		// T1: messages 1-10
		_, _ = store.AddTopic(Topic{
			UserID:     userID,
			Summary:    "Topic 1",
			StartMsgID: 1,
			EndMsgID:   10,
		})

		// T2: messages 5-15 (overlaps with T1)
		_, _ = store.AddTopic(Topic{
			UserID:     userID,
			Summary:    "Topic 2",
			StartMsgID: 5,
			EndMsgID:   15,
		})

		// T3: messages 20-30 (no overlap)
		_, _ = store.AddTopic(Topic{
			UserID:     userID,
			Summary:    "Topic 3",
			StartMsgID: 20,
			EndMsgID:   30,
		})

		// Count overlapping (should find 1 pair: T1-T2)
		count, err := store.CountOverlappingTopics(0)
		assert.NoError(t, err)
		assert.Equal(t, 1, count)

		// Get overlapping pairs
		pairs, err := store.GetOverlappingTopics(0)
		assert.NoError(t, err)
		assert.Len(t, pairs, 1)
		// Query orders by t1.id DESC, so higher ID topic is first
		assert.Contains(t, pairs[0].Topic1Summary, "Topic") // Either Topic 1 or 2 (higher ID)
		assert.Contains(t, pairs[0].Topic2Summary, "Topic") // Either Topic 1 or 2 (lower ID)
	})

	t.Run("adjacent topics do not overlap", func(t *testing.T) {
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// T1: 1-10
		_, _ = store2.AddTopic(Topic{
			UserID:     userID,
			Summary:    "Topic 1",
			StartMsgID: 1,
			EndMsgID:   10,
		})

		// T2: 11-20 (adjacent, not overlapping)
		_, _ = store2.AddTopic(Topic{
			UserID:     userID,
			Summary:    "Topic 2",
			StartMsgID: 11,
			EndMsgID:   20,
		})

		count, err := store2.CountOverlappingTopics(0)
		assert.NoError(t, err)
		assert.Zero(t, count)
	})

	t.Run("no overlapping topics", func(t *testing.T) {
		store3, cleanup3 := setupTestDB(t)
		defer cleanup3()
		_ = store3.Init()

		_, _ = store3.AddTopic(Topic{
			UserID:     userID,
			Summary:    "Only topic",
			StartMsgID: 1,
			EndMsgID:   10,
		})

		count, err := store3.CountOverlappingTopics(0)
		assert.NoError(t, err)
		assert.Zero(t, count)

		pairs, err := store3.GetOverlappingTopics(0)
		assert.NoError(t, err)
		assert.Empty(t, pairs)
	})
}

func TestCountFactsOnOrphanedTopics(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)

	t.Run("counts facts on orphaned topics", func(t *testing.T) {
		// Create orphaned topic (no messages)
		orphanTopicID, _ := store.AddTopic(Topic{
			UserID:     userID,
			Summary:    "Orphaned",
			StartMsgID: 1,
			EndMsgID:   10,
		})

		// Create valid topic (with messages)
		validTopicID, _ := store.AddTopic(Topic{
			UserID:     userID,
			Summary:    "Valid",
			StartMsgID: 11,
			EndMsgID:   20,
		})
		_ = store.AddMessageToHistory(userID, Message{
			Role:    "user",
			Content: "msg",
			TopicID: &validTopicID,
		})

		// Add facts to both topics
		_, _ = store.AddFact(Fact{
			UserID:   userID,
			TopicID:  &orphanTopicID,
			Relation: "is",
			Content:  "Orphaned fact",
		})
		_, _ = store.AddFact(Fact{
			UserID:   userID,
			TopicID:  &orphanTopicID,
			Relation: "is",
			Content:  "Another orphaned fact",
		})
		_, _ = store.AddFact(Fact{
			UserID:   userID,
			TopicID:  &validTopicID,
			Relation: "is",
			Content:  "Valid fact",
		})

		// Count facts on orphaned topics (all users)
		count, err := store.CountFactsOnOrphanedTopics(0)
		assert.NoError(t, err)
		assert.Equal(t, 2, count) // Only facts on orphaned topic

		// Count for specific user
		count, err = store.CountFactsOnOrphanedTopics(userID)
		assert.NoError(t, err)
		assert.Equal(t, 2, count)
	})

	t.Run("no facts on orphaned topics", func(t *testing.T) {
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// Create valid topic with fact
		topicID, _ := store2.AddTopic(Topic{
			UserID:     userID,
			Summary:    "Valid",
			StartMsgID: 1,
			EndMsgID:   10,
		})
		_ = store2.AddMessageToHistory(userID, Message{
			Role:    "user",
			Content: "msg",
			TopicID: &topicID,
		})
		_, _ = store2.AddFact(Fact{
			UserID:   userID,
			TopicID:  &topicID,
			Relation: "is",
			Content:  "Valid fact",
		})

		count, err := store2.CountFactsOnOrphanedTopics(0)
		assert.NoError(t, err)
		assert.Zero(t, count)
	})
}

func TestRecalculateTopicRanges(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)

	t.Run("recalculates topic message ranges", func(t *testing.T) {
		// Create topic with incorrect range
		topicID, _ := store.AddTopic(Topic{
			UserID:     userID,
			Summary:    "Test topic",
			StartMsgID: 100, // Wrong
			EndMsgID:   200, // Wrong
		})

		// Add messages with IDs 1-5 to this topic
		for i := int64(1); i <= 5; i++ {
			_ = store.AddMessageToHistory(userID, Message{
				Role:    "user",
				Content: "msg",
				TopicID: &topicID,
			})
		}

		// Recalculate ranges for all users
		affected, err := store.RecalculateTopicRanges(0)
		assert.NoError(t, err)
		assert.Equal(t, 1, affected)

		// Verify topic was updated
		topics, _ := store.GetTopics(userID)
		assert.Len(t, topics, 1)
		assert.Equal(t, int64(1), topics[0].StartMsgID) // Should be 1
		assert.Equal(t, int64(5), topics[0].EndMsgID)   // Should be 5
	})

	t.Run("recalculates for specific user", func(t *testing.T) {
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		otherUserID := int64(456)

		// Create topics for both users
		topic1, _ := store2.AddTopic(Topic{
			UserID:     userID,
			Summary:    "User1 topic",
			StartMsgID: 999,
			EndMsgID:   999,
		})
		_, _ = store2.AddTopic(Topic{
			UserID:     otherUserID,
			Summary:    "User2 topic",
			StartMsgID: 888,
			EndMsgID:   888,
		})

		// Add message only to user1's topic
		_ = store2.AddMessageToHistory(userID, Message{
			Role:    "user",
			Content: "msg",
			TopicID: &topic1,
		})

		// Recalculate only for user1
		affected, err := store2.RecalculateTopicRanges(userID)
		assert.NoError(t, err)
		assert.Equal(t, 1, affected) // Only user1's topic updated

		// Verify user1's topic was updated
		t1, _ := store2.GetTopics(userID)
		assert.Equal(t, int64(1), t1[0].StartMsgID)

		// Verify user2's topic was NOT updated
		t2, _ := store2.GetTopics(otherUserID)
		assert.Equal(t, int64(888), t2[0].StartMsgID)
	})
}

func TestRecalculateTopicSizes(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	_ = store.Init()

	userID := int64(123)

	t.Run("recalculates topic sizes", func(t *testing.T) {
		// Create topic
		topicID, _ := store.AddTopic(Topic{
			UserID:     userID,
			Summary:    "Test topic",
			StartMsgID: 1,
			EndMsgID:   10,
		})

		// Add messages with specific content lengths
		msg1 := "Hello"        // 5 chars
		msg2 := "World!"       // 6 chars
		msg3 := "Test message" // 12 chars
		_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: msg1, TopicID: &topicID})
		_ = store.AddMessageToHistory(userID, Message{Role: "assistant", Content: msg2, TopicID: &topicID})
		_ = store.AddMessageToHistory(userID, Message{Role: "user", Content: msg3, TopicID: &topicID})

		// Recalculate sizes for all users
		affected, err := store.RecalculateTopicSizes(0)
		assert.NoError(t, err)
		assert.Equal(t, 1, affected)

		// Verify size was calculated correctly (5 + 6 + 12 = 23)
		topics, _ := store.GetTopics(userID)
		assert.Len(t, topics, 1)
		assert.Equal(t, 23, topics[0].SizeChars)
	})

	t.Run("handles topic with no messages", func(t *testing.T) {
		store2, cleanup2 := setupTestDB(t)
		defer cleanup2()
		_ = store2.Init()

		// Create topic without messages
		_, _ = store2.AddTopic(Topic{
			UserID:     userID,
			Summary:    "Empty topic",
			StartMsgID: 1,
			EndMsgID:   10,
		})

		// Recalculate sizes
		_, err := store2.RecalculateTopicSizes(0)
		assert.NoError(t, err)

		// Verify size is 0
		topics, _ := store2.GetTopics(userID)
		assert.Len(t, topics, 1)
		assert.Zero(t, topics[0].SizeChars)
	})

	t.Run("recalculates for specific user", func(t *testing.T) {
		store3, cleanup3 := setupTestDB(t)
		defer cleanup3()
		_ = store3.Init()

		otherUserID := int64(456)

		// Create topics for both users
		topic1, _ := store3.AddTopic(Topic{
			UserID:     userID,
			Summary:    "User1 topic",
			StartMsgID: 1,
			EndMsgID:   10,
		})
		_, _ = store3.AddTopic(Topic{
			UserID:     otherUserID,
			Summary:    "User2 topic",
			StartMsgID: 11,
			EndMsgID:   20,
		})

		// Add messages only to user1's topic
		_ = store3.AddMessageToHistory(userID, Message{Role: "user", Content: "test message content", TopicID: &topic1})

		// Recalculate only for user1
		affected, err := store3.RecalculateTopicSizes(userID)
		assert.NoError(t, err)
		assert.Equal(t, 1, affected) // Only user1's topic updated

		// Verify user1's topic was updated
		t1, _ := store3.GetTopics(userID)
		assert.Equal(t, 20, t1[0].SizeChars) // "test message content" = 20 chars

		// Verify user2's topic was NOT updated (still 0)
		t2, _ := store3.GetTopics(otherUserID)
		assert.Zero(t, t2[0].SizeChars)
	})
}

package main

import (
	"context"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessSessionEmpty(t *testing.T) {
	tb, cleanup := setupTestBotWithData(t)
	defer cleanup()

	// Don't add any unprocessed messages

	// Create command with testbot in context
	cmd := processSessionCmd
	ctx := context.WithValue(context.Background(), testbotKey, tb)
	opts := &testbotOptions{userID: testutil.TestUserID}
	ctx = context.WithValue(ctx, optionsKey, opts)
	cmd.SetContext(ctx)

	// Execute command - should not error, just print "No unprocessed messages"
	err := cmd.RunE(cmd, []string{})
	assert.NoError(t, err)
}

func TestProcessSessionWithUnprocessedMessages(t *testing.T) {
	tb, cleanup := setupTestBotWithData(t)
	defer cleanup()

	// Add unprocessed messages
	msg := storage.Message{
		ID:        1,
		UserID:    testutil.TestUserID,
		Role:      "user",
		Content:   "Test message",
		CreatedAt: time.Now(),
		TopicID:   nil, // Unprocessed
	}
	err := tb.store.AddMessageToHistory(testutil.TestUserID, msg)
	require.NoError(t, err)

	// Create command with testbot in context
	cmd := processSessionCmd
	ctx := context.WithValue(context.Background(), testbotKey, tb)
	opts := &testbotOptions{userID: testutil.TestUserID}
	ctx = context.WithValue(ctx, optionsKey, opts)
	cmd.SetContext(ctx)

	// Execute command - will panic because bot is nil
	// Recover and verify panic happens
	assert.Panics(t, func() {
		_ = cmd.RunE(cmd, []string{})
	})
}

func TestProcessSessionErrors(t *testing.T) {
	t.Run("testbot not initialized", func(t *testing.T) {
		// Create command without testbot in context
		cmd := processSessionCmd
		ctx := context.Background()
		cmd.SetContext(ctx)

		err := cmd.RunE(cmd, []string{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "testbot not initialized")
	})

	t.Run("storage error getting unprocessed messages", func(t *testing.T) {
		tb, cleanup := setupTestBotWithData(t)
		defer cleanup()

		// Close store to simulate error
		_ = tb.store.Close()

		cmd := processSessionCmd
		ctx := context.WithValue(context.Background(), testbotKey, tb)
		opts := &testbotOptions{userID: testutil.TestUserID}
		ctx = context.WithValue(ctx, optionsKey, opts)
		cmd.SetContext(ctx)

		err := cmd.RunE(cmd, []string{})
		assert.Error(t, err)
	})
}

package main

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClearFacts(t *testing.T) {
	tb, cleanup := setupTestBotWithData(t)
	defer cleanup()

	// Add test facts
	for _, fact := range testutil.TestFacts() {
		_, err := tb.store.AddFact(fact)
		require.NoError(t, err)
	}

	// Verify facts exist
	facts, err := tb.store.GetFacts(testutil.TestUserID)
	require.NoError(t, err)
	assert.Equal(t, 3, len(facts))

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Create command with testbot in context
	cmd := clearFactsCmd
	ctx := context.WithValue(context.Background(), testbotKey, tb)
	opts := &testbotOptions{userID: testutil.TestUserID}
	ctx = context.WithValue(ctx, optionsKey, opts)
	cmd.SetContext(ctx)

	// Execute command
	runErr := cmd.RunE(cmd, []string{})

	// Restore stdout and read output
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)

	require.NoError(t, runErr)
	output := buf.String()

	// Verify output
	assert.Contains(t, output, "Cleared 3 facts for user 123")

	// Verify facts are cleared
	facts, err = tb.store.GetFacts(testutil.TestUserID)
	require.NoError(t, err)
	assert.Equal(t, 0, len(facts))
}

func TestClearTopics(t *testing.T) {
	tb, cleanup := setupTestBotWithData(t)
	defer cleanup()

	// Add test topics
	for _, topic := range testutil.TestTopics() {
		_, err := tb.store.AddTopic(topic)
		require.NoError(t, err)
	}

	// Verify topics exist
	topics, err := tb.store.GetTopics(testutil.TestUserID)
	require.NoError(t, err)
	assert.Equal(t, 3, len(topics))

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Create command with testbot in context
	cmd := clearTopicsCmd
	ctx := context.WithValue(context.Background(), testbotKey, tb)
	opts := &testbotOptions{userID: testutil.TestUserID}
	ctx = context.WithValue(ctx, optionsKey, opts)
	cmd.SetContext(ctx)

	// Execute command
	runErr := cmd.RunE(cmd, []string{})

	// Restore stdout and read output
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)

	require.NoError(t, runErr)
	output := buf.String()

	// Verify output
	assert.Contains(t, output, "Cleared 3 topics for user 123")

	// Verify topics are cleared
	topics, err = tb.store.GetTopics(testutil.TestUserID)
	require.NoError(t, err)
	assert.Equal(t, 0, len(topics))
}

func TestClearPeople(t *testing.T) {
	tb, cleanup := setupTestBotWithData(t)
	defer cleanup()

	// Add test people
	for _, person := range testutil.TestPeople() {
		_, err := tb.store.AddPerson(person)
		require.NoError(t, err)
	}

	// Verify people exist
	people, err := tb.store.GetPeople(testutil.TestUserID)
	require.NoError(t, err)
	assert.Equal(t, 3, len(people))

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Create command with testbot in context
	cmd := clearPeopleCmd
	ctx := context.WithValue(context.Background(), testbotKey, tb)
	opts := &testbotOptions{userID: testutil.TestUserID}
	ctx = context.WithValue(ctx, optionsKey, opts)
	cmd.SetContext(ctx)

	// Execute command
	runErr := cmd.RunE(cmd, []string{})

	// Restore stdout and read output
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)

	require.NoError(t, runErr)
	output := buf.String()

	// Verify output
	assert.Contains(t, output, "Cleared 3 people for user 123")

	// Verify people are cleared
	people, err = tb.store.GetPeople(testutil.TestUserID)
	require.NoError(t, err)
	assert.Equal(t, 0, len(people))
}

func TestClearEmptyData(t *testing.T) {
	tests := []struct {
		name        string
		cmd         *cobra.Command
		addTestData func(*storage.SQLiteStore) error
		expectedMsg string
	}{
		{
			name:        "clear-facts with empty database",
			cmd:         clearFactsCmd,
			addTestData: func(s *storage.SQLiteStore) error { return nil },
			expectedMsg: "Cleared 0 facts for user 123",
		},
		{
			name: "clear-topics with empty database",
			cmd:  clearTopicsCmd,
			addTestData: func(s *storage.SQLiteStore) error {
				return nil
			},
			expectedMsg: "Cleared 0 topics for user 123",
		},
		{
			name: "clear-people with empty database",
			cmd:  clearPeopleCmd,
			addTestData: func(s *storage.SQLiteStore) error {
				return nil
			},
			expectedMsg: "Cleared 0 people for user 123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tb, cleanup := setupTestBotWithData(t)
			defer cleanup()

			// Add test data (or not)
			err := tt.addTestData(tb.store)
			require.NoError(t, err)

			// Capture stdout
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			// Create command with testbot in context
			ctx := context.WithValue(context.Background(), testbotKey, tb)
			opts := &testbotOptions{userID: testutil.TestUserID}
			ctx = context.WithValue(ctx, optionsKey, opts)
			tt.cmd.SetContext(ctx)

			// Execute command
			runErr := tt.cmd.RunE(tt.cmd, []string{})

			// Restore stdout and read output
			w.Close()
			os.Stdout = old
			var buf bytes.Buffer
			_, err = buf.ReadFrom(r)
			require.NoError(t, err)

			require.NoError(t, runErr)
			assert.Contains(t, buf.String(), tt.expectedMsg)
		})
	}
}

func TestClearErrors(t *testing.T) {
	t.Run("testbot not initialized", func(t *testing.T) {
		// Create command without testbot in context
		cmd := clearFactsCmd
		ctx := context.Background()
		cmd.SetContext(ctx)

		err := cmd.RunE(cmd, []string{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "testbot not initialized")
	})

	t.Run("storage error on get", func(t *testing.T) {
		tb, cleanup := setupTestBotWithData(t)
		defer cleanup()

		// Close store to simulate error
		_ = tb.store.Close()

		cmd := clearFactsCmd
		ctx := context.WithValue(context.Background(), testbotKey, tb)
		opts := &testbotOptions{userID: testutil.TestUserID}
		ctx = context.WithValue(ctx, optionsKey, opts)
		cmd.SetContext(ctx)

		err := cmd.RunE(cmd, []string{})
		assert.Error(t, err)
	})
}

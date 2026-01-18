package main

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestDB creates an in-memory SQLite database with test data
func setupTestDB(t *testing.T) (*storage.SQLiteStore, func()) {
	store, err := storage.NewSQLiteStore(testutil.TestLogger(), ":memory:")
	require.NoError(t, err)
	require.NoError(t, store.Init())

	cleanup := func() {
		_ = store.Close()
	}

	return store, cleanup
}

// setupTestBotWithData creates a testBot with populated test data
func setupTestBotWithData(t *testing.T) (*testBot, func()) {
	store, cleanupStore := setupTestDB(t)

	// Create testbot with minimal setup
	tb := &testBot{
		logger: testutil.TestLogger(),
		store:  store,
		cfg:    testutil.TestConfig(),
	}

	cleanup := func() {
		cleanupStore()
	}

	return tb, cleanup
}

func TestCheckFacts(t *testing.T) {
	tb, cleanup := setupTestBotWithData(t)
	defer cleanup()

	// Add test facts
	for _, fact := range testutil.TestFacts() {
		_, err := tb.store.AddFact(fact)
		require.NoError(t, err)
	}

	tests := []struct {
		name         string
		format       string
		facts        []storage.Fact
		expectedType string
		expectCount  int
	}{
		{
			name:         "text with facts",
			format:       "text",
			facts:        testutil.TestFacts(),
			expectedType: "",
			expectCount:  3,
		},
		{
			name:         "text empty",
			format:       "text",
			facts:        []storage.Fact{},
			expectedType: "",
			expectCount:  0,
		},
		{
			name:         "json with facts",
			format:       "json",
			facts:        testutil.TestFacts(),
			expectedType: "facts",
			expectCount:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear and add facts for this test
			_ = tb.store.DeleteAllFacts(testutil.TestUserID)
			for _, fact := range tt.facts {
				_, _ = tb.store.AddFact(fact)
			}

			// Capture stdout
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			// Create mock command with testbot in context
			cmd := checkFactsCmd
			ctx := context.WithValue(context.Background(), testbotKey, tb)
			opts := &testbotOptions{userID: testutil.TestUserID}
			ctx = context.WithValue(ctx, optionsKey, opts)
			cmd.SetContext(ctx)

			// Set format flag
			err := cmd.Flags().Set("format", tt.format)
			require.NoError(t, err)

			// Execute command
			runErr := cmd.RunE(cmd, []string{})

			// Restore stdout
			w.Close()
			os.Stdout = old
			var buf bytes.Buffer
			_, err = buf.ReadFrom(r)
			require.NoError(t, err)
			output := buf.String()

			require.NoError(t, runErr)

			if tt.format == "json" {
				assert.Contains(t, output, `"type": "facts"`)
				assert.Contains(t, output, `"count": 3`)
			} else {
				assert.Contains(t, output, "Facts for user 123:")
				if tt.expectCount > 0 {
					assert.Contains(t, output, "[identity]")
				}
			}
		})
	}
}

func TestCheckTopics(t *testing.T) {
	tb, cleanup := setupTestBotWithData(t)
	defer cleanup()

	// Add test topics
	for _, topic := range testutil.TestTopics() {
		_, err := tb.store.AddTopic(topic)
		require.NoError(t, err)
	}

	tests := []struct {
		name    string
		format  string
		topics  []storage.Topic
		checkFn func(string)
	}{
		{
			name:   "text with topics",
			format: "text",
			topics: testutil.TestTopics(),
			checkFn: func(output string) {
				assert.Contains(t, output, "Topics for user 123:")
				assert.Contains(t, output, "Discussion about Go programming")
			},
		},
		{
			name:   "json with topics",
			format: "json",
			topics: testutil.TestTopics(),
			checkFn: func(output string) {
				assert.Contains(t, output, `"type": "topics"`)
				assert.Contains(t, output, `"count": 3`)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			// Create mock command with testbot in context
			cmd := checkTopicsCmd
			ctx := context.WithValue(context.Background(), testbotKey, tb)
			opts := &testbotOptions{userID: testutil.TestUserID}
			ctx = context.WithValue(ctx, optionsKey, opts)
			cmd.SetContext(ctx)

			// Set format flag
			err := cmd.Flags().Set("format", tt.format)
			require.NoError(t, err)

			// Execute command
			runErr := cmd.RunE(cmd, []string{})

			// Restore stdout and read output
			w.Close()
			os.Stdout = old
			var buf bytes.Buffer
			_, err = buf.ReadFrom(r)
			require.NoError(t, err)

			require.NoError(t, runErr)
			tt.checkFn(buf.String())
		})
	}
}

func TestCheckPeople(t *testing.T) {
	tb, cleanup := setupTestBotWithData(t)
	defer cleanup()

	// Add test people
	for _, person := range testutil.TestPeople() {
		_, err := tb.store.AddPerson(person)
		require.NoError(t, err)
	}

	tests := []struct {
		name    string
		format  string
		checkFn func(string)
	}{
		{
			name:   "text with people",
			format: "text",
			checkFn: func(output string) {
				assert.Contains(t, output, "People for user 123:")
				assert.Contains(t, output, "Alice Smith")
				assert.Contains(t, output, "Bob Johnson")
			},
		},
		{
			name:   "json with people",
			format: "json",
			checkFn: func(output string) {
				assert.Contains(t, output, `"type": "people"`)
				assert.Contains(t, output, `"count": 3`)
				assert.Contains(t, output, "Alice Smith")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			// Create mock command with testbot in context
			cmd := checkPeopleCmd
			ctx := context.WithValue(context.Background(), testbotKey, tb)
			opts := &testbotOptions{userID: testutil.TestUserID}
			ctx = context.WithValue(ctx, optionsKey, opts)
			cmd.SetContext(ctx)

			// Set format flag
			err := cmd.Flags().Set("format", tt.format)
			require.NoError(t, err)

			// Execute command
			runErr := cmd.RunE(cmd, []string{})

			// Restore stdout and read output
			w.Close()
			os.Stdout = old
			var buf bytes.Buffer
			_, err = buf.ReadFrom(r)
			require.NoError(t, err)

			require.NoError(t, runErr)
			tt.checkFn(buf.String())
		})
	}
}

func TestCheckMessages(t *testing.T) {
	tb, cleanup := setupTestBotWithData(t)
	defer cleanup()

	// Add test messages
	for _, msg := range testutil.TestMessages() {
		err := tb.store.AddMessageToHistory(testutil.TestUserID, msg)
		require.NoError(t, err)
	}

	tests := []struct {
		name    string
		format  string
		checkFn func(string)
	}{
		{
			name:   "text with messages",
			format: "text",
			checkFn: func(output string) {
				assert.Contains(t, output, "Messages for user 123:")
				assert.Contains(t, output, "Hello, how are you?")
			},
		},
		{
			name:   "json with messages",
			format: "json",
			checkFn: func(output string) {
				assert.Contains(t, output, `"type": "messages"`)
				assert.Contains(t, output, `"count": 4`)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			old := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			// Create mock command with testbot in context
			cmd := checkMessagesCmd
			ctx := context.WithValue(context.Background(), testbotKey, tb)
			opts := &testbotOptions{userID: testutil.TestUserID}
			ctx = context.WithValue(ctx, optionsKey, opts)
			cmd.SetContext(ctx)

			// Set format flag
			err := cmd.Flags().Set("format", tt.format)
			require.NoError(t, err)

			// Execute command
			runErr := cmd.RunE(cmd, []string{})

			// Restore stdout and read output
			w.Close()
			os.Stdout = old
			var buf bytes.Buffer
			_, err = buf.ReadFrom(r)
			require.NoError(t, err)

			require.NoError(t, runErr)
			tt.checkFn(buf.String())
		})
	}
}

func TestCheckErrors(t *testing.T) {
	t.Run("testbot not initialized", func(t *testing.T) {
		// Create command without testbot in context
		cmd := checkFactsCmd
		ctx := context.Background()
		cmd.SetContext(ctx)

		err := cmd.Flags().Set("format", "text")
		require.NoError(t, err)

		err = cmd.RunE(cmd, []string{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "testbot not initialized")
	})

	t.Run("storage error", func(t *testing.T) {
		tb, cleanup := setupTestBotWithData(t)
		defer cleanup()

		// Close store to simulate error
		_ = tb.store.Close()

		// Capture stdout
		old := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		cmd := checkFactsCmd
		ctx := context.WithValue(context.Background(), testbotKey, tb)
		opts := &testbotOptions{userID: testutil.TestUserID}
		ctx = context.WithValue(ctx, optionsKey, opts)
		cmd.SetContext(ctx)

		err := cmd.Flags().Set("format", "text")
		require.NoError(t, err)

		runErr := cmd.RunE(cmd, []string{})

		w.Close()
		os.Stdout = old
		var buf bytes.Buffer
		_, err = buf.ReadFrom(r)
		require.NoError(t, err)

		assert.Error(t, runErr)
	})
}

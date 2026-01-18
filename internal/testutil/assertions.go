package testutil

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/stretchr/testify/assert"
)

// AssertFactExists asserts that a fact containing the substring exists for the user.
// Returns the matching fact for further inspection.
func AssertFactExists(t *testing.T, store *storage.SQLiteStore, userID int64, substr string) storage.Fact {
	t.Helper()
	facts, err := store.GetFacts(userID)
	assert.NoError(t, err, "failed to get facts")

	for _, fact := range facts {
		if strings.Contains(strings.ToLower(fact.Content), strings.ToLower(substr)) {
			return fact
		}
	}
	t.Fatalf("no fact found containing %q for user %d", substr, userID)
	return storage.Fact{}
}

// AssertFactCount asserts the number of facts for a user.
func AssertFactCount(t *testing.T, store *storage.SQLiteStore, userID int64, expected int) {
	t.Helper()
	facts, err := store.GetFacts(userID)
	assert.NoError(t, err, "failed to get facts")
	if len(facts) != expected {
		t.Errorf("expected %d facts, got %d", expected, len(facts))
		// Print all facts for debugging
		for i, fact := range facts {
			t.Logf("Fact %d: %s (%s)", i+1, fact.Content, fact.Type)
		}
	}
}

// AssertFactContains asserts that a fact containing the given content exists.
func AssertFactContains(t *testing.T, store *storage.SQLiteStore, userID int64, content string) {
	t.Helper()
	AssertFactExists(t, store, userID, content)
}

// AssertTopicCount asserts the number of topics for a user.
func AssertTopicCount(t *testing.T, store *storage.SQLiteStore, userID int64, expected int) {
	t.Helper()
	topics, err := store.GetTopics(userID)
	assert.NoError(t, err, "failed to get topics")
	if len(topics) != expected {
		t.Errorf("expected %d topics, got %d", expected, len(topics))
		// Print all topics for debugging
		for i, topic := range topics {
			t.Logf("Topic %d: %s (msg %d-%d)", i+1, topic.Summary, topic.StartMsgID, topic.EndMsgID)
		}
	}
}

// AssertTopicExists asserts that a topic containing the substring exists for the user.
// Returns the matching topic for further inspection.
func AssertTopicExists(t *testing.T, store *storage.SQLiteStore, userID int64, substr string) storage.Topic {
	t.Helper()
	topics, err := store.GetTopics(userID)
	assert.NoError(t, err, "failed to get topics")

	for _, topic := range topics {
		if strings.Contains(strings.ToLower(topic.Summary), strings.ToLower(substr)) {
			return topic
		}
	}
	t.Fatalf("no topic found containing %q for user %d", substr, userID)
	return storage.Topic{}
}

// AssertMessageCount asserts the number of messages for a user.
func AssertMessageCount(t *testing.T, store *storage.SQLiteStore, userID int64, expected int) {
	t.Helper()
	messages, err := store.GetRecentHistory(userID, expected+100) // Get more than expected
	assert.NoError(t, err, "failed to get messages")
	if len(messages) != expected {
		t.Errorf("expected %d messages, got %d", expected, len(messages))
	}
}

// AssertMessageExists asserts that a message containing the substring exists for the user.
func AssertMessageExists(t *testing.T, store *storage.SQLiteStore, userID int64, substr string) storage.Message {
	t.Helper()
	messages, err := store.GetRecentHistory(userID, 1000)
	assert.NoError(t, err, "failed to get messages")

	for _, msg := range messages {
		if strings.Contains(strings.ToLower(msg.Content), strings.ToLower(substr)) {
			return msg
		}
	}
	t.Fatalf("no message found containing %q for user %d", substr, userID)
	return storage.Message{}
}

// AssertPersonExists asserts that a person with the given name exists for the user.
// Returns the matching person for further inspection.
func AssertPersonExists(t *testing.T, store *storage.SQLiteStore, userID int64, name string) storage.Person {
	t.Helper()
	people, err := store.GetPeople(userID)
	assert.NoError(t, err, "failed to get people")

	for _, person := range people {
		if strings.Contains(strings.ToLower(person.DisplayName), strings.ToLower(name)) {
			return person
		}
	}
	t.Fatalf("no person found containing %q for user %d", name, userID)
	return storage.Person{}
}

// AssertPersonCount asserts the number of people for a user.
func AssertPersonCount(t *testing.T, store *storage.SQLiteStore, userID int64, expected int) {
	t.Helper()
	people, err := store.GetPeople(userID)
	assert.NoError(t, err, "failed to get people")
	if len(people) != expected {
		t.Errorf("expected %d people, got %d", expected, len(people))
		// Print all people for debugging
		for i, person := range people {
			username := "none"
			if person.Username != nil {
				username = *person.Username
			}
			t.Logf("Person %d: %s (@%s)", i+1, person.DisplayName, username)
		}
	}
}

// AssertPersonHasAlias asserts that a person has a specific alias.
func AssertPersonHasAlias(t *testing.T, store *storage.SQLiteStore, userID int64, personName string, alias string) {
	t.Helper()
	person := AssertPersonExists(t, store, userID, personName)
	for _, a := range person.Aliases {
		if strings.EqualFold(a, alias) {
			return
		}
	}
	t.Errorf("person %q does not have alias %q. Aliases: %v", personName, alias, person.Aliases)
}

// AssertLogContains asserts that the log contains an entry with the given level and message.
func AssertLogContains(t *testing.T, logs []LogEntry, level string, msg string) {
	t.Helper()
	for _, entry := range logs {
		if (level == "" || strings.EqualFold(entry.Level, level)) &&
			strings.Contains(entry.Message, msg) {
			return
		}
	}
	t.Fatalf("no log entry found with level=%q msg containing %q. Entries: %d", level, msg, len(logs))
}

// AssertLogHasField asserts that a log entry exists with the given field value.
func AssertLogHasField(t *testing.T, logs []LogEntry, key string, value interface{}) {
	t.Helper()
	for _, entry := range logs {
		if v, ok := entry.Fields[key]; ok && fmt.Sprint(v) == fmt.Sprint(value) {
			return
		}
	}
	t.Fatalf("no log entry found with field %q=%v. Entries: %d", key, value, len(logs))
}

// AssertNoErrorLogs asserts that no ERROR level logs were captured.
func AssertNoErrorLogs(t *testing.T, logs []LogEntry) {
	t.Helper()
	for _, entry := range logs {
		if strings.EqualFold(entry.Level, "error") {
			t.Errorf("found error log: %s (fields: %v)", entry.Message, entry.Fields)
		}
	}
}

// ProcessSessionForTest processes the active session for testing purposes.
// This forces topic creation without waiting for timeout, but does NOT extract facts.
// For fact extraction, use the memory service or testbot with --process-session flag.
func ProcessSessionForTest(t *testing.T, ctx context.Context, userID int64, store *storage.SQLiteStore) int64 {
	t.Helper()
	messages, err := store.GetUnprocessedMessages(userID)
	assert.NoError(t, err, "failed to get unprocessed messages")

	if len(messages) == 0 {
		t.Skip("no unprocessed messages to create session")
		return 0
	}

	startMsgID := messages[0].ID
	endMsgID := messages[len(messages)-1].ID

	// Create a topic
	topic := storage.Topic{
		UserID:     userID,
		Summary:    "Test topic",
		StartMsgID: startMsgID,
		EndMsgID:   endMsgID,
	}
	topicID, err := store.AddTopic(topic)
	assert.NoError(t, err, "failed to add topic")

	// Update messages to link to topic
	err = store.UpdateMessagesTopicInRange(ctx, userID, startMsgID, endMsgID, topicID)
	assert.NoError(t, err, "failed to update message topics")

	return topicID
}

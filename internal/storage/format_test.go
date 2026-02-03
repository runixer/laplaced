package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatUserProfile(t *testing.T) {
	now := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		facts    []Fact
		expected string
	}{
		{
			name:  "empty facts",
			facts: []Fact{},
			expected: "<user_profile>\n" +
				"</user_profile>",
		},
		{
			name: "single fact",
			facts: []Fact{
				{ID: 1, Category: "personal", Type: "identity", Content: "Name is Alice", LastUpdated: now},
			},
			expected: "<user_profile>\n" +
				"- [Fact:1] [personal/identity] (Updated: 2025-01-15) Name is Alice\n" +
				"</user_profile>",
		},
		{
			name: "multiple facts",
			facts: []Fact{
				{ID: 1, Category: "personal", Type: "identity", Content: "Name is Alice", LastUpdated: now},
				{ID: 2, Category: "work", Type: "context", Content: "Works at TechCorp", LastUpdated: now.Add(-24 * time.Hour)},
				{ID: 3, Category: "hobbies", Type: "status", Content: "Loves photography", LastUpdated: now.Add(-48 * time.Hour)},
			},
			expected: "<user_profile>\n" +
				"- [Fact:1] [personal/identity] (Updated: 2025-01-15) Name is Alice\n" +
				"- [Fact:2] [work/context] (Updated: 2025-01-14) Works at TechCorp\n" +
				"- [Fact:3] [hobbies/status] (Updated: 2025-01-13) Loves photography\n" +
				"</user_profile>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatUserProfile(tt.facts)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatUserProfileCompact(t *testing.T) {
	now := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		facts    []Fact
		expected string
	}{
		{
			name:     "empty facts",
			facts:    []Fact{},
			expected: "<user_profile>\n</user_profile>",
		},
		{
			name: "single fact",
			facts: []Fact{
				{ID: 1, Category: "personal", Type: "identity", Content: "Name is Alice", LastUpdated: now},
			},
			expected: "<user_profile>\n" +
				"- [personal/identity] (2025-01-15) Name is Alice\n" +
				"</user_profile>",
		},
		{
			name: "multiple facts",
			facts: []Fact{
				{ID: 1, Category: "personal", Type: "identity", Content: "Name is Alice", LastUpdated: now},
				{ID: 2, Category: "work", Type: "context", Content: "Works at TechCorp", LastUpdated: now.Add(-24 * time.Hour)},
			},
			expected: "<user_profile>\n" +
				"- [personal/identity] (2025-01-15) Name is Alice\n" +
				"- [work/context] (2025-01-14) Works at TechCorp\n" +
				"</user_profile>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatUserProfileCompact(tt.facts)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatRecentTopics(t *testing.T) {
	now := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		topics   []TopicExtended
		expected string
	}{
		{
			name:     "empty topics",
			topics:   []TopicExtended{},
			expected: "<recent_topics>\n</recent_topics>",
		},
		{
			name: "single topic",
			topics: []TopicExtended{
				{
					Topic: Topic{
						CreatedAt: now,
						Summary:   "Discussion about AI",
						SizeChars: 5000,
					},
					MessageCount: 10,
				},
			},
			expected: "<recent_topics>\n" +
				"- 2025-01-15: \"Discussion about AI\" (10 msg, ~5k chars)\n" +
				"</recent_topics>",
		},
		{
			name: "multiple topics",
			topics: []TopicExtended{
				{
					Topic: Topic{
						CreatedAt: now,
						Summary:   "Discussion about AI",
						SizeChars: 5000,
					},
					MessageCount: 10,
				},
				{
					Topic: Topic{
						CreatedAt: now.Add(-24 * time.Hour),
						Summary:   "Chat about photography",
						SizeChars: 15000,
					},
					MessageCount: 25,
				},
			},
			expected: "<recent_topics>\n" +
				"- 2025-01-15: \"Discussion about AI\" (10 msg, ~5k chars)\n" +
				"- 2025-01-14: \"Chat about photography\" (25 msg, ~15k chars)\n" +
				"</recent_topics>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatRecentTopics(tt.topics)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFilterProfileFacts(t *testing.T) {
	tests := []struct {
		name     string
		facts    []Fact
		expected int
	}{
		{
			name:     "empty facts",
			facts:    []Fact{},
			expected: 0,
		},
		{
			name: "only identity facts",
			facts: []Fact{
				{Type: "identity", Importance: 50},
				{Type: "identity", Importance: 70},
			},
			expected: 2,
		},
		{
			name: "high importance facts (>=80)",
			facts: []Fact{
				{Type: "context", Importance: 80},
				{Type: "context", Importance: 90},
				{Type: "context", Importance: 100},
			},
			expected: 3,
		},
		{
			name: "mixed facts - identity and high importance",
			facts: []Fact{
				{Type: "identity", Importance: 50}, // included - identity
				{Type: "context", Importance: 90},  // included - high importance
				{Type: "status", Importance: 79},   // excluded - below threshold
				{Type: "context", Importance: 80},  // included - exactly 80
			},
			expected: 3,
		},
		{
			name: "all excluded - low importance non-identity",
			facts: []Fact{
				{Type: "context", Importance: 70},
				{Type: "status", Importance: 50},
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterProfileFacts(tt.facts)
			assert.Len(t, result, tt.expected)

			// Verify that all returned facts meet the criteria
			for _, f := range result {
				assert.True(t, f.Type == "identity" || f.Importance >= 80,
					"fact should be identity or have importance >= 80")
			}
		})
	}
}

func TestFormatPeople(t *testing.T) {
	username := "alice"

	tests := []struct {
		name     string
		people   []Person
		tag      string
		expected string
	}{
		{
			name:     "empty people with tag",
			people:   []Person{},
			tag:      TagInnerCircle,
			expected: "<inner_circle>\n</inner_circle>",
		},
		{
			name:     "empty people with empty tag",
			people:   []Person{},
			tag:      "",
			expected: "",
		},
		{
			name:     "empty people with relevant_people tag",
			people:   []Person{},
			tag:      TagRelevantPeople,
			expected: "",
		},
		{
			name: "single person minimal",
			people: []Person{
				{ID: 1, DisplayName: "Alice", Circle: "Work_Inner", Bio: ""},
			},
			tag: TagInnerCircle,
			expected: "<inner_circle>\n" +
				"[Person:1] Alice [Work_Inner]\n" +
				"</inner_circle>",
		},
		{
			name: "person with username",
			people: []Person{
				{ID: 1, DisplayName: "Alice", Username: &username, Circle: "Work_Inner", Bio: ""},
			},
			tag: TagInnerCircle,
			expected: "<inner_circle>\n" +
				"[Person:1] Alice (@alice) [Work_Inner]\n" +
				"</inner_circle>",
		},
		{
			name: "person with aliases",
			people: []Person{
				{ID: 1, DisplayName: "Alice", Aliases: []string{"Гелёй", "@akaGelo"}, Circle: "Family", Bio: ""},
			},
			tag: TagInnerCircle,
			expected: "<inner_circle>\n" +
				"[Person:1] Alice (aka Гелёй, @akaGelo) [Family]\n" +
				"</inner_circle>",
		},
		{
			name: "person with bio",
			people: []Person{
				{ID: 1, DisplayName: "Alice", Circle: "Friends", Bio: "Colleague from TechCorp"},
			},
			tag: TagPeople,
			expected: "<people>\n" +
				"[Person:1] Alice [Friends]: Colleague from TechCorp\n" +
				"</people>",
		},
		{
			name: "person with all fields",
			people: []Person{
				{
					ID:          1,
					DisplayName: "Alice",
					Username:    &username,
					Aliases:     []string{"Гелёй"},
					Circle:      "Work_Inner",
					Bio:         "Colleague from TechCorp",
				},
			},
			tag: TagInnerCircle,
			expected: "<inner_circle>\n" +
				"[Person:1] Alice (@alice) (aka Гелёй) [Work_Inner]: Colleague from TechCorp\n" +
				"</inner_circle>",
		},
		{
			name: "multiple people",
			people: []Person{
				{ID: 1, DisplayName: "Alice", Circle: "Work_Inner", Bio: "Colleague"},
				{ID: 2, DisplayName: "Bob", Circle: "Family", Bio: "Brother"},
			},
			tag: TagInnerCircle,
			expected: "<inner_circle>\n" +
				"[Person:1] Alice [Work_Inner]: Colleague\n" +
				"[Person:2] Bob [Family]: Brother\n" +
				"</inner_circle>",
		},
		{
			name: "plain format without tag",
			people: []Person{
				{ID: 1, DisplayName: "Alice", Circle: "Work_Outer", Bio: "Colleague"},
			},
			tag:      "",
			expected: "[Person:1] Alice [Work_Outer]: Colleague\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatPeople(tt.people, tt.tag)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFilterInnerCircle(t *testing.T) {
	tests := []struct {
		name     string
		people   []Person
		expected int
	}{
		{
			name:     "empty people",
			people:   []Person{},
			expected: 0,
		},
		{
			name: "only Work_Inner",
			people: []Person{
				{Circle: "Work_Inner"},
				{Circle: "Work_Inner"},
			},
			expected: 2,
		},
		{
			name: "only Family",
			people: []Person{
				{Circle: "Family"},
			},
			expected: 1,
		},
		{
			name: "Work_Inner and Family mixed",
			people: []Person{
				{Circle: "Work_Inner"},
				{Circle: "Family"},
				{Circle: "Work_Outer"},
				{Circle: "Friends"},
				{Circle: "Other"},
			},
			expected: 2,
		},
		{
			name: "no inner circle",
			people: []Person{
				{Circle: "Work_Outer"},
				{Circle: "Friends"},
				{Circle: "Other"},
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterInnerCircle(tt.people)
			assert.Len(t, result, tt.expected)

			// Verify that all returned people are Work_Inner or Family
			for _, p := range result {
				assert.True(t, p.Circle == "Work_Inner" || p.Circle == "Family",
					"person should be Work_Inner or Family")
			}
		})
	}
}

func TestTagConstants(t *testing.T) {
	assert.Equal(t, "inner_circle", TagInnerCircle)
	assert.Equal(t, "relevant_people", TagRelevantPeople)
	assert.Equal(t, "people", TagPeople)
}

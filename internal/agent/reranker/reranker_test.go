package reranker

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatCandidatesForReranker(t *testing.T) {

	tests := []struct {
		name         string
		candidates   []Candidate
		wantLines    int
		wantContains []string
	}{
		{
			name:       "empty candidates",
			candidates: []Candidate{},
			wantLines:  0,
		},
		{
			name: "single candidate",
			candidates: []Candidate{
				{
					TopicID: 42,
					Topic: storage.Topic{
						ID:        42,
						Summary:   "Test topic",
						CreatedAt: time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC),
					},
					MessageCount: 5,
					SizeChars:    1500,
				},
			},
			wantLines:    1,
			wantContains: []string{"[Topic:42]", "2026-01-10", "5 msgs", "~1K chars", "Test topic"},
		},
		{
			name: "large topic with K chars",
			candidates: []Candidate{
				{
					TopicID: 100,
					Topic: storage.Topic{
						ID:        100,
						Summary:   "Large discussion",
						CreatedAt: time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC),
					},
					MessageCount: 50,
					SizeChars:    35000,
				},
			},
			wantLines:    1,
			wantContains: []string{"[Topic:100]", "~35K chars", "50 msgs"},
		},
		{
			name: "small topic with raw chars",
			candidates: []Candidate{
				{
					TopicID: 1,
					Topic: storage.Topic{
						ID:        1,
						Summary:   "Small chat",
						CreatedAt: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC),
					},
					MessageCount: 2,
					SizeChars:    500,
				},
			},
			wantLines:    1,
			wantContains: []string{"~500 chars"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatCandidatesForReranker(tt.candidates)

			if tt.wantLines == 0 {
				assert.Empty(t, result)
				return
			}

			for _, want := range tt.wantContains {
				assert.Contains(t, result, want)
			}
		})
	}
}

func TestParseResponse(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	tests := []struct {
		name           string
		content        string
		wantTopics     []int64
		wantSelections []TopicSelection
		wantErr        bool
	}{
		// Old format (backward compatibility)
		{
			name:       "valid response with topics (old format)",
			content:    `{"topic_ids": [42, 18]}`,
			wantTopics: []int64{42, 18},
			wantErr:    false,
		},
		{
			name:       "ignores extra fields like people",
			content:    `{"topic_ids": [1, 2, 3], "people": ["John", "Jane"]}`,
			wantTopics: []int64{1, 2, 3},
			wantErr:    false,
		},
		{
			name:       "empty topics array",
			content:    `{"topic_ids": []}`,
			wantTopics: []int64{},
			wantErr:    false,
		},
		{
			name:       "fallback to ids field when topics empty",
			content:    `{"ids": [100, 200]}`,
			wantTopics: []int64{100, 200},
			wantErr:    false,
		},
		{
			name:       "topics takes priority over ids",
			content:    `{"topic_ids": [1, 2], "ids": [100, 200]}`,
			wantTopics: []int64{1, 2},
			wantErr:    false,
		},
		{
			name:    "invalid JSON",
			content: `{not valid json`,
			wantErr: true,
		},
		// New format with objects
		{
			name:       "new format with objects",
			content:    `{"topic_ids": [{"id": "Topic:42", "reason": "relevant discussion"}, {"id": "Topic:18", "reason": "mentions person"}]}`,
			wantTopics: []int64{42, 18},
			wantSelections: []TopicSelection{
				{ID: "Topic:42", Reason: "relevant discussion"},
				{ID: "Topic:18", Reason: "mentions person"},
			},
			wantErr: false,
		},
		// Bare array format (Flash sometimes returns this)
		{
			name:       "bare array format",
			content:    `[{"id": "Topic:42", "reason": "relevant"}, {"id": "Topic:18", "reason": "mentions person"}]`,
			wantTopics: []int64{42, 18},
			wantSelections: []TopicSelection{
				{ID: "Topic:42", Reason: "relevant"},
				{ID: "Topic:18", Reason: "mentions person"},
			},
			wantErr: false,
		},
		{
			name:       "new format with empty reason",
			content:    `{"topic_ids": [{"id": "Topic:42"}]}`,
			wantTopics: []int64{42},
			wantSelections: []TopicSelection{
				{ID: "Topic:42", Reason: ""},
			},
			wantErr: false,
		},
		// LLM quirk: wraps entire response in an array
		{
			name:       "wrapped in array (LLM quirk)",
			content:    `[{"topic_ids": [{"id": "Topic:5205", "reason": "discussion about limits"}, {"id": "Topic:4750", "reason": "cost context"}]}]`,
			wantTopics: []int64{5205, 4750},
			wantSelections: []TopicSelection{
				{ID: "Topic:5205", Reason: "discussion about limits"},
				{ID: "Topic:4750", Reason: "cost context"},
			},
			wantErr: false,
		},
		{
			name:       "wrapped in array with simple IDs",
			content:    `[{"topic_ids": [42, 18]}]`,
			wantTopics: []int64{42, 18},
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseResponse(tt.content, logger)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wantTopics, result.TopicIDs())

			// Check detailed selections if specified
			if tt.wantSelections != nil {
				assert.Equal(t, tt.wantSelections, result.Topics)
			}
		})
	}
}

func TestParseToolCallIDs(t *testing.T) {

	tests := []struct {
		name      string
		arguments string
		wantIDs   []int64
		wantErr   bool
	}{
		{
			name:      "valid topic_ids",
			arguments: `{"topic_ids": [1, 2, 3]}`,
			wantIDs:   []int64{1, 2, 3},
			wantErr:   false,
		},
		{
			name:      "empty array",
			arguments: `{"topic_ids": []}`,
			wantIDs:   []int64{},
			wantErr:   false,
		},
		{
			name:      "single ID",
			arguments: `{"topic_ids": [42]}`,
			wantIDs:   []int64{42},
			wantErr:   false,
		},
		{
			name:      "invalid JSON",
			arguments: `{not valid}`,
			wantErr:   true,
		},
		{
			name:      "missing field returns empty",
			arguments: `{"other_field": [1, 2]}`,
			wantIDs:   nil,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, err := parseToolCallIDs(tt.arguments)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wantIDs, ids)
		})
	}
}

func TestFilterValidTopics(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	candidateMap := map[int64]Candidate{
		1: {TopicID: 1},
		2: {TopicID: 2},
		3: {TopicID: 3},
	}

	tests := []struct {
		name       string
		result     *Result
		wantTopics []TopicSelection
	}{
		{
			name: "all valid",
			result: &Result{
				Topics: []TopicSelection{
					{ID: "Topic:1", Reason: "a"},
					{ID: "Topic:2", Reason: "b"},
				},
			},
			wantTopics: []TopicSelection{
				{ID: "Topic:1", Reason: "a"},
				{ID: "Topic:2", Reason: "b"},
			},
		},
		{
			name: "some hallucinated",
			result: &Result{
				Topics: []TopicSelection{
					{ID: "Topic:1", Reason: "valid"},
					{ID: "Topic:999", Reason: "hallucinated"},
					{ID: "Topic:2", Reason: "also valid"},
				},
			},
			wantTopics: []TopicSelection{
				{ID: "Topic:1", Reason: "valid"},
				{ID: "Topic:2", Reason: "also valid"},
			},
		},
		{
			name: "all hallucinated",
			result: &Result{
				Topics: []TopicSelection{
					{ID: "Topic:999", Reason: "fake"},
					{ID: "Topic:888", Reason: "also fake"},
				},
			},
			wantTopics: nil,
		},
		{
			name: "empty input",
			result: &Result{
				Topics: []TopicSelection{},
			},
			wantTopics: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := filterValidTopics(123, tt.result, candidateMap, logger)
			assert.Equal(t, tt.wantTopics, filtered.Topics)
		})
	}
}

func TestExtractJSONFromResponse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "clean array",
			input:    `[{"id": 1}, {"id": 2}]`,
			expected: `[{"id": 1}, {"id": 2}]`,
		},
		{
			name:     "clean object",
			input:    `{"topic_ids": [1, 2]}`,
			expected: `{"topic_ids": [1, 2]}`,
		},
		{
			name:     "text before array",
			input:    `Here are the topics: [{"id": 1}]`,
			expected: `[{"id": 1}]`,
		},
		{
			name:     "text before object",
			input:    `Based on analysis: {"topic_ids": [42]}`,
			expected: `{"topic_ids": [42]}`,
		},
		{
			name:     "text after JSON",
			input:    `{"topic_ids": [1]} Hope this helps!`,
			expected: `{"topic_ids": [1]}`,
		},
		{
			name:     "markdown code block",
			input:    "```json\n{\"topic_ids\": [1, 2]}\n```",
			expected: `{"topic_ids": [1, 2]}`,
		},
		{
			name:     "nested braces",
			input:    `{"data": {"nested": {"deep": 1}}}`,
			expected: `{"data": {"nested": {"deep": 1}}}`,
		},
		{
			name:     "string with braces inside",
			input:    `{"text": "value with {braces} inside"}`,
			expected: `{"text": "value with {braces} inside"}`,
		},
		{
			name:     "escaped quotes in string",
			input:    `{"text": "he said \"hello\""}`,
			expected: `{"text": "he said \"hello\""}`,
		},
		{
			name:     "no JSON",
			input:    "Just plain text without JSON",
			expected: "Just plain text without JSON",
		},
		{
			name:     "array preferred when first",
			input:    `[1, 2] {"a": 1}`,
			expected: `[1, 2]`,
		},
		{
			name:     "object preferred when first",
			input:    `{"a": 1} [1, 2]`,
			expected: `{"a": 1}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractJSONFromResponse(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFallbackToVectorTop(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9},
		{TopicID: 2, Score: 0.8},
		{TopicID: 3, Score: 0.7},
		{TopicID: 4, Score: 0.6},
		{TopicID: 5, Score: 0.5},
	}

	tests := []struct {
		name      string
		maxTopics int
		wantIDs   []int64
	}{
		{
			name:      "limit to 3",
			maxTopics: 3,
			wantIDs:   []int64{1, 2, 3},
		},
		{
			name:      "limit to 5 (all)",
			maxTopics: 5,
			wantIDs:   []int64{1, 2, 3, 4, 5},
		},
		{
			name:      "limit more than available",
			maxTopics: 10,
			wantIDs:   []int64{1, 2, 3, 4, 5},
		},
		{
			name:      "zero defaults to 5",
			maxTopics: 0,
			wantIDs:   []int64{1, 2, 3, 4, 5},
		},
		{
			name:      "negative defaults to 5",
			maxTopics: -1,
			wantIDs:   []int64{1, 2, 3, 4, 5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := fallbackToVectorTop(nil, candidates, nil, nil, tt.maxTopics, logger)
			assert.Equal(t, tt.wantIDs, result.TopicIDs())
		})
	}
}

func TestFallbackFromState(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9},
		{TopicID: 2, Score: 0.8},
		{TopicID: 3, Score: 0.7},
	}

	tests := []struct {
		name         string
		state        *state
		maxTopics    int
		wantIDs      []int64
		wantFromReqs bool // true if should return from requestedIDs
	}{
		{
			name:         "uses requestedIDs when available",
			state:        &state{requestedIDs: []int64{2, 3}},
			maxTopics:    5,
			wantIDs:      []int64{2, 3},
			wantFromReqs: true,
		},
		{
			name:         "limits requestedIDs to maxTopics",
			state:        &state{requestedIDs: []int64{1, 2, 3, 4, 5}},
			maxTopics:    2,
			wantIDs:      []int64{1, 2},
			wantFromReqs: true,
		},
		{
			name:         "falls back to vector when no requestedIDs",
			state:        &state{requestedIDs: nil},
			maxTopics:    2,
			wantIDs:      []int64{1, 2},
			wantFromReqs: false,
		},
		{
			name:         "falls back to vector when requestedIDs empty",
			state:        &state{requestedIDs: []int64{}},
			maxTopics:    2,
			wantIDs:      []int64{1, 2},
			wantFromReqs: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := fallbackFromState(nil, tt.state, candidates, nil, nil, tt.maxTopics, logger)
			assert.Equal(t, tt.wantIDs, result.TopicIDs())
		})
	}
}

func TestResultTopicIDs(t *testing.T) {
	result := &Result{
		Topics: []TopicSelection{
			{ID: "Topic:42", Reason: "a"},
			{ID: "Topic:18", Reason: "b"},
			{ID: "Topic:5", Reason: "c"},
		},
	}

	ids := result.TopicIDs()
	assert.Equal(t, []int64{42, 18, 5}, ids)
}

func TestResultTopicIDsEmpty(t *testing.T) {
	result := &Result{Topics: nil}
	ids := result.TopicIDs()
	assert.Empty(t, ids)

	result2 := &Result{Topics: []TopicSelection{}}
	ids2 := result2.TopicIDs()
	assert.Empty(t, ids2)
}

// MockMessageRepository is a minimal mock for testing LoadTopicsContent.
// LEGITIMATE: Kept inline because it's a simple map-based stub used only for
// testing topic content loading. Using full MockStorage would be overkill.
type MockMessageRepository struct {
	messages map[int64][]storage.Message
}

func (m *MockMessageRepository) GetMessagesByTopicID(ctx context.Context, topicID int64) ([]storage.Message, error) {
	if msgs, ok := m.messages[topicID]; ok {
		return msgs, nil
	}
	return nil, nil
}

func TestLoadTopicsContent(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	mockRepo := &MockMessageRepository{
		messages: map[int64][]storage.Message{
			1: {
				{ID: 1, Role: "user", Content: "Hello", CreatedAt: time.Now()},
				{ID: 2, Role: "assistant", Content: "Hi there!", CreatedAt: time.Now()},
			},
			2: {
				{ID: 3, Role: "user", Content: "Question", CreatedAt: time.Now()},
			},
		},
	}

	candidateMap := map[int64]Candidate{
		1: {
			TopicID: 1,
			Topic:   storage.Topic{ID: 1, Summary: "Greeting", CreatedAt: time.Now()},
		},
		2: {
			TopicID: 2,
			Topic:   storage.Topic{ID: 2, Summary: "Question", CreatedAt: time.Now()},
		},
	}

	tr := &trace{candidates: []storage.RerankerCandidate{{TopicID: 1}, {TopicID: 2}}}

	content := loadTopicsContent(context.Background(), 123, []int64{1, 2}, candidateMap, mockRepo, logger, tr)

	assert.Contains(t, content, "=== Topic 1 ===")
	assert.Contains(t, content, "=== Topic 2 ===")
	assert.Contains(t, content, "Hello")
	assert.Contains(t, content, "Hi there!")
	assert.Contains(t, content, "Question")
}

func TestTruncateForLog(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "short string",
			input:  "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exact length",
			input:  "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "truncated",
			input:  "hello world",
			maxLen: 5,
			want:   "hello...",
		},
		{
			name:   "empty string",
			input:  "",
			maxLen: 10,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateForLog(tt.input, tt.maxLen)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestFormatSizeChars(t *testing.T) {
	tests := []struct {
		chars int
		want  string
	}{
		{500, "~500 chars"},
		{999, "~999 chars"},
		{1000, "~1K chars"},
		{1500, "~1K chars"},
		{25000, "~25K chars"},
		{100000, "~100K chars"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			result := formatSizeChars(tt.chars)
			assert.Equal(t, tt.want, result)
		})
	}
}

// Filtering Tests

// TestFilterValidTopics_InvalidIDFormat verifies handling of malformed topic IDs.
func TestFilterValidTopics_InvalidIDFormat(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	candidateMap := map[int64]Candidate{
		1: {TopicID: 1},
		2: {TopicID: 2},
	}

	result := &Result{
		Topics: []TopicSelection{
			{ID: "Topic:1", Reason: "valid"},
			{ID: "NotATopic:999", Reason: "malformed"},
			{ID: "Topic:2", Reason: "also valid"},
		},
	}

	filtered := filterValidTopics(123, result, candidateMap, logger)
	assert.Equal(t, []int64{1, 2}, filtered.TopicIDs())
	assert.Len(t, filtered.Topics, 2)
}

// TestFilterValidTopics_MixedValidAndInvalid verifies filtering with mix of IDs.
func TestFilterValidTopics_MixedValidAndInvalid(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	candidateMap := map[int64]Candidate{
		1: {TopicID: 1},
		3: {TopicID: 3},
		5: {TopicID: 5},
	}

	result := &Result{
		Topics: []TopicSelection{
			{ID: "Topic:1", Reason: "valid 1"},
			{ID: "Topic:999", Reason: "hallucinated"},
			{ID: "Topic:3", Reason: "valid 3"},
			{ID: "Topic:888", Reason: "hallucinated 2"},
			{ID: "Topic:5", Reason: "valid 5"},
		},
	}

	filtered := filterValidTopics(123, result, candidateMap, logger)
	assert.Equal(t, []int64{1, 3, 5}, filtered.TopicIDs())
	assert.Len(t, filtered.Topics, 3)
}

// People Filtering Tests

// TestFilterValidPeople_AllValid verifies all people are kept when valid.
func TestFilterValidPeople_AllValid(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	peopleMap := map[int64]PersonCandidate{
		1: {PersonID: 1},
		2: {PersonID: 2},
	}

	result := &Result{
		Topics:    []TopicSelection{{ID: "Topic:1", Reason: "t1"}},
		People:    []PersonSelection{{ID: "Person:1", Reason: "p1"}, {ID: "Person:2", Reason: "p2"}},
		Artifacts: []ArtifactSelection{{ID: "Artifact:1", Reason: "a1"}},
	}

	filtered := filterValidPeople(123, result, peopleMap, logger)
	assert.Equal(t, []int64{1, 2}, filtered.PeopleIDs())
	assert.Len(t, filtered.People, 2)
	// Other fields preserved
	assert.Len(t, filtered.Topics, 1)
	assert.Len(t, filtered.Artifacts, 1)
}

// TestFilterValidPeople_SomeHallucinated filters out hallucinated people.
func TestFilterValidPeople_SomeHallucinated(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	peopleMap := map[int64]PersonCandidate{
		1: {PersonID: 1},
		3: {PersonID: 3},
	}

	result := &Result{
		People: []PersonSelection{
			{ID: "Person:1", Reason: "valid"},
			{ID: "Person:999", Reason: "hallucinated"},
			{ID: "Person:3", Reason: "also valid"},
		},
	}

	filtered := filterValidPeople(123, result, peopleMap, logger)
	assert.Equal(t, []int64{1, 3}, filtered.PeopleIDs())
	assert.Len(t, filtered.People, 2)
}

// TestFilterValidPeople_AllHallucinated returns empty when all are invalid.
func TestFilterValidPeople_AllHallucinated(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	peopleMap := map[int64]PersonCandidate{
		1: {PersonID: 1},
	}

	result := &Result{
		People: []PersonSelection{
			{ID: "Person:999", Reason: "fake 1"},
			{ID: "Person:888", Reason: "fake 2"},
		},
	}

	filtered := filterValidPeople(123, result, peopleMap, logger)
	assert.Empty(t, filtered.PeopleIDs())
	assert.Nil(t, filtered.People)
}

// TestFilterValidPeople_EmptyInput handles empty input gracefully.
func TestFilterValidPeople_EmptyInput(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	peopleMap := map[int64]PersonCandidate{
		1: {PersonID: 1},
	}

	result := &Result{
		People: []PersonSelection{},
	}

	filtered := filterValidPeople(123, result, peopleMap, logger)
	assert.Empty(t, filtered.PeopleIDs())
}

// TestFilterValidPeople_InvalidIDFormat handles malformed IDs.
func TestFilterValidPeople_InvalidIDFormat(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	peopleMap := map[int64]PersonCandidate{
		1: {PersonID: 1},
	}

	result := &Result{
		People: []PersonSelection{
			{ID: "Person:1", Reason: "valid"},
			{ID: "NotAPerson:999", Reason: "malformed"},
		},
	}

	filtered := filterValidPeople(123, result, peopleMap, logger)
	assert.Equal(t, []int64{1}, filtered.PeopleIDs())
}

// Artifact Filtering Tests

// TestFilterValidArtifacts_AllValid verifies all artifacts are kept when valid.
func TestFilterValidArtifacts_AllValid(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	artifactsMap := map[int64]ArtifactCandidate{
		1: {ArtifactID: 1},
		2: {ArtifactID: 2},
	}

	result := &Result{
		Topics:    []TopicSelection{{ID: "Topic:1", Reason: "t1"}},
		People:    []PersonSelection{{ID: "Person:1", Reason: "p1"}},
		Artifacts: []ArtifactSelection{{ID: "Artifact:1", Reason: "a1"}, {ID: "Artifact:2", Reason: "a2"}},
	}

	filtered := filterValidArtifacts(123, result, artifactsMap, logger)
	assert.Equal(t, []int64{1, 2}, filtered.ArtifactIDs())
	assert.Len(t, filtered.Artifacts, 2)
}

// TestFilterValidArtifacts_SomeHallucinated filters out hallucinated artifacts.
func TestFilterValidArtifacts_SomeHallucinated(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	artifactsMap := map[int64]ArtifactCandidate{
		1: {ArtifactID: 1},
		3: {ArtifactID: 3},
	}

	result := &Result{
		Artifacts: []ArtifactSelection{
			{ID: "Artifact:1", Reason: "valid"},
			{ID: "Artifact:999", Reason: "hallucinated"},
			{ID: "Artifact:3", Reason: "also valid"},
		},
	}

	filtered := filterValidArtifacts(123, result, artifactsMap, logger)
	assert.Equal(t, []int64{1, 3}, filtered.ArtifactIDs())
	assert.Len(t, filtered.Artifacts, 2)
}

// TestFilterValidArtifacts_AllHallucinated returns empty when all are invalid.
func TestFilterValidArtifacts_AllHallucinated(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	artifactsMap := map[int64]ArtifactCandidate{
		1: {ArtifactID: 1},
	}

	result := &Result{
		Artifacts: []ArtifactSelection{
			{ID: "Artifact:999", Reason: "fake 1"},
			{ID: "Artifact:888", Reason: "fake 2"},
		},
	}

	filtered := filterValidArtifacts(123, result, artifactsMap, logger)
	assert.Empty(t, filtered.ArtifactIDs())
	assert.Nil(t, filtered.Artifacts)
}

// TestFilterValidArtifacts_EmptyInput handles empty input gracefully.
func TestFilterValidArtifacts_EmptyInput(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	artifactsMap := map[int64]ArtifactCandidate{
		1: {ArtifactID: 1},
	}

	result := &Result{
		Artifacts: []ArtifactSelection{},
	}

	filtered := filterValidArtifacts(123, result, artifactsMap, logger)
	assert.Empty(t, filtered.ArtifactIDs())
}

// TestFilterValidArtifacts_InvalidIDFormat handles malformed IDs.
func TestFilterValidArtifacts_InvalidIDFormat(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	artifactsMap := map[int64]ArtifactCandidate{
		1: {ArtifactID: 1},
	}

	result := &Result{
		Artifacts: []ArtifactSelection{
			{ID: "Artifact:1", Reason: "valid"},
			{ID: "NotAnArtifact:999", Reason: "malformed"},
		},
	}

	filtered := filterValidArtifacts(123, result, artifactsMap, logger)
	assert.Equal(t, []int64{1}, filtered.ArtifactIDs())
}

// Array Parsing Tests

// TestParsePeopleArray_ObjectFormat parses people in object format.
func TestParsePeopleArray_ObjectFormat(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	data := []byte(`[{"id": "Person:1", "reason": "friend"}, {"id": "Person:2", "reason": "colleague"}]`)
	result := parsePeopleArray(data, logger)

	require.Len(t, result, 2)
	assert.Equal(t, "Person:1", result[0].ID)
	assert.Equal(t, "friend", result[0].Reason)
	assert.Equal(t, "Person:2", result[1].ID)
	assert.Equal(t, "colleague", result[1].Reason)
}

// TestParsePeopleArray_NumericFallback handles numeric format.
func TestParsePeopleArray_NumericFallback(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	data := []byte(`[1, 2, 3]`)
	result := parsePeopleArray(data, logger)

	require.Len(t, result, 3)
	assert.Equal(t, "Person:1", result[0].ID)
	assert.Equal(t, "Person:2", result[1].ID)
	assert.Equal(t, "Person:3", result[2].ID)
}

// TestParsePeopleArray_Empty handles empty array.
func TestParsePeopleArray_Empty(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	data := []byte(`[]`)
	result := parsePeopleArray(data, logger)

	assert.Nil(t, result)
}

// TestParsePeopleArray_Invalid handles invalid JSON.
func TestParsePeopleArray_Invalid(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	data := []byte(`invalid`)
	result := parsePeopleArray(data, logger)

	assert.Nil(t, result)
}

// TestParseArtifactArray_ObjectFormat parses artifacts in object format.
func TestParseArtifactArray_ObjectFormat(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	data := []byte(`[{"id": "Artifact:1", "reason": "document"}, {"id": "Artifact:2", "reason": "image"}]`)
	result := parseArtifactArray(data, logger)

	require.Len(t, result, 2)
	assert.Equal(t, "Artifact:1", result[0].ID)
	assert.Equal(t, "document", result[0].Reason)
	assert.Equal(t, "Artifact:2", result[1].ID)
	assert.Equal(t, "image", result[1].Reason)
}

// TestParseArtifactArray_NumericFallback handles numeric format.
func TestParseArtifactArray_NumericFallback(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	data := []byte(`[1, 2, 3]`)
	result := parseArtifactArray(data, logger)

	require.Len(t, result, 3)
	assert.Equal(t, "Artifact:1", result[0].ID)
	assert.Equal(t, "Artifact:2", result[1].ID)
	assert.Equal(t, "Artifact:3", result[2].ID)
}

// TestParseArtifactArray_Empty handles empty array.
func TestParseArtifactArray_Empty(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	data := []byte(`[]`)
	result := parseArtifactArray(data, logger)

	assert.Nil(t, result)
}

// TestParseArtifactArray_Invalid handles invalid JSON.
func TestParseArtifactArray_Invalid(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	data := []byte(`invalid`)
	result := parseArtifactArray(data, logger)

	assert.Nil(t, result)
}

// Build Map Tests

// TestBuildCandidateMap_Empty handles empty candidates.
func TestBuildCandidateMap_Empty(t *testing.T) {

	result := buildCandidateMap([]Candidate{})

	assert.Empty(t, result)
}

// TestBuildCandidateMap_Single builds map with single entry.
func TestBuildCandidateMap_Single(t *testing.T) {

	candidates := []Candidate{
		{TopicID: 42, Score: 0.9, Topic: storage.Topic{ID: 42, Summary: "Test"}},
	}

	result := buildCandidateMap(candidates)

	assert.Len(t, result, 1)
	assert.Contains(t, result, int64(42))
	assert.Equal(t, int64(42), result[42].TopicID)
}

// TestBuildCandidateMap_Multiple builds map with multiple entries.
func TestBuildCandidateMap_Multiple(t *testing.T) {

	candidates := []Candidate{
		{TopicID: 1, Score: 0.9, Topic: storage.Topic{ID: 1, Summary: "First"}},
		{TopicID: 2, Score: 0.8, Topic: storage.Topic{ID: 2, Summary: "Second"}},
		{TopicID: 3, Score: 0.7, Topic: storage.Topic{ID: 3, Summary: "Third"}},
	}

	result := buildCandidateMap(candidates)

	assert.Len(t, result, 3)
	assert.Contains(t, result, int64(1))
	assert.Contains(t, result, int64(2))
	assert.Contains(t, result, int64(3))
}

// TestBuildPeopleMap_Empty handles empty candidates.
func TestBuildPeopleMap_Empty(t *testing.T) {

	result := buildPeopleMap([]PersonCandidate{})

	assert.Empty(t, result)
}

// TestBuildPeopleMap_Single builds map with single entry.
func TestBuildPeopleMap_Single(t *testing.T) {

	candidates := []PersonCandidate{
		{PersonID: 42, Score: 0.9, Person: storage.Person{ID: 42, DisplayName: "Test"}},
	}

	result := buildPeopleMap(candidates)

	assert.Len(t, result, 1)
	assert.Contains(t, result, int64(42))
	assert.Equal(t, int64(42), result[42].PersonID)
}

// TestBuildPeopleMap_Multiple builds map with multiple entries.
func TestBuildPeopleMap_Multiple(t *testing.T) {

	candidates := []PersonCandidate{
		{PersonID: 1, Score: 0.9, Person: storage.Person{ID: 1, DisplayName: "First"}},
		{PersonID: 2, Score: 0.8, Person: storage.Person{ID: 2, DisplayName: "Second"}},
		{PersonID: 3, Score: 0.7, Person: storage.Person{ID: 3, DisplayName: "Third"}},
	}

	result := buildPeopleMap(candidates)

	assert.Len(t, result, 3)
	assert.Contains(t, result, int64(1))
	assert.Contains(t, result, int64(2))
	assert.Contains(t, result, int64(3))
}

// TestBuildArtifactsMap_Empty handles empty candidates.
func TestBuildArtifactsMap_Empty(t *testing.T) {

	result := buildArtifactsMap([]ArtifactCandidate{})

	assert.Empty(t, result)
}

// TestBuildArtifactsMap_Single builds map with single entry.
func TestBuildArtifactsMap_Single(t *testing.T) {

	candidates := []ArtifactCandidate{
		{ArtifactID: 42, Score: 0.9, OriginalName: "test.pdf"},
	}

	result := buildArtifactsMap(candidates)

	assert.Len(t, result, 1)
	assert.Contains(t, result, int64(42))
	assert.Equal(t, int64(42), result[42].ArtifactID)
}

// TestBuildArtifactsMap_Multiple builds map with multiple entries.
func TestBuildArtifactsMap_Multiple(t *testing.T) {
	candidates := []ArtifactCandidate{
		{ArtifactID: 1, Score: 0.9, OriginalName: "first.pdf"},
		{ArtifactID: 2, Score: 0.8, OriginalName: "second.pdf"},
		{ArtifactID: 3, Score: 0.7, OriginalName: "third.pdf"},
	}

	result := buildArtifactsMap(candidates)

	assert.Len(t, result, 3)
	assert.Contains(t, result, int64(1))
	assert.Contains(t, result, int64(2))
	assert.Contains(t, result, int64(3))
}

// Format Artifact Candidates Tests

// TestFormatArtifactCandidates_Empty handles empty candidates.
func TestFormatArtifactCandidates_Empty(t *testing.T) {
	result := formatArtifactCandidates([]ArtifactCandidate{})
	assert.Empty(t, result)
}

// TestFormatArtifactCandidates_WithKeywords formats with keywords.
func TestFormatArtifactCandidates_WithKeywords(t *testing.T) {
	candidates := []ArtifactCandidate{
		{ArtifactID: 1, Score: 0.9, FileType: "pdf", OriginalName: "doc.pdf", Summary: "Test doc", Keywords: []string{"kw1", "kw2"}},
	}

	result := formatArtifactCandidates(candidates)

	assert.Contains(t, result, "[Artifact:1]")
	assert.Contains(t, result, "pdf")
	assert.Contains(t, result, "doc.pdf")
	assert.Contains(t, result, "kw1, kw2")
	assert.Contains(t, result, "Test doc")
}

// TestFormatArtifactCandidates_WithEntities formats with entities.
func TestFormatArtifactCandidates_WithEntities(t *testing.T) {
	candidates := []ArtifactCandidate{
		{ArtifactID: 1, Score: 0.9, FileType: "pdf", OriginalName: "doc.pdf", Summary: "Test doc", Entities: []string{"Entity1", "Entity2"}},
	}

	result := formatArtifactCandidates(candidates)
	assert.Contains(t, result, "Entities: Entity1, Entity2")
}

// TestFormatArtifactCandidates_WithBoth formats with both keywords and entities.
func TestFormatArtifactCandidates_WithBoth(t *testing.T) {
	candidates := []ArtifactCandidate{
		{ArtifactID: 1, Score: 0.9, FileType: "pdf", OriginalName: "doc.pdf", Summary: "Test doc", Keywords: []string{"kw1"}, Entities: []string{"Entity1"}},
	}

	result := formatArtifactCandidates(candidates)
	assert.Contains(t, result, "kw1")
	assert.Contains(t, result, "Entities: Entity1")
}

// TestFormatArtifactCandidates_LongSummaryTruncation verifies summary truncation.
func TestFormatArtifactCandidates_LongSummaryTruncation(t *testing.T) {
	longSummary := strings.Repeat("word ", 100) // Long summary
	candidates := []ArtifactCandidate{
		{ArtifactID: 1, Score: 0.9, FileType: "pdf", OriginalName: "doc.pdf", Summary: longSummary},
	}

	result := formatArtifactCandidates(candidates)
	// Should contain the summary (not truncated by formatArtifactCandidates)
	assert.Contains(t, result, "word")
}

// Result Helper Methods Tests

// TestResult_PeopleIDs returns people IDs from result.
func TestResult_PeopleIDs(t *testing.T) {
	result := &Result{
		People: []PersonSelection{
			{ID: "Person:42", Reason: "a"},
			{ID: "Person:18", Reason: "b"},
			{ID: "Person:5", Reason: "c"},
		},
	}

	ids := result.PeopleIDs()
	assert.Equal(t, []int64{42, 18, 5}, ids)
}

// TestResult_PeopleIDsEmpty returns empty for nil people.
func TestResult_PeopleIDsEmpty(t *testing.T) {
	result := &Result{People: nil}
	ids := result.PeopleIDs()
	assert.Empty(t, ids)

	result2 := &Result{People: []PersonSelection{}}
	ids2 := result2.PeopleIDs()
	assert.Empty(t, ids2)
}

// TestResult_ArtifactIDs returns artifact IDs from result.
func TestResult_ArtifactIDs(t *testing.T) {
	result := &Result{
		Artifacts: []ArtifactSelection{
			{ID: "Artifact:42", Reason: "a"},
			{ID: "Artifact:18", Reason: "b"},
			{ID: "Artifact:5", Reason: "c"},
		},
	}

	ids := result.ArtifactIDs()
	assert.Equal(t, []int64{42, 18, 5}, ids)
}

// TestResult_ArtifactIDsEmpty returns empty for nil artifacts.
func TestResult_ArtifactIDsEmpty(t *testing.T) {
	result := &Result{Artifacts: nil}
	ids := result.ArtifactIDs()
	assert.Empty(t, ids)

	result2 := &Result{Artifacts: []ArtifactSelection{}}
	ids2 := result2.ArtifactIDs()
	assert.Empty(t, ids2)
}

// ID Parsing Tests (types.go)

// TestParseTopicID_ValidFormats parses valid topic ID formats.
func TestParseTopicID_ValidFormats(t *testing.T) {
	tests := []struct {
		input  string
		wantID int64
	}{
		{"Topic:42", 42},
		{"Topic:1", 1},
		{"42", 42},
		{"999", 999},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			id, err := parseTopicID(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

// TestParseTopicID_Invalid handles invalid topic ID formats.
func TestParseTopicID_Invalid(t *testing.T) {
	tests := []struct {
		input string
	}{
		{"NotATopic:42"},
		{"Person:42"},
		{"Artifact:42"},
		{"invalid"},
		{""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, err := parseTopicID(tt.input)
			assert.Error(t, err)
		})
	}
}

// TestParsePersonID_ValidFormats parses valid person ID formats.
func TestParsePersonID_ValidFormats(t *testing.T) {
	tests := []struct {
		input  string
		wantID int64
	}{
		{"Person:42", 42},
		{"Person:1", 1},
		{"42", 42},
		{"999", 999},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			id, err := parsePersonID(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

// TestParsePersonID_Invalid handles invalid person ID formats.
func TestParsePersonID_Invalid(t *testing.T) {
	tests := []struct {
		input string
	}{
		{"NotAPerson:42"},
		{"Topic:42"},
		{"Artifact:42"},
		{"invalid"},
		{""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, err := parsePersonID(tt.input)
			assert.Error(t, err)
		})
	}
}

// TestParseArtifactID_ValidFormats parses valid artifact ID formats.
func TestParseArtifactID_ValidFormats(t *testing.T) {
	tests := []struct {
		input  string
		wantID int64
	}{
		{"Artifact:42", 42},
		{"Artifact:1", 1},
		{"42", 42},
		{"999", 999},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			id, err := parseArtifactID(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

// TestParseArtifactID_Invalid handles invalid artifact ID formats.
func TestParseArtifactID_Invalid(t *testing.T) {
	tests := []struct {
		input string
	}{
		{"NotAnArtifact:42"},
		{"Topic:42"},
		{"Person:42"},
		{"invalid"},
		{""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, err := parseArtifactID(tt.input)
			assert.Error(t, err)
		})
	}
}

// Selection GetNumericID Tests

// TestTopicSelection_GetNumericID extracts numeric ID from topic.
func TestTopicSelection_GetNumericID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantID  int64
		wantErr bool
	}{
		{"valid with prefix", "Topic:42", 42, false},
		{"valid without prefix", "42", 42, false},
		{"invalid", "invalid", 0, true},
		{"wrong prefix", "Person:42", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := TopicSelection{ID: tt.id}
			id, err := ts.GetNumericID()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantID, id)
			}
		})
	}
}

// TestPersonSelection_GetNumericID extracts numeric ID from person.
func TestPersonSelection_GetNumericID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantID  int64
		wantErr bool
	}{
		{"valid with prefix", "Person:42", 42, false},
		{"valid without prefix", "42", 42, false},
		{"invalid", "invalid", 0, true},
		{"wrong prefix", "Topic:42", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := PersonSelection{ID: tt.id}
			id, err := ps.GetNumericID()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantID, id)
			}
		})
	}
}

// TestArtifactSelection_GetNumericID extracts numeric ID from artifact.
func TestArtifactSelection_GetNumericID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantID  int64
		wantErr bool
	}{
		{"valid with prefix", "Artifact:42", 42, false},
		{"valid without prefix", "42", 42, false},
		{"invalid", "invalid", 0, true},
		{"wrong prefix", "Topic:42", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			as := ArtifactSelection{ID: tt.id}
			id, err := as.GetNumericID()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantID, id)
			}
		})
	}
}

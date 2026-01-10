package reranker

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/stretchr/testify/assert"
)

func TestFormatCandidatesForReranker(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	r := &Reranker{logger: logger}

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
			wantContains: []string{"[ID:42]", "2026-01-10", "5 msgs", "~1K chars", "Test topic"},
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
			wantContains: []string{"[ID:100]", "~35K chars", "50 msgs"},
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
			result := r.formatCandidatesForReranker(tt.candidates)

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
	r := &Reranker{logger: logger}

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
			content:    `{"topic_ids": [{"id": 42, "reason": "relevant discussion"}, {"id": 18, "reason": "mentions person"}]}`,
			wantTopics: []int64{42, 18},
			wantSelections: []TopicSelection{
				{ID: 42, Reason: "relevant discussion"},
				{ID: 18, Reason: "mentions person"},
			},
			wantErr: false,
		},
		// Bare array format (Flash sometimes returns this)
		{
			name:       "bare array format",
			content:    `[{"id": 42, "reason": "relevant"}, {"id": 18, "reason": "mentions person"}]`,
			wantTopics: []int64{42, 18},
			wantSelections: []TopicSelection{
				{ID: 42, Reason: "relevant"},
				{ID: 18, Reason: "mentions person"},
			},
			wantErr: false,
		},
		{
			name:       "new format with empty reason",
			content:    `{"topic_ids": [{"id": 42}]}`,
			wantTopics: []int64{42},
			wantSelections: []TopicSelection{
				{ID: 42, Reason: ""},
			},
			wantErr: false,
		},
		// LLM quirk: wraps entire response in an array
		{
			name:       "wrapped in array (LLM quirk)",
			content:    `[{"topic_ids": [{"id": 5205, "reason": "discussion about limits"}, {"id": 4750, "reason": "cost context"}]}]`,
			wantTopics: []int64{5205, 4750},
			wantSelections: []TopicSelection{
				{ID: 5205, Reason: "discussion about limits"},
				{ID: 4750, Reason: "cost context"},
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
			result, err := r.parseResponse(tt.content)

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
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	r := &Reranker{logger: logger}

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
			ids, err := r.parseToolCallIDs(tt.arguments)

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
	r := &Reranker{logger: logger}

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
					{ID: 1, Reason: "a"},
					{ID: 2, Reason: "b"},
				},
			},
			wantTopics: []TopicSelection{
				{ID: 1, Reason: "a"},
				{ID: 2, Reason: "b"},
			},
		},
		{
			name: "some hallucinated",
			result: &Result{
				Topics: []TopicSelection{
					{ID: 1, Reason: "valid"},
					{ID: 999, Reason: "hallucinated"},
					{ID: 2, Reason: "also valid"},
				},
			},
			wantTopics: []TopicSelection{
				{ID: 1, Reason: "valid"},
				{ID: 2, Reason: "also valid"},
			},
		},
		{
			name: "all hallucinated",
			result: &Result{
				Topics: []TopicSelection{
					{ID: 999, Reason: "fake"},
					{ID: 888, Reason: "also fake"},
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
			filtered := r.filterValidTopics(123, tt.result, candidateMap)
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
	r := &Reranker{logger: logger}

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
			result := r.fallbackToVectorTop(candidates, tt.maxTopics)
			assert.Equal(t, tt.wantIDs, result.TopicIDs())
		})
	}
}

func TestFallbackFromState(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	r := &Reranker{logger: logger}

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
			result := r.fallbackFromState(tt.state, candidates, tt.maxTopics)
			assert.Equal(t, tt.wantIDs, result.TopicIDs())
		})
	}
}

func TestResultTopicIDs(t *testing.T) {
	result := &Result{
		Topics: []TopicSelection{
			{ID: 42, Reason: "a"},
			{ID: 18, Reason: "b"},
			{ID: 5, Reason: "c"},
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

// MockMessageRepository for testing
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

	r := &Reranker{
		logger:  logger,
		msgRepo: mockRepo,
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
	st := &state{}

	content := r.loadTopicsContent(context.Background(), 123, []int64{1, 2}, candidateMap, tr, st)

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

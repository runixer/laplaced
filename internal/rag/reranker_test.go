package rag

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestFormatCandidatesForReranker(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s := &Service{logger: logger}

	tests := []struct {
		name         string
		candidates   []rerankerCandidate
		wantLines    int
		wantContains []string
	}{
		{
			name:       "empty candidates",
			candidates: []rerankerCandidate{},
			wantLines:  0,
		},
		{
			name: "single candidate",
			candidates: []rerankerCandidate{
				{
					TopicID:      42,
					Score:        0.85,
					MessageCount: 15,
					SizeChars:    7500, // 15 * 500
					Topic: storage.Topic{
						Summary:   "Discussion about Go programming",
						CreatedAt: time.Date(2025, 7, 15, 10, 0, 0, 0, time.UTC),
					},
				},
			},
			wantLines:    1,
			wantContains: []string{"[ID:42]", "2025-07-15", "15 msgs", "~7K chars", "Discussion about Go programming"},
		},
		{
			name: "multiple candidates",
			candidates: []rerankerCandidate{
				{
					TopicID:      1,
					MessageCount: 5,
					Topic: storage.Topic{
						Summary:   "Topic A",
						CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
					},
				},
				{
					TopicID:      2,
					MessageCount: 10,
					Topic: storage.Topic{
						Summary:   "Topic B",
						CreatedAt: time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC),
					},
				},
			},
			wantLines:    2,
			wantContains: []string{"[ID:1]", "[ID:2]", "Topic A", "Topic B"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.formatCandidatesForReranker(tt.candidates)

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

func TestBuildCandidateMap(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s := &Service{logger: logger}

	candidates := []rerankerCandidate{
		{TopicID: 1, Score: 0.9},
		{TopicID: 5, Score: 0.8},
		{TopicID: 10, Score: 0.7},
	}

	m := s.buildCandidateMap(candidates)

	assert.Len(t, m, 3)
	assert.Equal(t, float32(0.9), m[1].Score)
	assert.Equal(t, float32(0.8), m[5].Score)
	assert.Equal(t, float32(0.7), m[10].Score)

	// Non-existent key
	_, exists := m[999]
	assert.False(t, exists)
}

func TestParseToolCallIDs(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s := &Service{logger: logger}

	tests := []struct {
		name      string
		arguments string
		wantIDs   []int64
		wantErr   bool
	}{
		{
			name:      "valid single ID",
			arguments: `{"topic_ids": [42]}`,
			wantIDs:   []int64{42},
			wantErr:   false,
		},
		{
			name:      "valid multiple IDs",
			arguments: `{"topic_ids": [1, 5, 10, 42]}`,
			wantIDs:   []int64{1, 5, 10, 42},
			wantErr:   false,
		},
		{
			name:      "empty array",
			arguments: `{"topic_ids": []}`,
			wantIDs:   []int64{},
			wantErr:   false,
		},
		{
			name:      "invalid JSON",
			arguments: `{invalid`,
			wantIDs:   nil,
			wantErr:   true,
		},
		{
			name:      "missing topic_ids field",
			arguments: `{"other": [1, 2]}`,
			wantIDs:   nil,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, err := s.parseToolCallIDs(tt.arguments)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantIDs, ids)
			}
		})
	}
}

func TestParseRerankerResponse(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s := &Service{logger: logger}

	// Helper to create string pointer
	strPtr := func(s string) *string { return &s }

	tests := []struct {
		name           string
		content        string
		wantTopics     []int64
		wantSelections []TopicSelection // For detailed checks
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
				{ID: 42, Reason: "relevant discussion", Excerpt: nil},
				{ID: 18, Reason: "mentions person", Excerpt: nil},
			},
			wantErr: false,
		},
		{
			name:       "new format with excerpt",
			content:    `{"topic_ids": [{"id": 42, "reason": "big topic", "excerpt": "[User]: relevant message..."}]}`,
			wantTopics: []int64{42},
			wantSelections: []TopicSelection{
				{ID: 42, Reason: "big topic", Excerpt: strPtr("[User]: relevant message...")},
			},
			wantErr: false,
		},
		{
			name:       "new format with null excerpt",
			content:    `{"topic_ids": [{"id": 42, "reason": "small topic", "excerpt": null}]}`,
			wantTopics: []int64{42},
			wantSelections: []TopicSelection{
				{ID: 42, Reason: "small topic", Excerpt: nil},
			},
			wantErr: false,
		},
		// Bare array format (Flash sometimes returns this)
		{
			name:       "bare array format",
			content:    `[{"id": 42, "reason": "relevant"}, {"id": 18, "reason": "mentions person"}]`,
			wantTopics: []int64{42, 18},
			wantSelections: []TopicSelection{
				{ID: 42, Reason: "relevant", Excerpt: nil},
				{ID: 18, Reason: "mentions person", Excerpt: nil},
			},
			wantErr: false,
		},
		{
			name:       "bare array with excerpt",
			content:    `[{"id": 42, "reason": "big topic", "excerpt": "[User]: message..."}]`,
			wantTopics: []int64{42},
			wantSelections: []TopicSelection{
				{ID: 42, Reason: "big topic", Excerpt: strPtr("[User]: message...")},
			},
			wantErr: false,
		},
		{
			name:       "new format with empty reason",
			content:    `{"topic_ids": [{"id": 42}]}`,
			wantTopics: []int64{42},
			wantSelections: []TopicSelection{
				{ID: 42, Reason: "", Excerpt: nil},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := s.parseRerankerResponse(tt.content)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wantTopics, result.TopicIDs())
			assert.Nil(t, result.PeopleIDs) // Reserved for v0.5

			// Check detailed selections if specified
			if tt.wantSelections != nil {
				assert.Equal(t, tt.wantSelections, result.Topics)
			}
		})
	}
}

func TestFallbackToVectorTop(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s := &Service{logger: logger}

	candidates := []rerankerCandidate{
		{TopicID: 1, Score: 0.95},
		{TopicID: 2, Score: 0.90},
		{TopicID: 3, Score: 0.85},
		{TopicID: 4, Score: 0.80},
		{TopicID: 5, Score: 0.75},
		{TopicID: 6, Score: 0.70},
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
			name:      "limit to 5",
			maxTopics: 5,
			wantIDs:   []int64{1, 2, 3, 4, 5},
		},
		{
			name:      "limit exceeds candidates",
			maxTopics: 10,
			wantIDs:   []int64{1, 2, 3, 4, 5, 6},
		},
		{
			name:      "zero defaults to 5",
			maxTopics: 0,
			wantIDs:   []int64{1, 2, 3, 4, 5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.fallbackToVectorTop(candidates, tt.maxTopics)
			assert.Equal(t, tt.wantIDs, result.TopicIDs())
		})
	}
}

func TestFallbackFromState(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s := &Service{logger: logger}

	candidates := []rerankerCandidate{
		{TopicID: 10, Score: 0.9},
		{TopicID: 20, Score: 0.8},
		{TopicID: 30, Score: 0.7},
	}

	tests := []struct {
		name         string
		requestedIDs []int64
		maxTopics    int
		wantIDs      []int64
	}{
		{
			name:         "use requestedIDs when available",
			requestedIDs: []int64{42, 18, 5},
			maxTopics:    5,
			wantIDs:      []int64{42, 18, 5},
		},
		{
			name:         "limit requestedIDs",
			requestedIDs: []int64{1, 2, 3, 4, 5, 6, 7},
			maxTopics:    3,
			wantIDs:      []int64{1, 2, 3},
		},
		{
			name:         "fallback to vector when no requestedIDs",
			requestedIDs: []int64{},
			maxTopics:    2,
			wantIDs:      []int64{10, 20},
		},
		{
			name:         "fallback to vector when nil requestedIDs",
			requestedIDs: nil,
			maxTopics:    2,
			wantIDs:      []int64{10, 20},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &rerankerState{requestedIDs: tt.requestedIDs}
			result := s.fallbackFromState(state, candidates, tt.maxTopics)
			assert.Equal(t, tt.wantIDs, result.TopicIDs())
		})
	}
}

func TestFormatUserProfileForReranker(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	mockStore := new(MockStorage)

	s := &Service{
		logger:   logger,
		factRepo: mockStore,
	}

	userID := int64(123)

	tests := []struct {
		name         string
		facts        []storage.Fact
		factsErr     error
		wantContains []string
		wantEmpty    bool
	}{
		{
			name: "filters identity facts",
			facts: []storage.Fact{
				{Entity: "User", Type: "identity", Importance: 95, Content: "Works as Go developer"},
				{Entity: "User", Type: "context", Importance: 50, Content: "Current project: Laplaced"},
				{Entity: "User", Type: "identity", Importance: 90, Content: "Uses Linux"},
			},
			wantContains: []string{"[User] Works as Go developer", "[User] Uses Linux"},
			wantEmpty:    false,
		},
		{
			name: "includes high importance non-identity",
			facts: []storage.Fact{
				{Entity: "User", Type: "context", Importance: 85, Content: "High importance context"},
				{Entity: "User", Type: "status", Importance: 30, Content: "Low importance status"},
			},
			wantContains: []string{"[User] High importance context"},
			wantEmpty:    false,
		},
		{
			name:      "empty on error",
			facts:     nil,
			factsErr:  assert.AnError,
			wantEmpty: true,
		},
		{
			name:      "empty when no relevant facts",
			facts:     []storage.Fact{{Entity: "User", Type: "status", Importance: 20, Content: "Low"}},
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStore.ExpectedCalls = nil
			mockStore.On("GetFacts", userID).Return(tt.facts, tt.factsErr)

			result := s.formatUserProfileForReranker(context.Background(), userID)

			if tt.wantEmpty {
				assert.Empty(t, result)
				return
			}

			for _, want := range tt.wantContains {
				assert.Contains(t, result, want)
			}
		})
	}
}

func TestRerankCandidates_Disabled(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg := &config.Config{}
	cfg.RAG.Reranker.Enabled = false
	cfg.RAG.Reranker.MaxTopics = 3

	s := &Service{
		logger: logger,
		cfg:    cfg,
	}

	candidates := []rerankerCandidate{
		{TopicID: 1, Score: 0.9},
		{TopicID: 2, Score: 0.8},
		{TopicID: 3, Score: 0.7},
		{TopicID: 4, Score: 0.6},
	}

	result, err := s.rerankCandidates(context.Background(), 123, candidates, "contextual_query", "original_query", "current_messages", "profile", nil)

	assert.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3}, result.TopicIDs())
}

func TestRerankCandidates_EmptyCandidates(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg := &config.Config{}
	cfg.RAG.Reranker.Enabled = true
	cfg.RAG.Reranker.MaxTopics = 5

	s := &Service{
		logger: logger,
		cfg:    cfg,
	}

	result, err := s.rerankCandidates(context.Background(), 123, []rerankerCandidate{}, "contextual_query", "original_query", "current_messages", "profile", nil)

	assert.NoError(t, err)
	assert.Empty(t, result.TopicIDs())
}

func TestRerankCandidates_ProtocolViolation_NoToolCalls(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	mockClient := new(MockClient)
	mockStore := new(MockStorage)

	translator, err := i18n.NewTranslator("../../internal/i18n/locales")
	if err != nil {
		t.Skipf("Skipping test: could not load translations: %v", err)
	}

	cfg := &config.Config{}
	cfg.RAG.Reranker.Enabled = true
	cfg.RAG.Reranker.Model = "test-model"
	cfg.RAG.Reranker.MaxTopics = 5
	cfg.RAG.Reranker.MaxPeople = 10
	cfg.RAG.Reranker.Timeout = "10s"
	cfg.RAG.Reranker.MaxToolCalls = 3
	cfg.Bot.Language = "en"

	s := &Service{
		logger:     logger,
		cfg:        cfg,
		client:     mockClient,
		msgRepo:    mockStore,
		translator: translator,
	}

	candidates := []rerankerCandidate{
		{TopicID: 1, Score: 0.9, MessageCount: 10, Topic: storage.Topic{Summary: "Topic 1", CreatedAt: time.Now()}},
		{TopicID: 2, Score: 0.8, MessageCount: 5, Topic: storage.Topic{Summary: "Topic 2", CreatedAt: time.Now()}},
	}

	// Mock LLM response - direct JSON without tool calls (protocol violation!)
	// Flash MUST call get_topics_content before returning final response.
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		openrouter.ChatCompletionResponse{
			Choices: []struct {
				Message struct {
					Role             string                `json:"role"`
					Content          string                `json:"content"`
					ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
					ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
				} `json:"message"`
				FinishReason string `json:"finish_reason,omitempty"`
				Index        int    `json:"index"`
			}{
				{
					Message: struct {
						Role             string                `json:"role"`
						Content          string                `json:"content"`
						ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
						ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
					}{
						Role:    "assistant",
						Content: `{"topic_ids": [1]}`,
					},
				},
			},
		}, nil,
	)

	result, err := s.rerankCandidates(context.Background(), 123, candidates, "contextual_query", "original_query", "current_messages", "profile", nil)

	assert.NoError(t, err)
	// Protocol violation: no tool calls → falls back to vector top (all candidates by score)
	assert.Equal(t, []int64{1, 2}, result.TopicIDs())
	// Reasons are empty because it's a fallback
	for _, topic := range result.Topics {
		assert.Empty(t, topic.Reason)
		assert.Nil(t, topic.Excerpt)
	}
	mockClient.AssertExpectations(t)
}

func TestRerankCandidates_WithToolCall(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	mockClient := new(MockClient)
	mockStore := new(MockStorage)

	translator, err := i18n.NewTranslator("../../internal/i18n/locales")
	if err != nil {
		t.Skipf("Skipping test: could not load translations: %v", err)
	}

	cfg := &config.Config{}
	cfg.RAG.Reranker.Enabled = true
	cfg.RAG.Reranker.Model = "test-model"
	cfg.RAG.Reranker.MaxTopics = 5
	cfg.RAG.Reranker.MaxPeople = 10
	cfg.RAG.Reranker.Timeout = "10s"
	cfg.RAG.Reranker.MaxToolCalls = 3
	cfg.Bot.Language = "en"

	s := &Service{
		logger:     logger,
		cfg:        cfg,
		client:     mockClient,
		msgRepo:    mockStore,
		translator: translator,
	}

	topic1 := storage.Topic{
		ID:         1,
		Summary:    "Topic 1",
		StartMsgID: 10,
		EndMsgID:   12,
		CreatedAt:  time.Now(),
	}

	candidates := []rerankerCandidate{
		{TopicID: 1, Score: 0.9, MessageCount: 3, Topic: topic1},
		{TopicID: 2, Score: 0.8, MessageCount: 5, Topic: storage.Topic{Summary: "Topic 2", CreatedAt: time.Now()}},
	}

	// First call: LLM requests tool call
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		openrouter.ChatCompletionResponse{
			Choices: []struct {
				Message struct {
					Role             string                `json:"role"`
					Content          string                `json:"content"`
					ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
					ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
				} `json:"message"`
				FinishReason string `json:"finish_reason,omitempty"`
				Index        int    `json:"index"`
			}{
				{
					Message: struct {
						Role             string                `json:"role"`
						Content          string                `json:"content"`
						ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
						ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
					}{
						Role: "assistant",
						ToolCalls: []openrouter.ToolCall{
							{
								ID:   "call_1",
								Type: "function",
								Function: struct {
									Name      string `json:"name"`
									Arguments string `json:"arguments"`
								}{
									Name:      "get_topics_content",
									Arguments: `{"topic_ids": [1]}`,
								},
							},
						},
					},
				},
			},
		}, nil,
	).Once()

	// Mock message loading
	mockStore.On("GetMessagesInRange", mock.Anything, int64(123), int64(10), int64(12)).Return(
		[]storage.Message{
			{ID: 10, Role: "user", Content: "Hello", CreatedAt: time.Now()},
			{ID: 11, Role: "assistant", Content: "Hi!", CreatedAt: time.Now()},
		}, nil,
	)

	// Second call: LLM returns final response
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		openrouter.ChatCompletionResponse{
			Choices: []struct {
				Message struct {
					Role             string                `json:"role"`
					Content          string                `json:"content"`
					ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
					ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
				} `json:"message"`
				FinishReason string `json:"finish_reason,omitempty"`
				Index        int    `json:"index"`
			}{
				{
					Message: struct {
						Role             string                `json:"role"`
						Content          string                `json:"content"`
						ToolCalls        []openrouter.ToolCall `json:"tool_calls,omitempty"`
						ReasoningDetails interface{}           `json:"reasoning_details,omitempty"`
					}{
						Role:    "assistant",
						Content: `{"topic_ids": [1]}`,
					},
				},
			},
		}, nil,
	).Once()

	result, err := s.rerankCandidates(context.Background(), 123, candidates, "contextual_query", "original_query", "current_messages", "profile", nil)

	assert.NoError(t, err)
	assert.Equal(t, []int64{1}, result.TopicIDs())
	mockClient.AssertExpectations(t)
	mockStore.AssertExpectations(t)
}

func TestRerankCandidates_LLMError_FallbackToVector(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	mockClient := new(MockClient)

	translator, err := i18n.NewTranslator("../../internal/i18n/locales")
	if err != nil {
		t.Skipf("Skipping test: could not load translations: %v", err)
	}

	cfg := &config.Config{}
	cfg.RAG.Reranker.Enabled = true
	cfg.RAG.Reranker.Model = "test-model"
	cfg.RAG.Reranker.MaxTopics = 2
	cfg.RAG.Reranker.MaxPeople = 10
	cfg.RAG.Reranker.Timeout = "10s"
	cfg.RAG.Reranker.MaxToolCalls = 3
	cfg.Bot.Language = "en"

	s := &Service{
		logger:     logger,
		cfg:        cfg,
		client:     mockClient,
		translator: translator,
	}

	candidates := []rerankerCandidate{
		{TopicID: 1, Score: 0.9},
		{TopicID: 2, Score: 0.8},
		{TopicID: 3, Score: 0.7},
	}

	// LLM returns error
	mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(
		openrouter.ChatCompletionResponse{}, assert.AnError,
	)

	result, err := s.rerankCandidates(context.Background(), 123, candidates, "contextual_query", "original_query", "current_messages", "profile", nil)

	// Should not error, should fallback
	assert.NoError(t, err)
	assert.Equal(t, []int64{1, 2}, result.TopicIDs()) // top-2 by vector score
	mockClient.AssertExpectations(t)
}

func TestValidateExcerpts(t *testing.T) {
	// Capture log output
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	s := &Service{logger: logger}

	tests := []struct {
		name         string
		result       *RerankerResult
		candidateMap map[int64]rerankerCandidate
		wantWarnings []string // substrings expected in log
		noWarnings   bool
	}{
		{
			name: "no warning for small topic without excerpt",
			result: &RerankerResult{
				Topics: []TopicSelection{{ID: 1, Reason: "test"}},
			},
			candidateMap: map[int64]rerankerCandidate{
				1: {TopicID: 1, SizeChars: 10000}, // < 25K threshold
			},
			noWarnings: true,
		},
		{
			name: "warning for large topic without excerpt",
			result: &RerankerResult{
				Topics: []TopicSelection{{ID: 1, Reason: "test"}},
			},
			candidateMap: map[int64]rerankerCandidate{
				1: {TopicID: 1, SizeChars: 30000}, // > 25K threshold
			},
			wantWarnings: []string{"large topic without excerpt"},
		},
		{
			name: "warning for too short excerpt",
			result: &RerankerResult{
				Topics: []TopicSelection{{ID: 1, Reason: "test", Excerpt: ptr("short")}},
			},
			candidateMap: map[int64]rerankerCandidate{
				1: {TopicID: 1, SizeChars: 30000},
			},
			wantWarnings: []string{"excerpt too short"},
		},
		{
			name: "warning for too long excerpt (>50% of topic)",
			result: &RerankerResult{
				Topics: []TopicSelection{{ID: 1, Reason: "test", Excerpt: ptr(string(make([]byte, 20000)))}},
			},
			candidateMap: map[int64]rerankerCandidate{
				1: {TopicID: 1, SizeChars: 30000}, // excerpt is 20K / 30K = 66%
			},
			wantWarnings: []string{"excerpt too long"},
		},
		{
			name: "no warning for good excerpt",
			result: &RerankerResult{
				Topics: []TopicSelection{{ID: 1, Reason: "test", Excerpt: ptr(string(make([]byte, 5000)))}},
			},
			candidateMap: map[int64]rerankerCandidate{
				1: {TopicID: 1, SizeChars: 30000}, // excerpt is 5K / 30K = 16%
			},
			noWarnings: true,
		},
		{
			name: "skip unknown topic ID",
			result: &RerankerResult{
				Topics: []TopicSelection{{ID: 999, Reason: "test"}},
			},
			candidateMap: map[int64]rerankerCandidate{
				1: {TopicID: 1, SizeChars: 30000},
			},
			noWarnings: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logBuf.Reset()
			s.validateExcerpts(123, tt.result, tt.candidateMap, nil) // nil loadedContents skips hallucination check
			logOutput := logBuf.String()

			if tt.noWarnings {
				assert.Empty(t, logOutput, "expected no warnings")
			} else {
				for _, warn := range tt.wantWarnings {
					assert.Contains(t, logOutput, warn, "expected warning: %s", warn)
				}
			}
		})
	}
}

// ptr is a helper to create string pointer
func ptr(s string) *string {
	return &s
}

func TestExcerptFoundInContent(t *testing.T) {
	tests := []struct {
		name    string
		excerpt string
		content string
		want    bool
	}{
		{
			name:    "content substring match",
			excerpt: "Hello world this is a test message from user",
			content: "[User (2026-01-01 12:00:00)]: Hello world this is a test message from user\n[Assistant]: Hi there",
			want:    true,
		},
		{
			name:    "normalized match (case insensitive)",
			excerpt: "hello world this is a longer test message",
			content: "[User (2026-01-01 12:00:00)]: Hello World This Is A Longer Test Message\n",
			want:    true,
		},
		{
			name:    "partial match with [...]",
			excerpt: "[User]: First message content here\n[...]\n[User]: Last message content here",
			content: "[User]: First message content here\n[Assistant]: Response\n[User]: Middle\n[User]: Last message content here\n",
			want:    true,
		},
		{
			name:    "hallucinated excerpt - not in content",
			excerpt: "[User]: This text was never in the topic at all",
			content: "[User]: Completely different content here\n[Assistant]: Something else entirely",
			want:    false,
		},
		{
			name:    "very short excerpt",
			excerpt: "Hi",
			content: "[User]: Hi there\n",
			want:    true,
		},
		{
			name:    "excerpt from current dialog (hallucination case)",
			excerpt: "[User]: мы уже отказались от Яндекс-транскрибации теперь ты мультимодальный",
			content: "[User]: Технический отчёт за январь\n[Assistant]: Обсуждаем vLLM на H100",
			want:    false,
		},
		{
			name:    "partial match with ... (no brackets)",
			excerpt: "[User]: ...важно именно проверка на тридцать секунд голосового...",
			content: "[User]: безусловно важно именно проверка на тридцать секунд голосового потому что это критично\n[Assistant]: Да",
			want:    true,
		},
		{
			name:    "partial match with mixed ellipsis patterns",
			excerpt: "[User]: First part of message...\n[...]\n[User]: ...and the ending here",
			content: "[User]: First part of message that continues longer\n[Assistant]: Response\n[User]: Some middle and the ending here with more text\n",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := excerptFoundInContent(tt.excerpt, tt.content)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateExcerptsHallucination(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	s := &Service{logger: logger}

	// Test that hallucinated excerpt triggers warning
	result := &RerankerResult{
		Topics: []TopicSelection{
			{ID: 1, Reason: "test", Excerpt: ptr("[User]: This was never in the topic")},
		},
	}
	candidateMap := map[int64]rerankerCandidate{
		1: {TopicID: 1, SizeChars: 30000}, // Large topic
	}
	loadedContents := map[int64]string{
		1: "[User]: Completely different content\n[Assistant]: Some response here",
	}

	s.validateExcerpts(123, result, candidateMap, loadedContents)
	logOutput := logBuf.String()

	assert.Contains(t, logOutput, "excerpt not found in topic content")
	assert.Contains(t, logOutput, "possible hallucination")
}

func TestRequestedIDsDeduplication(t *testing.T) {
	// Test that duplicate IDs are not added to requestedIDs
	state := &rerankerState{requestedIDs: []int64{}}

	// Simulate adding IDs (same logic as in rerankCandidates)
	addIDs := func(ids []int64) {
		for _, id := range ids {
			isDuplicate := false
			for _, existing := range state.requestedIDs {
				if existing == id {
					isDuplicate = true
					break
				}
			}
			if !isDuplicate {
				state.requestedIDs = append(state.requestedIDs, id)
			}
		}
	}

	// First batch
	addIDs([]int64{1, 2, 3})
	assert.Equal(t, []int64{1, 2, 3}, state.requestedIDs)

	// Add duplicates
	addIDs([]int64{2, 3, 4})
	assert.Equal(t, []int64{1, 2, 3, 4}, state.requestedIDs)

	// Add more duplicates
	addIDs([]int64{1, 1, 1, 5})
	assert.Equal(t, []int64{1, 2, 3, 4, 5}, state.requestedIDs)
}

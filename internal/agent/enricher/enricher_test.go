package enricher

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestEnricher_Execute(t *testing.T) {
	tests := []struct {
		name           string
		query          string
		history        []storage.Message
		llmResponse    string
		expectedResult string
		expectError    bool
	}{
		{
			name:           "simple query expansion",
			query:          "что мы обсуждали про Go?",
			llmResponse:    "Go programming golang обсуждение разработка",
			expectedResult: "Go programming golang обсуждение разработка",
		},
		{
			name:  "query with history",
			query: "а ещё?",
			history: []storage.Message{
				{Role: "user", Content: "Расскажи про Docker"},
				{Role: "assistant", Content: "Docker - это платформа контейнеризации..."},
			},
			llmResponse:    "Docker контейнеры контейнеризация развертывание",
			expectedResult: "Docker контейнеры контейнеризация развертывание",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mocks
			mockClient := &testutil.MockOpenRouterClient{}
			mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
				Return(testutil.MockChatResponse(tt.llmResponse), nil)

			executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
			cfg := testutil.TestConfig()
			cfg.Agents.Enricher.Model = "test-model"
			translator := testutil.TestTranslator(t)

			enricher := New(executor, translator, cfg)

			// Build request
			req := &agent.Request{
				Query: tt.query,
				Shared: &agent.SharedContext{
					UserID:       123,
					Profile:      "<user_profile>\n- Software engineer\n</user_profile>",
					RecentTopics: "<recent_topics>\n</recent_topics>",
				},
			}
			if len(tt.history) > 0 {
				req.Params = map[string]any{
					ParamHistory: tt.history,
				}
			}

			// Execute
			resp, err := enricher.Execute(context.Background(), req)

			// Assert
			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedResult, resp.Content)
			assert.NotZero(t, resp.Tokens.Total)

			// Check metadata
			assert.Equal(t, tt.query, resp.Metadata["original_query"])

			mockClient.AssertExpectations(t)
		})
	}
}

func TestEnricher_NoModel(t *testing.T) {
	// When no model is configured, enricher should return original query
	mockClient := &testutil.MockOpenRouterClient{}
	executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
	cfg := testutil.TestConfig()
	cfg.Agents.Enricher.Model = "" // No model configured
	cfg.Agents.Default.Model = ""  // No default either
	translator := testutil.TestTranslator(t)

	enricher := New(executor, translator, cfg)

	req := &agent.Request{
		Query: "test query",
	}

	resp, err := enricher.Execute(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "test query", resp.Content)

	// Should not call LLM
	mockClient.AssertNotCalled(t, "CreateChatCompletion")
}

func TestEnricher_Type(t *testing.T) {
	enricher := &Enricher{}
	assert.Equal(t, agent.TypeEnricher, enricher.Type())
}

func TestEnricher_Capabilities(t *testing.T) {
	enricher := &Enricher{}
	caps := enricher.Capabilities()

	assert.False(t, caps.IsAgentic)
	assert.Contains(t, caps.SupportedMedia, "image")
	assert.Contains(t, caps.SupportedMedia, "audio")
	assert.Equal(t, "text", caps.OutputFormat)
}

func TestEnricher_getHistory(t *testing.T) {
	t.Helper()

	tests := []struct {
		name     string
		params   map[string]any
		expected []storage.Message
	}{
		{
			name:     "no params",
			params:   nil,
			expected: nil,
		},
		{
			name:     "empty params",
			params:   map[string]any{},
			expected: nil,
		},
		{
			name: "wrong type",
			params: map[string]any{
				ParamHistory: "not a slice",
			},
			expected: nil,
		},
		{
			name: "valid history",
			params: map[string]any{
				ParamHistory: []storage.Message{
					{ID: 1, Role: "user", Content: "Hello"},
					{ID: 2, Role: "assistant", Content: "Hi there!"},
				},
			},
			expected: []storage.Message{
				{ID: 1, Role: "user", Content: "Hello"},
				{ID: 2, Role: "assistant", Content: "Hi there!"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Enricher{}
			result := e.getHistory(&agent.Request{Params: tt.params})
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEnricher_getContext(t *testing.T) {
	t.Helper()

	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)

	tests := []struct {
		name               string
		shared             *agent.SharedContext
		expectProfileEmpty bool
		expectTopicsEmpty  bool
	}{
		{
			name:               "no shared context",
			shared:             nil,
			expectProfileEmpty: false, // getContext returns default template tags even without shared context
			expectTopicsEmpty:  false,
		},
		{
			name: "with shared context",
			shared: &agent.SharedContext{
				UserID:       123,
				Profile:      "<user_profile>\n- Software engineer\n</user_profile>",
				RecentTopics: "<recent_topics>\n- Go discussion\n</recent_topics>",
			},
			expectProfileEmpty: false,
			expectTopicsEmpty:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := New(nil, translator, cfg)
			profile, topics := e.getContext(context.Background(), &agent.Request{Shared: tt.shared})

			if tt.name == "no shared context" {
				// When no shared context, GetSharedContext returns empty template tags
				assert.Contains(t, profile, "<user_profile>")
				assert.Contains(t, topics, "<recent_topics>")
			} else {
				// With shared context, expect actual content
				if tt.expectProfileEmpty {
					assert.Empty(t, profile)
				} else {
					assert.Contains(t, profile, "Software engineer")
				}

				if tt.expectTopicsEmpty {
					assert.Empty(t, topics)
				} else {
					assert.Contains(t, topics, "Go discussion")
				}
			}
		})
	}
}

func TestEnricher_buildMessages(t *testing.T) {
	t.Helper()

	cfg := testutil.TestConfig()
	translator := testutil.TestTranslator(t)
	e := New(nil, translator, cfg)

	systemPrompt := "You are a helpful assistant."
	userPrompt := "Expand this query."

	tests := []struct {
		name         string
		mediaParts   []interface{}
		expectSystem bool
		expectUser   bool
		expectParts  int
	}{
		{
			name:         "no media",
			mediaParts:   nil,
			expectSystem: true,
			expectUser:   true,
			expectParts:  2, // system + user
		},
		{
			name: "with file",
			mediaParts: []interface{}{
				openrouter.FilePart{
					Type: "file",
					File: openrouter.File{
						FileName: "image.png",
						FileData: "data:image/png;base64,iVBORw0KG...",
					},
				},
			},
			expectSystem: true,
			expectUser:   true,
			expectParts:  2, // system + user (user has multiple parts)
		},
		{
			name: "multiple media",
			mediaParts: []interface{}{
				openrouter.FilePart{
					Type: "file",
					File: openrouter.File{
						FileName: "image.png",
						FileData: "data:image/png;base64,iVBORw0KG...",
					},
				},
				openrouter.FilePart{
					Type: "file",
					File: openrouter.File{
						FileName: "audio.ogg",
						FileData: "data:audio/ogg;base64,...",
					},
				},
			},
			expectSystem: true,
			expectUser:   true,
			expectParts:  2, // system + user (user has text + 2 media parts)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &agent.Request{}
			if tt.mediaParts != nil {
				req.Params = map[string]any{
					ParamMediaParts: tt.mediaParts,
				}
			}

			messages := e.buildMessages(systemPrompt, userPrompt, req)

			assert.Len(t, messages, tt.expectParts)

			// Check system message
			if tt.expectSystem {
				assert.Equal(t, "system", messages[0].Role)
				assert.Equal(t, systemPrompt, messages[0].Content)
			}

			// Check user message
			if tt.expectUser {
				userMsg := messages[1]
				assert.Equal(t, "user", userMsg.Role)

				// User content should be a slice with media parts
				if len(tt.mediaParts) > 0 {
					content, ok := userMsg.Content.([]interface{})
					require.True(t, ok, "user content should be a slice when media is present")
					// First part is text with instruction
					_, isTextPart := content[0].(openrouter.TextPart)
					assert.True(t, isTextPart, "first part should be TextPart")
					assert.GreaterOrEqual(t, len(content), 2)
				} else {
					assert.Equal(t, userPrompt, userMsg.Content)
				}
			}
		})
	}
}

func TestEnricher_SpecialCharacters(t *testing.T) {
	// Test that special characters in query are handled correctly
	tests := []struct {
		name        string
		query       string
		llmResponse string
	}{
		{
			name:        "unicode characters",
			query:       "что мы обсуждали про 日本語?",
			llmResponse: "日本語 Nihongo Japanese язык",
		},
		{
			name:        "quotes in query",
			query:       `что за "стек" технологий?`,
			llmResponse: "tech stack технология стек",
		},
		{
			name:        "newlines in query",
			query:       "первая строка\nвторая строка",
			llmResponse: "multiline запрос",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := &testutil.MockOpenRouterClient{}
			mockClient.On("CreateChatCompletion", mock.Anything, mock.Anything).
				Return(testutil.MockChatResponse(tt.llmResponse), nil)

			executor := agent.NewExecutor(mockClient, nil, testutil.TestLogger())
			cfg := testutil.TestConfig()
			cfg.Agents.Enricher.Model = "test-model"
			translator := testutil.TestTranslator(t)

			enricher := New(executor, translator, cfg)

			req := &agent.Request{
				Query: tt.query,
				Shared: &agent.SharedContext{
					UserID:       123,
					Profile:      "<user_profile>\n</user_profile>",
					RecentTopics: "<recent_topics>\n</recent_topics>",
				},
			}

			resp, err := enricher.Execute(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, tt.llmResponse, resp.Content)

			mockClient.AssertExpectations(t)
		})
	}
}

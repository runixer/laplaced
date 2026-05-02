package reranker

import (
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
)

// testConfig returns a config with reranker-specific test defaults.
func testConfig() *config.Config {
	return &config.Config{
		Bot: config.BotConfig{
			Language: "en",
		},
		Agents: config.AgentsConfig{
			Default: config.AgentConfig{
				Name:  "DefaultAgent",
				Model: "test-model",
			},
			Reranker: config.RerankerAgentConfig{
				AgentConfig: config.AgentConfig{
					Name:  "Reranker",
					Model: "test-reranker-model",
				},
				Enabled:       true,
				Timeout:       "20s",
				TurnTimeout:   "5s",
				MaxToolCalls:  3,
				ThinkingLevel: "minimal",
				Topics: config.RerankerTypeConfig{
					CandidatesLimit: 20,
					Max:             5,
				},
				People: config.RerankerTypeConfig{
					CandidatesLimit: 20,
					Max:             3,
				},
				Artifacts: config.RerankerArtifactsConfig{
					RerankerTypeConfig: config.RerankerTypeConfig{
						CandidatesLimit: 20,
						Max:             3,
					},
				},
			},
		},
	}
}

// testConfigDisabled returns a config with reranker disabled.
func testConfigDisabled() *config.Config {
	cfg := testConfig()
	cfg.Agents.Reranker.Enabled = false
	return cfg
}

// makeChatResponse creates a simple text response from the LLM.
func makeChatResponse(content string) openrouter.ChatCompletionResponse {
	return openrouter.ChatCompletionResponse{
		Choices: []openrouter.ResponseChoice{
			{
				Message: openrouter.ResponseMessage{
					Role:    "assistant",
					Content: content,
				},
			},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}
}

// makeToolCallResponse creates a response with a tool call.
func makeToolCallResponse(toolName, arguments string) openrouter.ChatCompletionResponse {
	return openrouter.ChatCompletionResponse{
		Choices: []openrouter.ResponseChoice{
			{
				Message: openrouter.ResponseMessage{
					Role: "assistant",
					ToolCalls: []openrouter.ToolCall{
						{
							ID:   "call_123",
							Type: "function",
							Function: struct {
								Name      string `json:"name"`
								Arguments string `json:"arguments"`
							}{
								Name:      toolName,
								Arguments: arguments,
							},
						},
					},
				},
			},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}
}

// makeFinalJSONResponse creates a response with final JSON output.
func makeFinalJSONResponse(jsonContent string) openrouter.ChatCompletionResponse {
	return makeChatResponse(jsonContent)
}

// makeEmptyResponse creates an empty response (no choices).
func makeEmptyResponse() openrouter.ChatCompletionResponse {
	return openrouter.ChatCompletionResponse{
		Choices: []openrouter.ResponseChoice{},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{
			PromptTokens:     100,
			CompletionTokens: 0,
			TotalTokens:      100,
		},
	}
}

// mockMessagesForTopic returns mock messages for a given topic ID.
func mockMessagesForTopic(topicID int64) []storage.Message {
	now := time.Now()
	return []storage.Message{
		{
			ID:        topicID*100 + 1,
			Role:      "user",
			Content:   "User message for topic",
			CreatedAt: now.Add(-10 * time.Minute),
		},
		{
			ID:        topicID*100 + 2,
			Role:      "assistant",
			Content:   "Assistant response for topic",
			CreatedAt: now.Add(-9 * time.Minute),
		},
	}
}

// mockTopic returns a mock topic for testing.
func mockTopic(id int64, summary string) storage.Topic {
	return storage.Topic{
		ID:        id,
		Summary:   summary,
		CreatedAt: time.Now().Add(-24 * time.Hour),
	}
}

// mockPerson returns a mock person for testing.
func mockPerson(id int64, name, username string) storage.Person {
	return storage.Person{
		ID:          id,
		DisplayName: name,
		Username:    &username,
		Circle:      "Test",
		Bio:         "Test bio for " + name,
	}
}

// mockArtifactCandidate returns a mock artifact candidate for testing.
func mockArtifactCandidate(id int64, fileType, name string) ArtifactCandidate {
	return ArtifactCandidate{
		ArtifactID:   id,
		Score:        0.9 - float32(id)*0.1,
		FileType:     fileType,
		OriginalName: name,
		Summary:      "Summary for " + name,
		Keywords:     []string{"keyword1", "keyword2"},
		Entities:     []string{"Entity1", "Entity2"},
		RAGHints:     []string{"What is this about?"},
	}
}

package laplace

import (
	"github.com/runixer/laplaced/internal/openrouter"
)

// Test helpers for creating mock OpenRouter responses in tests.
// These helpers reduce boilerplate in test files.

// HelperOption allows customizing mock responses.
type HelperOption func(*openrouter.ChatCompletionResponse)

// WithID sets the response ID.
func WithID(id string) HelperOption {
	return func(r *openrouter.ChatCompletionResponse) {
		r.ID = id
	}
}

// WithModel sets the model name.
func WithModel(model string) HelperOption {
	return func(r *openrouter.ChatCompletionResponse) {
		r.Model = model
	}
}

// WithTokens sets token counts.
func WithTokens(prompt, completion, total int) HelperOption {
	return func(r *openrouter.ChatCompletionResponse) {
		r.Usage.PromptTokens = prompt
		r.Usage.CompletionTokens = completion
		r.Usage.TotalTokens = total
	}
}

// newChoice creates a new choice with the given role and content.
func newChoice(role, content string) openrouter.ResponseChoice {
	return openrouter.ResponseChoice{
		Index: 0,
		Message: openrouter.ResponseMessage{
			Role:    role,
			Content: content,
		},
		FinishReason: "stop",
	}
}

// makeChatResponse creates a simple chat completion response for testing.
func makeChatResponse(content string, opts ...HelperOption) openrouter.ChatCompletionResponse {
	resp := openrouter.ChatCompletionResponse{
		ID:      "test-response-id",
		Model:   "test-model",
		Choices: []openrouter.ResponseChoice{newChoice("assistant", content)},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}
	for _, opt := range opts {
		opt(&resp)
	}
	return resp
}

// makeToolCallResponse creates a response with a tool call for testing.
func makeToolCallResponse(toolName, args string, opts ...HelperOption) openrouter.ChatCompletionResponse {
	choice := newChoice("assistant", "")
	choice.Message.ToolCalls = []openrouter.ToolCall{
		{
			ID:   "test-call-id",
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{
				Name:      toolName,
				Arguments: args,
			},
		},
	}
	choice.FinishReason = "tool_calls"

	resp := openrouter.ChatCompletionResponse{
		ID:      "test-response-id",
		Model:   "test-model",
		Choices: []openrouter.ResponseChoice{choice},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}
	for _, opt := range opts {
		opt(&resp)
	}
	return resp
}

// makeEmptyResponse creates an empty response for testing retry logic.
func makeEmptyResponse(opts ...HelperOption) openrouter.ChatCompletionResponse {
	resp := openrouter.ChatCompletionResponse{
		ID:    "test-response-id",
		Model: "test-model",
		Choices: []openrouter.ResponseChoice{
			{
				Index: 0,
				Message: openrouter.ResponseMessage{
					Role:    "assistant",
					Content: "",
				},
				FinishReason: "stop",
			},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{
			PromptTokens:     10,
			CompletionTokens: 0,
			TotalTokens:      10,
		},
	}
	for _, opt := range opts {
		opt(&resp)
	}
	return resp
}

// makeToolCallWithContentResponse creates a response with both content and a tool call.
// This happens when LLM provides intermediate text before calling a tool.
func makeToolCallWithContentResponse(content, toolName, args string, toolCallID string, opts ...HelperOption) openrouter.ChatCompletionResponse {
	choice := newChoice("assistant", content)
	choice.Message.ToolCalls = []openrouter.ToolCall{
		{
			ID:   toolCallID,
			Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{
				Name:      toolName,
				Arguments: args,
			},
		},
	}
	choice.FinishReason = "tool_calls"

	resp := openrouter.ChatCompletionResponse{
		ID:      "test-response-id",
		Model:   "test-model",
		Choices: []openrouter.ResponseChoice{choice},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}
	for _, opt := range opts {
		opt(&resp)
	}
	return resp
}

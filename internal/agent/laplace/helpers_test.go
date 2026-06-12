package laplace

import (
	"github.com/runixer/laplaced/internal/llm"
)

// Test helpers for creating mock LLM responses in tests.
// These helpers reduce boilerplate in test files.

// HelperOption allows customizing mock responses.
type HelperOption func(*llm.ChatCompletionResponse)

// WithID sets the response ID.
func WithID(id string) HelperOption {
	return func(r *llm.ChatCompletionResponse) {
		r.ID = id
	}
}

// WithModel sets the model name.
func WithModel(model string) HelperOption {
	return func(r *llm.ChatCompletionResponse) {
		r.Model = model
	}
}

// WithTokens sets token counts.
func WithTokens(prompt, completion, total int) HelperOption {
	return func(r *llm.ChatCompletionResponse) {
		r.Usage.PromptTokens = prompt
		r.Usage.CompletionTokens = completion
		r.Usage.TotalTokens = total
	}
}

// newChoice creates a new choice with the given role and content.
func newChoice(role, content string) llm.ResponseChoice {
	return llm.ResponseChoice{
		Index: 0,
		Message: llm.ResponseMessage{
			Role:    role,
			Content: content,
		},
		FinishReason: "stop",
	}
}

// makeChatResponse creates a simple chat completion response for testing.
func makeChatResponse(content string, opts ...HelperOption) llm.ChatCompletionResponse {
	resp := llm.ChatCompletionResponse{
		ID:      "test-response-id",
		Model:   "test-model",
		Choices: []llm.ResponseChoice{newChoice("assistant", content)},
		Usage: llm.Usage{
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
func makeToolCallResponse(toolName, args string, opts ...HelperOption) llm.ChatCompletionResponse {
	choice := newChoice("assistant", "")
	choice.Message.ToolCalls = []llm.ToolCall{
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

	resp := llm.ChatCompletionResponse{
		ID:      "test-response-id",
		Model:   "test-model",
		Choices: []llm.ResponseChoice{choice},
		Usage: llm.Usage{
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
func makeEmptyResponse(opts ...HelperOption) llm.ChatCompletionResponse {
	resp := llm.ChatCompletionResponse{
		ID:    "test-response-id",
		Model: "test-model",
		Choices: []llm.ResponseChoice{
			{
				Index: 0,
				Message: llm.ResponseMessage{
					Role:    "assistant",
					Content: "",
				},
				FinishReason: "stop",
			},
		},
		Usage: llm.Usage{
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
func makeToolCallWithContentResponse(content, toolName, args string, toolCallID string, opts ...HelperOption) llm.ChatCompletionResponse {
	choice := newChoice("assistant", content)
	choice.Message.ToolCalls = []llm.ToolCall{
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

	resp := llm.ChatCompletionResponse{
		ID:      "test-response-id",
		Model:   "test-model",
		Choices: []llm.ResponseChoice{choice},
		Usage: llm.Usage{
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

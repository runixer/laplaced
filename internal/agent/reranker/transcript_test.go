package reranker

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/runixer/laplaced/internal/llm"
)

func TestFlattenToolTranscript(t *testing.T) {
	requested := func(topics string) string { return "Requested topics: " + topics }
	call := func(id, args string) llm.ToolCall { return makeToolCall(id, "get_topics_content", args) }
	media := []any{llm.TextPart{Type: "text", Text: "prompt"}}

	tests := []struct {
		name  string
		input []llm.Message
		want  []llm.Message
	}{
		{
			name:  "no tool rounds stays unchanged",
			input: []llm.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: media}},
			want:  []llm.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: media}},
		},
		{
			name: "rounds become plain conversation",
			input: []llm.Message{
				{Role: "system", Content: "sys"},
				{Role: "user", Content: media},
				{Role: "assistant", ToolCalls: []llm.ToolCall{call("a", `{"topic_ids":[1,"Topic:2"]}`)}, ReasoningDetails: "r"},
				{Role: "tool", ToolCallID: "a", Content: "=== Topic 1 ==="},
				{Role: "assistant", Content: "Looking deeper.", ToolCalls: []llm.ToolCall{call("b", `{"topic_ids":[3]}`)}},
				{Role: "tool", ToolCallID: "b", Content: "=== Topic 3 ==="},
			},
			want: []llm.Message{
				{Role: "system", Content: "sys"},
				{Role: "user", Content: media},
				{Role: "assistant", Content: "Requested topics: Topic:1, Topic:2"},
				{Role: "user", Content: "Content:\n\n=== Topic 1 ==="},
				{Role: "assistant", Content: "Looking deeper.\nRequested topics: Topic:3"},
				{Role: "user", Content: "Content:\n\n=== Topic 3 ==="},
			},
		},
		{
			name: "several results of one round merge, bad arguments stay visible",
			input: []llm.Message{
				{Role: "user", Content: "prompt"},
				{Role: "assistant", ToolCalls: []llm.ToolCall{call("a", `{"topic_ids":[1]}`), call("b", `{"topic_ids":"x"}`)}},
				{Role: "tool", ToolCallID: "a", Content: "=== Topic 1 ==="},
				{Role: "tool", ToolCallID: "b", Content: "Error: bad ids"},
			},
			want: []llm.Message{
				{Role: "user", Content: "prompt"},
				{Role: "assistant", Content: `Requested topics: Topic:1; {"topic_ids":"x"}`},
				{Role: "user", Content: "Content:\n\n=== Topic 1 ===\n\nContent:\n\nError: bad ids"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, flattenToolTranscript(tt.input, requested, "Content:"))
		})
	}
}

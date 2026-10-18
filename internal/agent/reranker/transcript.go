package reranker

import (
	"strconv"
	"strings"

	"github.com/runixer/laplaced/internal/llm"
)

// flattenToolTranscript rewrites the tool-calling part of a reranker
// transcript as plain conversation, for a request that offers no tools.
//
// Withholding tools is not enough on its own: with get_topics_content calls
// and their results still in the history, Gemini (via OpenRouter) keeps
// acting as if the tool session were open. It calls the tool anyway, which
// fails the turn as a protocol violation, or answers the user's question in
// prose despite JSON mode, which fails it as a parse error. Replayed against
// production turns that failed this way, the flattened transcript returned a
// valid selection every time, with the same choices on turns that already
// worked.
//
// Each assistant tool call becomes an assistant line naming the requested
// topics, and each tool result becomes a user message carrying the loaded
// content. Consecutive text-only user messages are merged, so several results
// of one round read as one message.
func flattenToolTranscript(messages []llm.Message, requestedLine func(topics string) string, resultHeader string) []llm.Message {
	flat := make([]llm.Message, 0, len(messages))
	for _, m := range messages {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			var requests []string
			for _, tc := range m.ToolCalls {
				requests = append(requests, describeToolRequest(tc))
			}
			content := strings.TrimSpace(textContent(m.Content))
			line := requestedLine(strings.Join(requests, "; "))
			if content != "" {
				line = content + "\n" + line
			}
			flat = append(flat, llm.Message{Role: "assistant", Content: line})
		case m.Role == "tool":
			text := resultHeader + "\n\n" + textContent(m.Content)
			if last := len(flat) - 1; last >= 0 && flat[last].Role == "user" {
				if prev, ok := flat[last].Content.(string); ok {
					flat[last].Content = prev + "\n\n" + text
					continue
				}
			}
			flat = append(flat, llm.Message{Role: "user", Content: text})
		default:
			flat = append(flat, m)
		}
	}
	return flat
}

// describeToolRequest lists the topic IDs of one get_topics_content call, or
// falls back to its raw arguments when they do not parse.
func describeToolRequest(tc llm.ToolCall) string {
	ids, err := parseToolCallIDs(tc.Function.Arguments)
	if err != nil || len(ids) == 0 {
		return tc.Function.Arguments
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = "Topic:" + strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ", ")
}

func textContent(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	return ""
}

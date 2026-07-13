package llm

import "unicode/utf8"

// EstimateTextTokens returns a model-agnostic text-only estimate using four
// Unicode code points per token. It intentionally excludes media tokenization.
func EstimateTextTokens(text string) int {
	chars := utf8.RuneCountInString(text)
	return (chars + 3) / 4
}

// EstimateMessageTokens estimates textual content in one chat message.
func EstimateMessageTokens(message Message) int {
	switch content := message.Content.(type) {
	case string:
		return EstimateTextTokens(content)
	case []interface{}:
		total := 0
		for _, part := range content {
			switch typed := part.(type) {
			case TextPart:
				total += EstimateTextTokens(typed.Text)
			case map[string]interface{}:
				if text, ok := typed["text"].(string); ok {
					total += EstimateTextTokens(text)
				}
			}
		}
		return total
	default:
		return 0
	}
}

// EstimateMessagesTokens estimates textual content across chat messages.
func EstimateMessagesTokens(messages []Message) int {
	total := 0
	for _, message := range messages {
		total += EstimateMessageTokens(message)
	}
	return total
}

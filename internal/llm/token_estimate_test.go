package llm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEstimateTextTokens(t *testing.T) {
	require.Equal(t, 0, EstimateTextTokens(""))
	require.Equal(t, 1, EstimateTextTokens("abc"))
	require.Equal(t, 2, EstimateTextTokens("abcde"))
	require.Equal(t, 1, EstimateTextTokens("Прив"))
}

func TestEstimateMessageTokens(t *testing.T) {
	message := Message{Content: []interface{}{
		TextPart{Type: "text", Text: "1234"},
		FilePart{Type: "file", File: File{FileData: "large-base64-data"}},
		map[string]interface{}{"type": "text", "text": "5678"},
	}}
	require.Equal(t, 2, EstimateMessageTokens(message))
	require.Equal(t, 3, EstimateMessagesTokens([]Message{message, {Content: "abcd"}}))
}

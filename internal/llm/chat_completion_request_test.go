package llm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatCompletionRequestSerializesSamplingOptions(t *testing.T) {
	temperature := 0.0
	data, err := json.Marshal(ChatCompletionRequest{
		Model:       "judge",
		Messages:    []Message{{Role: "user", Content: "test"}},
		N:           1,
		Temperature: &temperature,
		MaxTokens:   10,
	})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(data, &body))
	require.Equal(t, float64(1), body["n"])
	require.Equal(t, float64(0), body["temperature"])
	require.Equal(t, float64(10), body["max_tokens"])
}

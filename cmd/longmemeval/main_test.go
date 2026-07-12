package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOptionsChatThinking(t *testing.T) {
	opts, err := parseOptions([]string{"--dataset", "test.json", "--chat-base-url", "https://example.com", "--chat-model", "qwen", "--chat-thinking"})
	require.NoError(t, err)
	require.True(t, opts.chatThinking)
}

func TestParseOptionsChatThinkingRequiresEndpoint(t *testing.T) {
	_, err := parseOptions([]string{"--dataset", "test.json", "--chat-thinking"})
	require.ErrorContains(t, err, "requires --chat-base-url")
}

func TestParseOptionsRequiresCompleteChatOverride(t *testing.T) {
	_, err := parseOptions([]string{"--dataset", "test.json", "--chat-model", "qwen"})
	require.ErrorContains(t, err, "must be used together")
}

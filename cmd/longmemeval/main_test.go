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

func TestParseOptionsJudge(t *testing.T) {
	opts, err := parseOptions([]string{"--dataset", "test.json", "--judge"})
	require.NoError(t, err)
	require.True(t, opts.judge)
	require.Equal(t, defaultJudgeModel, opts.judgeModel)
}

func TestParseOptionsJudgeRejectsEmptyModel(t *testing.T) {
	_, err := parseOptions([]string{"--dataset", "test.json", "--judge", "--judge-model", ""})
	require.ErrorContains(t, err, "cannot be empty")
}

func TestParseOptionsOfflineReport(t *testing.T) {
	opts, err := parseOptions([]string{"--report-input", "results.jsonl", "--report-format", "markdown"})
	require.NoError(t, err)
	require.Empty(t, opts.dataset)
	require.Equal(t, "markdown", opts.reportFormat)

	_, err = parseOptions([]string{"--compare-baseline", "before.jsonl"})
	require.ErrorContains(t, err, "must be used together")
}

func TestParseOptionsCacheDir(t *testing.T) {
	opts, err := parseOptions([]string{"--dataset", "test.json", "--cache-dir", "data/cache"})
	require.NoError(t, err)
	require.Equal(t, "data/cache", opts.cacheDir)
}

package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAgentType_String(t *testing.T) {
	tests := []struct {
		agentType AgentType
		expected  string
	}{
		{TypeLaplace, "laplace"},
		{TypeReranker, "reranker"},
		{TypeEnricher, "enricher"},
		{TypeSplitter, "splitter"},
		{TypeMerger, "merger"},
		{TypeArchivist, "archivist"},
		{TypeDeduplicator, "deduplicator"},
		{TypeScout, "scout"},
	}

	for _, tt := range tests {
		t.Run(string(tt.agentType), func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.agentType.String())
		})
	}
}

func TestTokenUsage_TotalTokens(t *testing.T) {
	tests := []struct {
		name     string
		usage    TokenUsage
		expected int
	}{
		{
			name: "uses Total if set",
			usage: TokenUsage{
				Prompt:     10,
				Completion: 20,
				Total:      35, // Different from sum
			},
			expected: 35,
		},
		{
			name: "computes sum if Total is zero",
			usage: TokenUsage{
				Prompt:     10,
				Completion: 20,
				Total:      0,
			},
			expected: 30,
		},
		{
			name:     "handles zero values",
			usage:    TokenUsage{},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.usage.TotalTokens())
		})
	}
}

func TestMessage(t *testing.T) {
	msg := Message{
		Role:    "user",
		Content: "Hello, world!",
	}

	assert.Equal(t, "user", msg.Role)
	assert.Equal(t, "Hello, world!", msg.Content)
}

func TestMediaPart(t *testing.T) {
	part := MediaPart{
		Type:     "image",
		MimeType: "image/png",
		Data:     []byte{0x89, 0x50, 0x4E, 0x47},
	}

	assert.Equal(t, "image", part.Type)
	assert.Equal(t, "image/png", part.MimeType)
	assert.Len(t, part.Data, 4)
}

func TestCapabilities(t *testing.T) {
	caps := Capabilities{
		IsAgentic:         true,
		SupportsStreaming: false,
		SupportedMedia:    []string{"image", "audio"},
		MaxInputTokens:    128000,
		OutputFormat:      "json",
	}

	assert.True(t, caps.IsAgentic)
	assert.False(t, caps.SupportsStreaming)
	assert.Contains(t, caps.SupportedMedia, "image")
	assert.Equal(t, 128000, caps.MaxInputTokens)
}

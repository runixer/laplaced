package laplace

import (
	"context"
	"log/slog"
	"testing"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	cfg := &config.Config{
		Bot: config.BotConfig{
			Language: "en",
		},
		Artifacts: config.ArtifactsConfig{
			Enabled:     true,
			StoragePath: "/tmp/test",
		},
		Tools: []config.ToolConfig{
			{Name: "test_tool", Description: "A test tool"},
		},
	}

	translator, err := i18n.NewTranslator("en")
	require.NoError(t, err)

	agent := New(
		cfg,
		nil, // orClient
		nil, // ragService
		nil, // msgRepo
		nil, // factRepo
		nil, // artifactRepo
		translator,
		slog.New(&testHandler{}), // logger
	)

	assert.NotNil(t, agent)
	assert.Equal(t, cfg, agent.cfg)
	assert.Equal(t, translator, agent.translator)
	assert.Equal(t, "/tmp/test", agent.storagePath)
	assert.NotNil(t, agent.tools)
	assert.Len(t, agent.tools, 1) // BuildTools should create tools from config
}

func TestLaplace_Type(t *testing.T) {
	agent := &Laplace{}
	assert.Equal(t, "laplace", string(agent.Type()))
}

func TestLaplace_SetAgentLogger(t *testing.T) {
	agent := &Laplace{}
	assert.Nil(t, agent.agentLogger)

	// SetAgentLogger just assigns, no error to check
	// This is a simple smoke test
	agent.SetAgentLogger(nil)
	assert.Nil(t, agent.agentLogger)
}

func TestFormatMessagesForLog(t *testing.T) {
	tests := []struct {
		name     string
		messages []openrouter.Message
		expected string
	}{
		{
			name:     "empty messages",
			messages: []openrouter.Message{},
			expected: "",
		},
		{
			name: "single text message",
			messages: []openrouter.Message{
				{Role: "user", Content: "Hello world"},
			},
			expected: "=== USER ===\nHello world",
		},
		{
			name: "multiple messages",
			messages: []openrouter.Message{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi there!"},
			},
			expected: `=== USER ===
Hello

=== ASSISTANT ===
Hi there!`,
		},
		{
			name: "message with structured content (text parts)",
			messages: []openrouter.Message{
				{
					Role: "user",
					Content: []interface{}{
						openrouter.TextPart{Type: "text", Text: "Structured message"},
					},
				},
			},
			expected: "=== USER ===\nStructured message",
		},
		{
			name: "message with map content (legacy format)",
			messages: []openrouter.Message{
				{
					Role: "user",
					Content: []interface{}{
						map[string]interface{}{"type": "text", "text": "Map message"},
					},
				},
			},
			expected: "=== USER ===\nMap message",
		},
		{
			name: "message with image",
			messages: []openrouter.Message{
				{
					Role: "user",
					Content: []interface{}{
						openrouter.TextPart{Type: "text", Text: "Look at this"},
						map[string]interface{}{"type": "image_url", "url": "http://example.com/img.jpg"},
					},
				},
			},
			expected: "=== USER ===\nLook at this[IMAGE]",
		},
		{
			name: "message with audio",
			messages: []openrouter.Message{
				{
					Role: "user",
					Content: []interface{}{
						map[string]interface{}{"type": "input_audio", "data": "base64..."},
					},
				},
			},
			expected: "=== USER ===\n[AUDIO]",
		},
		{
			name: "message with tool call",
			messages: []openrouter.Message{
				{
					Role:    "assistant",
					Content: "Let me search",
					ToolCalls: []openrouter.ToolCall{
						{ID: "call_1", Function: toolFunc("search_web", `{"query": "test"}`)},
					},
				},
			},
			expected: `=== ASSISTANT ===
Let me search

[TOOL CALLS]
- search_web({"query": "test"})`,
		},
		{
			name: "message with nil content (tool-only response)",
			messages: []openrouter.Message{
				{
					Role:    "assistant",
					Content: nil,
					ToolCalls: []openrouter.ToolCall{
						{ID: "call_1", Function: toolFunc("test", "")},
					},
				},
			},
			expected: `=== ASSISTANT ===


[TOOL CALLS]
- test()`,
		},
		{
			name: "multiple tool calls",
			messages: []openrouter.Message{
				{
					Role:    "assistant",
					Content: "",
					ToolCalls: []openrouter.ToolCall{
						{Function: toolFunc("search_web", `{"q":"a"}`)},
						{Function: toolFunc("search_history", `{"q":"b"}`)},
					},
				},
			},
			expected: `=== ASSISTANT ===


[TOOL CALLS]
- search_web({"q":"a"})
- search_history({"q":"b"})`,
		},
		{
			name: "mixed content types",
			messages: []openrouter.Message{
				{Role: "system", Content: "You are a bot"},
				{Role: "user", Content: []interface{}{
					openrouter.TextPart{Type: "text", Text: "Text with"},
					map[string]interface{}{"type": "image_url"},
				}},
				{Role: "assistant", Content: "Response"},
			},
			expected: `=== SYSTEM ===
You are a bot

=== USER ===
Text with[IMAGE]

=== ASSISTANT ===
Response`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatMessagesForLog(tt.messages)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// testHandler is a minimal slog.Handler for testing
type testHandler struct{}

func (h *testHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *testHandler) Handle(ctx context.Context, r slog.Record) error {
	return nil
}

func (h *testHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *testHandler) WithGroup(name string) slog.Handler {
	return h
}

// toolFunc creates a ToolCall.Function for testing
func toolFunc(name, args string) struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
} {
	return struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: name, Arguments: args}
}

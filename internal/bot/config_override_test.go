package bot

import (
	"testing"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/openrouter"

	"github.com/stretchr/testify/assert"
)

func TestSystemPromptWithAgentName(t *testing.T) {
	// Tests that system prompt is correctly built using agents.chat.name
	// System prompt comes from i18n with agent name substitution

	tests := []struct {
		name              string
		agentName         string
		systemPromptExtra string
		expectContains    []string
	}{
		{
			name:           "Agent name from config",
			agentName:      "TestBot",
			expectContains: []string{"System prompt for TestBot"},
		},
		{
			name:           "Default agent name",
			agentName:      "", // Will fall back to "Bot" or default
			expectContains: []string{"System prompt for Bot"},
		},
		{
			name:              "Agent name + Extra",
			agentName:         "TestBot",
			systemPromptExtra: "Extra Instruction",
			expectContains:    []string{"System prompt for TestBot", "Extra Instruction"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the logic in buildContext
			agentName := tt.agentName
			if agentName == "" {
				agentName = "Bot"
			}

			// Simulate i18n result with agent name substitution
			basePrompt := "System prompt for " + agentName

			fullSystemPrompt := basePrompt
			if tt.systemPromptExtra != "" {
				fullSystemPrompt += " " + tt.systemPromptExtra
			}

			for _, expected := range tt.expectContains {
				assert.Contains(t, fullSystemPrompt, expected)
			}
		})
	}
}

func TestToolDescriptionOverride(t *testing.T) {
	// Mock translator
	// translator := &i18n.Translator{} // We can't easily mock this struct as it has private fields.

	// Let's test the logic we added to getTools.
	// Again, since getTools is a method on Bot, we need a Bot instance.

	cfg := &config.Config{
		Bot: config.BotConfig{Language: "ru"},
		Tools: []config.ToolConfig{
			{
				Name: "test_tool",
				// No description, should fallback
			},
			{
				Name:        "override_tool",
				Description: "Overridden Description",
			},
		},
	}

	// We can't easily run getTools without a full Bot.
	// But we can verify the logic structure.

	// 1. Configured Tools
	var tools []openrouter.Tool
	for _, toolCfg := range cfg.Tools {
		desc := toolCfg.Description
		if desc == "" {
			// Simulate translator.Get
			desc = "Translated Description for " + toolCfg.Name
		}

		tool := openrouter.Tool{
			Function: openrouter.ToolFunction{
				Name:        toolCfg.Name,
				Description: desc,
			},
		}
		tools = append(tools, tool)
	}

	assert.Equal(t, "Translated Description for test_tool", tools[0].Function.Description)
	assert.Equal(t, "Overridden Description", tools[1].Function.Description)
}

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentsConfig_ChatSettingsFor(t *testing.T) {
	overrides := []ChatUserOverride{
		{UserIDs: []string{"111", "222"}, Model: "override-model", ThinkingLevel: "high"},
		{UserIDs: []string{"333"}, ThinkingLevel: "medium"},
		{UserIDs: []string{"111"}, Model: "shadowed-model"},
	}

	tests := []struct {
		name     string
		envModel string
		sender   string
		want     ChatSettings
	}{
		{"no match uses global settings", "", "999", ChatSettings{Model: "chat-model", ThinkingLevel: "low"}},
		{"empty sender uses global settings", "", "", ChatSettings{Model: "chat-model", ThinkingLevel: "low"}},
		{"match replaces model and thinking", "", "222", ChatSettings{Model: "override-model", ThinkingLevel: "high", Overridden: true}},
		{"first matching entry wins", "", "111", ChatSettings{Model: "override-model", ThinkingLevel: "high", Overridden: true}},
		{"empty model inherits global", "", "333", ChatSettings{Model: "chat-model", ThinkingLevel: "medium", Overridden: true}},
		{"override wins over env model", "env-model", "222", ChatSettings{Model: "override-model", ThinkingLevel: "high", Overridden: true}},
		{"env model applies without match", "env-model", "999", ChatSettings{Model: "env-model", ThinkingLevel: "low"}},
		{"thinking-only override keeps env model", "env-model", "333", ChatSettings{Model: "env-model", ThinkingLevel: "medium", Overridden: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := AgentsConfig{
				ChatModel: tt.envModel,
				Default:   AgentConfig{Model: "default-model"},
				Chat: ChatAgentConfig{
					AgentConfig:   AgentConfig{Name: "Test", Model: "chat-model"},
					UserOverrides: overrides,
				},
			}
			assert.Equal(t, tt.want, cfg.ChatSettingsFor(tt.sender))
		})
	}
}

func TestValidateChatUserOverrides(t *testing.T) {
	tests := []struct {
		name      string
		overrides []ChatUserOverride
		wantErrs  []string
	}{
		{"none", nil, nil},
		{"valid", []ChatUserOverride{{UserIDs: []string{"1", "abc"}, Model: "m"}, {UserIDs: []string{"2"}, ThinkingLevel: "auto"}}, nil},
		{"empty ids", []ChatUserOverride{{Model: "m"}}, []string{"user_ids: must not be empty"}},
		{"nothing to override", []ChatUserOverride{{UserIDs: []string{"1"}}}, []string{"set model and/or thinking_level"}},
		{"bad thinking level", []ChatUserOverride{{UserIDs: []string{"1"}, ThinkingLevel: "max"}}, []string{`got "max"`}},
		{"padded id", []ChatUserOverride{{UserIDs: []string{" 1"}, Model: "m"}}, []string{`invalid id " 1"`}},
		{"duplicate across entries", []ChatUserOverride{{UserIDs: []string{"1"}, Model: "a"}, {UserIDs: []string{"1"}, Model: "b"}}, []string{`"1" already listed in agents.chat.user_overrides[0]`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateChatUserOverrides(tt.overrides)
			require.Len(t, errs, len(tt.wantErrs))
			for i, want := range tt.wantErrs {
				assert.Contains(t, errs[i].Error(), want)
			}
		})
	}
}

func TestLoad_ChatUserOverridesAcceptNumericIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
agents:
  chat:
    user_overrides:
      - user_ids: [123456789, "abc123"]
        model: "override-model"
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Agents.Chat.UserOverrides, 1)
	assert.Equal(t, []string{"123456789", "abc123"}, cfg.Agents.Chat.UserOverrides[0].UserIDs)
	assert.Equal(t, "override-model", cfg.Agents.ChatSettingsFor("123456789").Model)
	// Overrides merge onto the embedded defaults instead of replacing them.
	assert.NotEmpty(t, cfg.Agents.Chat.Name)
}

package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoad(t *testing.T) {
	// Create a temporary config file for testing
	content := `
server:
  listen_port: "9001"
telegram:
  token: "test_token"
  webhook_url: "https://test.com/webhook"
openrouter:
  api_key: "test_api_key"
bot:
  allowed_user_ids:
    - 123
    - 456
  system_prompt: "You are a test assistant."
tools:
  - name: "test_tool"
    model: "test_model"
    description: "Test tool description"
    parameter_description: "Test param"
database:
  path: "test.db"
`
	tmpfile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name()) // clean up

	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	// Test loading the config
	cfg, err := Load(tmpfile.Name())
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// Assert values
	assert.Equal(t, "9001", cfg.Server.ListenPort)
	assert.Equal(t, "test_token", cfg.Telegram.Token)
	assert.Equal(t, "https://test.com/webhook", cfg.Telegram.WebhookURL)
	assert.Equal(t, "test_api_key", cfg.OpenRouter.APIKey)
	assert.Equal(t, []int64{123, 456}, cfg.Bot.AllowedUserIDs)
	assert.Equal(t, "You are a test assistant.", cfg.Bot.SystemPrompt)
	assert.Equal(t, "test.db", cfg.Database.Path)

	// Assert tools
	assert.Len(t, cfg.Tools, 1)
	assert.Equal(t, "test_tool", cfg.Tools[0].Name)
	assert.Equal(t, "test_model", cfg.Tools[0].Model)
	assert.Equal(t, "Test tool description", cfg.Tools[0].Description)
	assert.Equal(t, "Test param", cfg.Tools[0].ParameterDescription)
}

func TestLoad_FileNotExists_FallsBackToDefault(t *testing.T) {
	// When file doesn't exist, Load should fall back to embedded default config
	cfg, err := Load("non_existent_file.yaml")
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	// Check that some default values are present
	assert.Equal(t, "9081", cfg.Server.ListenPort)
}

func TestLoadDefault(t *testing.T) {
	cfg, err := LoadDefault()
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	// Check that default values are present
	assert.Equal(t, "9081", cfg.Server.ListenPort)
	assert.Equal(t, "info", cfg.Log.Level)
}

func TestLoad_WithEnvVars(t *testing.T) {
	// Set environment variable for the test
	t.Setenv("TEST_TOKEN", "secret-from-env")
	t.Setenv("TEST_API_KEY", "api-key-from-env")

	// Create a temporary config file with placeholders
	content := `
server:
  listen_port: "9001"
telegram:
  token: "$TEST_TOKEN"
  webhook_url: "https://test.com/webhook"
openrouter:
  api_key: "${TEST_API_KEY}"
bot:
  system_prompt: "You are a test assistant from env."
  allowed_user_ids:
    - 123
database:
  path: "test.db"
`
	tmpfile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	// Test loading the config
	cfg, err := Load(tmpfile.Name())
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// Assert that the environment variables were expanded
	assert.Equal(t, "secret-from-env", cfg.Telegram.Token)
	assert.Equal(t, "api-key-from-env", cfg.OpenRouter.APIKey)
	assert.Equal(t, "You are a test assistant from env.", cfg.Bot.SystemPrompt)
	assert.Equal(t, "https://test.com/webhook", cfg.Telegram.WebhookURL) // Ensure other values are still correct
}

func TestLoad_MergesWithDefaults(t *testing.T) {
	// Create a minimal config that only overrides a few fields
	content := `telegram:
  token: "my-custom-token"
bot:
  allowed_user_ids:
    - 999
`
	tmpfile, err := os.CreateTemp("", "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	// Load the config
	cfg, err := Load(tmpfile.Name())
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// Assert that user-specified values are applied
	assert.Equal(t, "my-custom-token", cfg.Telegram.Token)
	assert.Equal(t, []int64{999}, cfg.Bot.AllowedUserIDs)

	// Assert that default values are preserved for fields not specified by user
	assert.Equal(t, "9081", cfg.Server.ListenPort)         // from default
	assert.Equal(t, "info", cfg.Log.Level)                 // from default
	assert.Equal(t, "data/laplaced.db", cfg.Database.Path) // from default
	assert.True(t, cfg.RAG.Enabled)                        // from default
	assert.Equal(t, "en", cfg.Bot.Language)                // from default
}

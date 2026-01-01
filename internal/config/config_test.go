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

func TestValidate(t *testing.T) {
	// Helper to create a valid config
	validConfig := func() *Config {
		cfg, _ := LoadDefault()
		cfg.Telegram.Token = "test_token"
		cfg.OpenRouter.APIKey = "test_api_key"
		cfg.Database.Path = "test.db"
		// Default has auth.enabled=true, set password to avoid validation error
		cfg.Server.Auth.Password = "test_password"
		return cfg
	}

	tests := []struct {
		name        string
		modify      func(*Config)
		wantErr     bool
		errContains string
	}{
		{
			name:    "valid config",
			modify:  func(c *Config) {},
			wantErr: false,
		},
		{
			name:        "missing telegram token",
			modify:      func(c *Config) { c.Telegram.Token = "" },
			wantErr:     true,
			errContains: "telegram.token is required",
		},
		{
			name:        "missing openrouter api key",
			modify:      func(c *Config) { c.OpenRouter.APIKey = "" },
			wantErr:     true,
			errContains: "openrouter.api_key is required",
		},
		{
			name:        "missing database path",
			modify:      func(c *Config) { c.Database.Path = "" },
			wantErr:     true,
			errContains: "database.path is required",
		},
		{
			name: "yandex enabled without api key",
			modify: func(c *Config) {
				c.Yandex.Enabled = true
				c.Yandex.APIKey = ""
				c.Yandex.FolderID = "folder123"
			},
			wantErr:     true,
			errContains: "yandex.api_key is required",
		},
		{
			name: "yandex enabled without folder id",
			modify: func(c *Config) {
				c.Yandex.Enabled = true
				c.Yandex.APIKey = "api_key_123"
				c.Yandex.FolderID = ""
			},
			wantErr:     true,
			errContains: "yandex.folder_id is required",
		},
		{
			name: "auth enabled without username",
			modify: func(c *Config) {
				c.Server.Auth.Enabled = true
				c.Server.Auth.Username = ""
				c.Server.Auth.Password = "pass123"
			},
			wantErr:     true,
			errContains: "server.auth.username is required",
		},
		{
			name: "auth enabled without password",
			modify: func(c *Config) {
				c.Server.Auth.Enabled = true
				c.Server.Auth.Username = "user"
				c.Server.Auth.Password = ""
			},
			wantErr:     true,
			errContains: "server.auth.password is required",
		},
		{
			name: "similarity threshold out of range (negative)",
			modify: func(c *Config) {
				c.RAG.Enabled = true
				c.RAG.SimilarityThreshold = -0.1
			},
			wantErr:     true,
			errContains: "rag.similarity_threshold must be between 0 and 1",
		},
		{
			name: "similarity threshold out of range (>1)",
			modify: func(c *Config) {
				c.RAG.Enabled = true
				c.RAG.SimilarityThreshold = 1.5
			},
			wantErr:     true,
			errContains: "rag.similarity_threshold must be between 0 and 1",
		},
		{
			name: "max context messages non-positive",
			modify: func(c *Config) {
				c.RAG.Enabled = true
				c.RAG.MaxContextMessages = 0
			},
			wantErr:     true,
			errContains: "rag.max_context_messages must be positive",
		},
		{
			name: "rag disabled skips rag validation",
			modify: func(c *Config) {
				c.RAG.Enabled = false
				c.RAG.SimilarityThreshold = -1 // would fail if validated
				c.RAG.MaxContextMessages = 0   // would fail if validated
			},
			wantErr: false,
		},
		{
			name: "multiple errors collected",
			modify: func(c *Config) {
				c.Telegram.Token = ""
				c.OpenRouter.APIKey = ""
			},
			wantErr:     true,
			errContains: "telegram.token is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.modify(cfg)

			err := cfg.Validate()
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

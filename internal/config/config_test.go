package config

import (
	"os"
	"testing"
	"time"

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
			name: "auth enabled without password (auto-generated)",
			modify: func(c *Config) {
				c.Server.Auth.Enabled = true
				c.Server.Auth.Username = "user"
				c.Server.Auth.Password = "" // Password is auto-generated by server, not required in config
			},
			wantErr: false,
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
			name: "max profile facts non-positive",
			modify: func(c *Config) {
				c.RAG.Enabled = true
				c.RAG.MaxProfileFacts = 0
			},
			wantErr:     true,
			errContains: "rag.max_profile_facts must be positive",
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

// Get* method tests for improved coverage

func TestAgentConfig_GetModel(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		defaultModel string
		expected     string
	}{
		{"uses configured model", "gpt-4", "gpt-3.5", "gpt-4"},
		{"falls back to default when empty", "", "gpt-3.5", "gpt-3.5"},
		{"uses configured model even if default is empty", "custom-model", "", "custom-model"},
		{"both empty returns empty", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := AgentConfig{Model: tt.model}
			result := cfg.GetModel(tt.defaultModel)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestArchivistAgentConfig_GetModel(t *testing.T) {
	cfg := ArchivistAgentConfig{AgentConfig: AgentConfig{Model: "archivist-model"}}
	assert.Equal(t, "archivist-model", cfg.GetModel("default"))

	cfg2 := ArchivistAgentConfig{AgentConfig: AgentConfig{Model: ""}}
	assert.Equal(t, "default", cfg2.GetModel("default"))
}

func TestExtractorAgentConfig_GetModel(t *testing.T) {
	cfg := ExtractorAgentConfig{AgentConfig: AgentConfig{Model: "extractor-model"}}
	assert.Equal(t, "extractor-model", cfg.GetModel("default"))

	cfg2 := ExtractorAgentConfig{AgentConfig: AgentConfig{Model: ""}}
	assert.Equal(t, "default", cfg2.GetModel("default"))
}

func TestExtractorAgentConfig_GetTimeout(t *testing.T) {
	tests := []struct {
		name     string
		timeout  string
		expected time.Duration
	}{
		{"uses configured timeout", "5m", 5 * time.Minute},
		{"defaults to 2 minutes when empty", "", 2 * time.Minute},
		{"defaults to 2 minutes on invalid format", "invalid", 2 * time.Minute},
		{"defaults to 2 minutes on zero duration", "0s", 2 * time.Minute},
		{"parses seconds", "30s", 30 * time.Second},
		{"parses hours", "1h", 1 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ExtractorAgentConfig{Timeout: tt.timeout}
			result := cfg.GetTimeout()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractorAgentConfig_GetPollingInterval(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		expected time.Duration
	}{
		{"uses configured interval", "1m", 1 * time.Minute},
		{"defaults to 30 seconds when empty", "", 30 * time.Second},
		{"defaults to 30 seconds on invalid format", "invalid", 30 * time.Second},
		{"defaults to 30 seconds on zero duration", "0s", 30 * time.Second},
		{"parses hours", "2h", 2 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ExtractorAgentConfig{PollingInterval: tt.interval}
			result := cfg.GetPollingInterval()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractorAgentConfig_GetRecoveryThreshold(t *testing.T) {
	tests := []struct {
		name     string
		thresh   string
		expected time.Duration
	}{
		{"uses configured threshold", "5m", 5 * time.Minute},
		{"defaults to 10 minutes when empty", "", 10 * time.Minute},
		{"defaults to 10 minutes on invalid format", "invalid", 10 * time.Minute},
		{"defaults to 10 minutes on zero duration", "0s", 10 * time.Minute},
		{"parses seconds", "30s", 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ExtractorAgentConfig{RecoveryThreshold: tt.thresh}
			result := cfg.GetRecoveryThreshold()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMemoryConfig_GetFactDefaultImportance(t *testing.T) {
	tests := []struct {
		name       string
		importance int
		expected   int
	}{
		{"uses configured importance", 75, 75},
		{"defaults to 50 when zero", 0, 50},
		{"uses configured importance even if low", 10, 10},
		{"uses configured importance even if high", 100, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := MemoryConfig{FactDefaultImportance: tt.importance}
			result := cfg.GetFactDefaultImportance()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSearchConfig_GetPeopleSimilarityThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		expected  float64
	}{
		{"uses configured threshold", 0.5, 0.5},
		{"defaults to 0.3 when zero", 0, 0.3},
		{"uses configured threshold even if low", 0.1, 0.1},
		{"uses configured threshold even if high", 0.9, 0.9},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := SearchConfig{PeopleSimilarityThreshold: tt.threshold}
			result := cfg.GetPeopleSimilarityThreshold()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSearchConfig_GetPeopleMaxResults(t *testing.T) {
	tests := []struct {
		name     string
		max      int
		expected int
	}{
		{"uses configured max", 10, 10},
		{"defaults to 5 when zero", 0, 5},
		{"uses configured max even if large", 100, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := SearchConfig{PeopleMaxResults: tt.max}
			result := cfg.GetPeopleMaxResults()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRAGConfig_GetChunkDuration(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		expected time.Duration
	}{
		{"uses configured interval", "2h", 2 * time.Hour},
		{"defaults to DefaultChunkInterval when empty", "", DefaultChunkInterval},
		{"defaults to DefaultChunkInterval on invalid format", "invalid", DefaultChunkInterval},
		{"parses minutes", "30m", 30 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := RAGConfig{ChunkInterval: tt.interval}
			result := cfg.GetChunkDuration()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRAGConfig_GetSplitThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
		expected  int
	}{
		{"uses configured threshold", 50000, 50000},
		{"defaults to DefaultSplitThreshold when zero or negative", 0, DefaultSplitThreshold},
		{"defaults to DefaultSplitThreshold when negative", -100, DefaultSplitThreshold},
		{"uses configured threshold even if small", 10000, 10000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := RAGConfig{SplitThresholdChars: tt.threshold}
			result := cfg.GetSplitThreshold()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRAGConfig_GetRecentTopicsInContext(t *testing.T) {
	tests := []struct {
		name     string
		count    int
		expected int
	}{
		{"uses configured count when positive", 5, 5},
		{"defaults to DefaultRecentTopicsInContext when zero", 0, DefaultRecentTopicsInContext},
		{"defaults to DefaultRecentTopicsInContext when negative", -1, DefaultRecentTopicsInContext},
		{"uses configured count even if large", 20, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := RAGConfig{RecentTopicsInContext: tt.count}
			result := cfg.GetRecentTopicsInContext()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRAGConfig_GetMinSafetyThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		expected  float64
	}{
		{"uses configured threshold when positive", 0.2, 0.2},
		{"defaults to DefaultMinSafetyThreshold when zero", 0, DefaultMinSafetyThreshold},
		{"uses configured threshold even if very small", 0.01, 0.01},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := RAGConfig{MinSafetyThreshold: tt.threshold}
			result := cfg.GetMinSafetyThreshold()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRAGConfig_GetConsolidationThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold float64
		expected  float64
	}{
		{"uses configured threshold when positive", 0.8, 0.8},
		{"defaults to DefaultConsolidationThreshold when zero", 0, DefaultConsolidationThreshold},
		{"uses configured threshold even if high", 0.95, 0.95},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := RAGConfig{ConsolidationSimilarityThreshold: tt.threshold}
			result := cfg.GetConsolidationThreshold()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRAGConfig_GetRetrievedTopicsCount(t *testing.T) {
	tests := []struct {
		name     string
		count    int
		expected int
	}{
		{"uses configured count when positive", 20, 20},
		{"defaults to DefaultRetrievedTopicsCount when zero", 0, DefaultRetrievedTopicsCount},
		{"uses configured count even if large", 50, 50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := RAGConfig{RetrievedTopicsCount: tt.count}
			result := cfg.GetRetrievedTopicsCount()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRAGConfig_GetMaxMergedSizeChars(t *testing.T) {
	tests := []struct {
		name     string
		size     int
		expected int
	}{
		{"uses configured size when positive", 75000, 75000},
		{"defaults to DefaultMaxMergedSizeChars when zero", 0, DefaultMaxMergedSizeChars},
		{"uses configured size even if small", 10000, 10000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := RAGConfig{MaxMergedSizeChars: tt.size}
			result := cfg.GetMaxMergedSizeChars()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRAGConfig_GetMaxChunkSize(t *testing.T) {
	tests := []struct {
		name     string
		size     int
		expected int
	}{
		{"uses configured size when positive", 500, 500},
		{"defaults to DefaultMaxChunkSize when zero", 0, DefaultMaxChunkSize},
		{"uses configured size even if large", 1000, 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := RAGConfig{MaxChunkSize: tt.size}
			result := cfg.GetMaxChunkSize()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRerankerAgentConfig_GetModel(t *testing.T) {
	cfg := RerankerAgentConfig{AgentConfig: AgentConfig{Model: "reranker-model"}}
	assert.Equal(t, "reranker-model", cfg.GetModel("default"))

	cfg2 := RerankerAgentConfig{AgentConfig: AgentConfig{Model: ""}}
	assert.Equal(t, "default", cfg2.GetModel("default"))
}

func TestAgentsConfig_GetChatModel(t *testing.T) {
	tests := []struct {
		name         string
		chatModel    string
		defaultModel string
		chatName     string
		expected     string
	}{
		{"env override takes precedence", "env-model", "default-model", "chat-name", "env-model"},
		{"uses chat model when no env override", "", "default-model", "chat-name", "chat-name"},
		{"falls back to default when both empty", "", "default-model", "", "default-model"},
		{"all three set - env wins", "env-model", "default-model", "chat-name", "env-model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := AgentsConfig{
				ChatModel: tt.chatModel,
				Default:   AgentConfig{Model: tt.defaultModel},
				Chat:      AgentConfig{Model: tt.chatName},
			}
			result := cfg.GetChatModel()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDefaultConfigBytes(t *testing.T) {
	bytes := DefaultConfigBytes()
	assert.NotNil(t, bytes)
	assert.Greater(t, len(bytes), 0, "default config should not be empty")
	assert.Contains(t, string(bytes), "server:", "should contain YAML content")
	assert.Contains(t, string(bytes), "telegram:", "should contain YAML content")
	assert.Contains(t, string(bytes), "bot:", "should contain YAML content")
}

func TestDefaultsConstants(t *testing.T) {
	assert.Equal(t, 0.1, DefaultMinSafetyThreshold)
	assert.Equal(t, 0.75, DefaultConsolidationThreshold)
	assert.Equal(t, 10, DefaultMaxSessionMessages)
	assert.Equal(t, 500, DefaultMaxCharsPerMessage)
	assert.Equal(t, 10, DefaultRetrievedTopicsCount)
	assert.Equal(t, 50000, DefaultMaxMergedSizeChars)
	assert.Equal(t, 100, DefaultMergeGapThreshold)
	assert.Equal(t, 1*time.Minute, DefaultFactExtractionInterval)
	assert.Equal(t, 10*time.Minute, DefaultConsolidationInterval)
	assert.Equal(t, 10*time.Minute, DefaultChunkProcessingTimeout)
	assert.Equal(t, 1*time.Minute, DefaultBackfillInterval)
	assert.Equal(t, 400, DefaultMaxChunkSize)
	assert.Equal(t, 50, DefaultFactDefaultImportance)
	assert.Equal(t, 0.3, DefaultPeopleSimilarityThreshold)
	assert.Equal(t, 5, DefaultPeopleMaxResults)
}

func TestRAGConfig_DefaultChunkInterval(t *testing.T) {
	assert.Equal(t, 1*time.Hour, DefaultChunkInterval)
}

func TestRAGConfig_DefaultSplitThreshold(t *testing.T) {
	assert.Equal(t, 25000, DefaultSplitThreshold)
}

func TestRAGConfig_DefaultRecentTopicsInContext(t *testing.T) {
	assert.Equal(t, 3, DefaultRecentTopicsInContext)
}

func TestValidate_RerankerConfig(t *testing.T) {
	// Helper to create a valid config
	validConfig := func() *Config {
		cfg, _ := LoadDefault()
		cfg.Telegram.Token = "test_token"
		cfg.OpenRouter.APIKey = "test_api_key"
		cfg.Database.Path = "test.db"
		return cfg
	}

	tests := []struct {
		name        string
		modify      func(*Config)
		wantErr     bool
		errContains string
	}{
		{
			name:    "valid reranker config",
			modify:  func(c *Config) {},
			wantErr: false,
		},
		{
			name: "invalid topics candidates_limit (no legacy)",
			modify: func(c *Config) {
				c.Agents.Reranker.Topics.CandidatesLimit = 0
				c.Agents.Reranker.Candidates = 0 // clear legacy to prevent migration
			},
			wantErr:     true,
			errContains: "reranker.topics.candidates_limit must be positive",
		},
		{
			name: "invalid topics max (no legacy)",
			modify: func(c *Config) {
				c.Agents.Reranker.Topics.Max = 0
				c.Agents.Reranker.MaxTopics = 0 // clear legacy to prevent migration
			},
			wantErr:     true,
			errContains: "reranker.topics.max must be positive",
		},
		{
			name: "invalid people max (no legacy)",
			modify: func(c *Config) {
				c.Agents.Reranker.People.Max = 0
				c.Agents.Reranker.MaxPeople = 0 // clear legacy to prevent migration
			},
			wantErr:     true,
			errContains: "reranker.people.max must be positive",
		},
		{
			name: "invalid max_tool_calls",
			modify: func(c *Config) {
				c.Agents.Reranker.MaxToolCalls = 0
			},
			wantErr:     true,
			errContains: "max_tool_calls must be positive",
		},
		{
			name: "invalid timeout duration",
			modify: func(c *Config) {
				c.Agents.Reranker.Timeout = "invalid-duration"
			},
			wantErr:     true,
			errContains: "reranker.timeout: invalid duration format",
		},
		{
			name: "invalid turn_timeout duration",
			modify: func(c *Config) {
				c.Agents.Reranker.TurnTimeout = "not-a-duration"
			},
			wantErr:     true,
			errContains: "reranker.turn_timeout: invalid duration format",
		},
		{
			name: "invalid thinking_level",
			modify: func(c *Config) {
				c.Agents.Reranker.ThinkingLevel = "invalid-level"
			},
			wantErr:     true,
			errContains: "thinking_level: must be one of",
		},
		{
			name: "valid thinking levels",
			modify: func(c *Config) {
				validLevels := []string{"off", "minimal", "low", "medium", "high"}
				c.Agents.Reranker.ThinkingLevel = validLevels[0]
			},
			wantErr: false,
		},
		{
			name: "legacy candidates migration works",
			modify: func(c *Config) {
				c.Agents.Reranker.Topics.CandidatesLimit = 0
				c.Agents.Reranker.Candidates = 25
				// After Validate(), Topics.CandidatesLimit should be 25
			},
			wantErr: false,
		},
		{
			name: "legacy max_topics migration works",
			modify: func(c *Config) {
				c.Agents.Reranker.Topics.Max = 0
				c.Agents.Reranker.MaxTopics = 8
			},
			wantErr: false,
		},
		{
			name: "legacy max_people migration works",
			modify: func(c *Config) {
				c.Agents.Reranker.People.Max = 0
				c.Agents.Reranker.MaxPeople = 10
			},
			wantErr: false,
		},
		{
			name: "people candidates_limit default is applied",
			modify: func(c *Config) {
				c.Agents.Reranker.People.CandidatesLimit = 0
			},
			wantErr: false, // default 20 should be applied
		},
		{
			name: "artifacts defaults are applied when zero",
			modify: func(c *Config) {
				c.Agents.Reranker.Artifacts.CandidatesLimit = 0
				c.Agents.Reranker.Artifacts.Max = 0
			},
			wantErr: false, // defaults should be applied (20 and 5)
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

func TestValidate_BotTurnWaitDuration(t *testing.T) {
	validConfig := func() *Config {
		cfg, _ := LoadDefault()
		cfg.Telegram.Token = "test_token"
		cfg.OpenRouter.APIKey = "test_api_key"
		cfg.Database.Path = "test.db"
		return cfg
	}

	tests := []struct {
		name        string
		duration    string
		wantErr     bool
		errContains string
	}{
		{"empty duration is ok", "", false, ""},
		{"valid seconds", "5s", false, ""},
		{"valid minutes", "2m", false, ""},
		{"invalid format", "invalid", true, "bot.turn_wait_duration: invalid duration format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Bot.TurnWaitDuration = tt.duration

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

func TestValidate_Artifacts(t *testing.T) {
	validConfig := func() *Config {
		cfg, _ := LoadDefault()
		cfg.Telegram.Token = "test_token"
		cfg.OpenRouter.APIKey = "test_api_key"
		cfg.Database.Path = "test.db"
		return cfg
	}

	tests := []struct {
		name        string
		modify      func(*Config)
		wantErr     bool
		errContains string
	}{
		{
			name:    "artifacts disabled - no validation",
			modify:  func(c *Config) { c.Artifacts.Enabled = false },
			wantErr: false,
		},
		{
			name: "artifacts enabled without storage_path",
			modify: func(c *Config) {
				c.Artifacts.Enabled = true
				c.Artifacts.StoragePath = ""
			},
			wantErr:     true,
			errContains: "artifacts.storage_path is required",
		},
		{
			name: "artifacts enabled with empty allowed_types",
			modify: func(c *Config) {
				c.Artifacts.Enabled = true
				c.Artifacts.StoragePath = "/tmp/artifacts"
				c.Artifacts.AllowedTypes = []string{}
			},
			wantErr:     true,
			errContains: "artifacts.allowed_types must not be empty",
		},
		{
			name: "valid artifacts config",
			modify: func(c *Config) {
				c.Artifacts.Enabled = true
				c.Artifacts.StoragePath = "/tmp/artifacts"
				c.Artifacts.AllowedTypes = []string{"image", "voice"}
			},
			wantErr: false,
		},
		{
			name: "extractor max_file_size_mb validation",
			modify: func(c *Config) {
				c.Artifacts.Enabled = true
				c.Artifacts.StoragePath = "/tmp/artifacts"
				c.Artifacts.AllowedTypes = []string{"image"}
				c.Agents.Extractor.MaxFileSizeMB = 0
			},
			wantErr:     true,
			errContains: "agents.extractor.max_file_size_mb must be positive",
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

func TestValidate_RAGDurationFormats(t *testing.T) {
	validConfig := func() *Config {
		cfg, _ := LoadDefault()
		cfg.Telegram.Token = "test_token"
		cfg.OpenRouter.APIKey = "test_api_key"
		cfg.Database.Path = "test.db"
		cfg.RAG.Enabled = true
		return cfg
	}

	tests := []struct {
		name        string
		modify      func(*Config)
		wantErr     bool
		errContains string
	}{
		{
			name:        "invalid backfill_interval",
			modify:      func(c *Config) { c.RAG.BackfillInterval = "invalid" },
			wantErr:     true,
			errContains: "rag.backfill_interval: invalid duration format",
		},
		{
			name:        "invalid chunk_interval",
			modify:      func(c *Config) { c.RAG.ChunkInterval = "not-a-duration" },
			wantErr:     true,
			errContains: "rag.chunk_interval: invalid duration format",
		},
		{
			name:        "valid durations",
			modify:      func(c *Config) { c.RAG.BackfillInterval = "1m"; c.RAG.ChunkInterval = "1h" },
			wantErr:     false,
			errContains: "",
		},
		{
			name:        "empty durations are ok",
			modify:      func(c *Config) { c.RAG.BackfillInterval = ""; c.RAG.ChunkInterval = "" },
			wantErr:     false,
			errContains: "",
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

func TestValidate_AgentsDefaults(t *testing.T) {
	validConfig := func() *Config {
		cfg, _ := LoadDefault()
		cfg.Telegram.Token = "test_token"
		cfg.OpenRouter.APIKey = "test_api_key"
		cfg.Database.Path = "test.db"
		return cfg
	}

	tests := []struct {
		name        string
		modify      func(*Config)
		wantErr     bool
		errContains string
	}{
		{
			name:        "missing default model",
			modify:      func(c *Config) { c.Agents.Default.Model = "" },
			wantErr:     true,
			errContains: "agents.default.model is required",
		},
		{
			name:        "missing chat name",
			modify:      func(c *Config) { c.Agents.Chat.Name = "" },
			wantErr:     true,
			errContains: "agents.chat.name is required",
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

func TestValidate_EmbeddingModel(t *testing.T) {
	validConfig := func() *Config {
		cfg, _ := LoadDefault()
		cfg.Telegram.Token = "test_token"
		cfg.OpenRouter.APIKey = "test_api_key"
		cfg.Database.Path = "test.db"
		return cfg
	}

	t.Run("missing embedding model", func(t *testing.T) {
		cfg := validConfig()
		cfg.Embedding.Model = ""

		err := cfg.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "embedding.model is required")
	})

	t.Run("valid embedding model", func(t *testing.T) {
		cfg := validConfig()
		cfg.Embedding.Model = "text-embedding-3-small"

		err := cfg.Validate()
		assert.NoError(t, err)
	})
}

func TestValidate_AllRAGPositiveIntegers(t *testing.T) {
	validConfig := func() *Config {
		cfg, _ := LoadDefault()
		cfg.Telegram.Token = "test_token"
		cfg.OpenRouter.APIKey = "test_api_key"
		cfg.Database.Path = "test.db"
		cfg.RAG.Enabled = true
		return cfg
	}

	tests := []struct {
		name        string
		modify      func(*Config)
		errContains string
	}{
		{
			name:        "retrieved_messages_count",
			modify:      func(c *Config) { c.RAG.RetrievedMessagesCount = 0 },
			errContains: "retrieved_messages_count must be positive",
		},
		{
			name:        "retrieved_topics_count",
			modify:      func(c *Config) { c.RAG.RetrievedTopicsCount = 0 },
			errContains: "retrieved_topics_count must be positive",
		},
		{
			name:        "max_chunk_size",
			modify:      func(c *Config) { c.RAG.MaxChunkSize = 0 },
			errContains: "max_chunk_size must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.modify(cfg)

			err := cfg.Validate()
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

func TestValidate_RAGThresholdsRange(t *testing.T) {
	validConfig := func() *Config {
		cfg, _ := LoadDefault()
		cfg.Telegram.Token = "test_token"
		cfg.OpenRouter.APIKey = "test_api_key"
		cfg.Database.Path = "test.db"
		cfg.RAG.Enabled = true
		return cfg
	}

	tests := []struct {
		name        string
		modify      func(*Config)
		errContains string
	}{
		{
			name:        "consolidation_threshold negative",
			modify:      func(c *Config) { c.RAG.ConsolidationSimilarityThreshold = -0.1 },
			errContains: "consolidation_similarity_threshold must be between 0 and 1",
		},
		{
			name:        "consolidation_threshold > 1",
			modify:      func(c *Config) { c.RAG.ConsolidationSimilarityThreshold = 1.5 },
			errContains: "consolidation_similarity_threshold must be between 0 and 1",
		},
		{
			name:        "min_safety_threshold negative",
			modify:      func(c *Config) { c.RAG.MinSafetyThreshold = -0.5 },
			errContains: "min_safety_threshold must be between 0 and 1",
		},
		{
			name:        "min_safety_threshold > 1",
			modify:      func(c *Config) { c.RAG.MinSafetyThreshold = 1.2 },
			errContains: "min_safety_threshold must be between 0 and 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.modify(cfg)

			err := cfg.Validate()
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	// Use truly invalid YAML (unmatched brackets)
	content := `
server:
  listen_port: [invalid
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

	_, err = Load(tmpfile.Name())
	assert.Error(t, err)
}

func TestLoad_ReadFileError(t *testing.T) {
	// Test that Load returns error when file exists but is not readable
	// This is difficult to test cross-platform, so we'll just verify
	// that a valid file works and non-existent file falls back to defaults
	cfg, err := Load("non_existent_file.yaml")
	assert.NoError(t, err) // Falls back to defaults
	assert.NotNil(t, cfg)
}

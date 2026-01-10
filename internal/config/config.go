package config

import (
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
	"gopkg.in/yaml.v3"
)

//go:embed default.yaml
var defaultConfig []byte

type BotConfig struct {
	Language          string  `yaml:"language" env:"LAPLACED_BOT_LANGUAGE"`
	AllowedUserIDs    []int64 `yaml:"allowed_user_ids" env:"LAPLACED_ALLOWED_USER_IDS"`
	SystemPromptExtra string  `yaml:"system_prompt_extra"`
	TurnWaitDuration  string  `yaml:"turn_wait_duration"`
}

// AgentConfig defines configuration for a single agent.
type AgentConfig struct {
	Name  string `yaml:"name"`
	Model string `yaml:"model"`
}

// RerankerAgentConfig extends AgentConfig with reranker-specific settings.
type RerankerAgentConfig struct {
	AgentConfig        `yaml:",inline"`
	Enabled            bool   `yaml:"enabled" env:"LAPLACED_RERANKER_ENABLED"`
	Candidates         int    `yaml:"candidates" env:"LAPLACED_RERANKER_CANDIDATES"`
	MaxTopics          int    `yaml:"max_topics" env:"LAPLACED_RERANKER_MAX_TOPICS"`
	MaxPeople          int    `yaml:"max_people" env:"LAPLACED_RERANKER_MAX_PEOPLE"`
	Timeout            string `yaml:"timeout" env:"LAPLACED_RERANKER_TIMEOUT"`
	TurnTimeout        string `yaml:"turn_timeout" env:"LAPLACED_RERANKER_TURN_TIMEOUT"`
	MaxToolCalls       int    `yaml:"max_tool_calls" env:"LAPLACED_RERANKER_MAX_TOOL_CALLS"`
	ThinkingLevel      string `yaml:"thinking_level" env:"LAPLACED_RERANKER_THINKING_LEVEL"`
	TargetContextChars int    `yaml:"target_context_chars" env:"LAPLACED_RERANKER_TARGET_CONTEXT_CHARS"`
}

// AgentsConfig defines all agents in the system.
type AgentsConfig struct {
	Default   AgentConfig         `yaml:"default"`   // Default model for all agents
	Chat      AgentConfig         `yaml:"chat"`      // Main bot - talks to users
	Archivist AgentConfig         `yaml:"archivist"` // Extracts facts from conversations
	Enricher  AgentConfig         `yaml:"enricher"`  // Expands search queries
	Reranker  RerankerAgentConfig `yaml:"reranker"`  // Filters and ranks RAG candidates
	Splitter  AgentConfig         `yaml:"splitter"`  // Splits large topics
	Merger    AgentConfig         `yaml:"merger"`    // Merges similar topics
}

// GetModel returns the agent's model, falling back to default if not set.
func (a *AgentConfig) GetModel(defaultModel string) string {
	if a.Model != "" {
		return a.Model
	}
	return defaultModel
}

// GetModel returns the reranker's model, falling back to default if not set.
func (r *RerankerAgentConfig) GetModel(defaultModel string) string {
	if r.Model != "" {
		return r.Model
	}
	return defaultModel
}

type YandexConfig struct {
	Enabled     bool   `yaml:"enabled" env:"LAPLACED_YANDEX_ENABLED"`
	APIKey      string `yaml:"api_key" env:"LAPLACED_YANDEX_API_KEY"`
	FolderID    string `yaml:"folder_id" env:"LAPLACED_YANDEX_FOLDER_ID"`
	Language    string `yaml:"language"`
	AudioFormat string `yaml:"audio_format"`
	SampleRate  string `yaml:"sample_rate"`
}

type PriceTier struct {
	UpToTokens     int     `yaml:"up_to_tokens"`
	PromptCost     float64 `yaml:"prompt_cost"`
	CompletionCost float64 `yaml:"completion_cost"`
}

type ToolConfig struct {
	Name                 string `yaml:"name"`
	Model                string `yaml:"model"`
	Description          string `yaml:"description"`
	ParameterDescription string `yaml:"parameter_description"`
}

type OpenRouterConfig struct {
	APIKey          string      `yaml:"api_key" env:"LAPLACED_OPENROUTER_API_KEY"`
	ProxyURL        string      `yaml:"proxy_url" env:"LAPLACED_OPENROUTER_PROXY_URL"`
	PDFParserEngine string      `yaml:"pdf_parser_engine"`
	RequestCost     float64     `yaml:"request_cost"`
	PriceTiers      []PriceTier `yaml:"price_tiers"`
}

// EmbeddingConfig defines embedding model settings.
type EmbeddingConfig struct {
	Model string `yaml:"model" env:"LAPLACED_EMBEDDING_MODEL"`
}

type RAGConfig struct {
	Enabled                          bool    `yaml:"enabled" env:"LAPLACED_RAG_ENABLED"`
	MaxContextMessages               int     `yaml:"max_context_messages"`
	MaxProfileFacts                  int     `yaml:"max_profile_facts"`
	RetrievedMessagesCount           int     `yaml:"retrieved_messages_count"`
	RetrievedTopicsCount             int     `yaml:"retrieved_topics_count"`
	SimilarityThreshold              float64 `yaml:"similarity_threshold"`
	ConsolidationSimilarityThreshold float64 `yaml:"consolidation_similarity_threshold"`
	MinSafetyThreshold               float64 `yaml:"min_safety_threshold"`
	MaxChunkSize                     int     `yaml:"max_chunk_size"`
	BackfillBatchSize                int     `yaml:"backfill_batch_size"`
	BackfillInterval                 string  `yaml:"backfill_interval"`
	ChunkInterval                    string  `yaml:"chunk_interval"`
	MaxMergedSizeChars               int     `yaml:"max_merged_size_chars"`
	SplitThresholdChars              int     `yaml:"split_threshold_chars"`
	RecentTopicsInContext            int     `yaml:"recent_topics_in_context"`
}

// DefaultChunkInterval is the default inactivity period before a session becomes a topic.
const DefaultChunkInterval = 1 * time.Hour

// DefaultSplitThreshold is the default character threshold for splitting large topics.
const DefaultSplitThreshold = 25000

// DefaultRecentTopicsInContext is the default number of recent topics to show in context.
const DefaultRecentTopicsInContext = 3

// GetChunkDuration returns the parsed chunk interval duration.
// Falls back to DefaultChunkInterval if not configured or invalid.
func (c *RAGConfig) GetChunkDuration() time.Duration {
	if c.ChunkInterval == "" {
		return DefaultChunkInterval
	}
	d, err := time.ParseDuration(c.ChunkInterval)
	if err != nil {
		return DefaultChunkInterval
	}
	return d
}

// GetSplitThreshold returns the threshold for splitting large topics.
// Falls back to DefaultSplitThreshold if not configured.
func (c *RAGConfig) GetSplitThreshold() int {
	if c.SplitThresholdChars <= 0 {
		return DefaultSplitThreshold
	}
	return c.SplitThresholdChars
}

// GetRecentTopicsInContext returns the number of recent topics to include in context.
// Falls back to DefaultRecentTopicsInContext if not configured. Returns 0 to disable.
func (c *RAGConfig) GetRecentTopicsInContext() int {
	if c.RecentTopicsInContext < 0 {
		return DefaultRecentTopicsInContext
	}
	if c.RecentTopicsInContext == 0 {
		return DefaultRecentTopicsInContext // 0 in config means use default, explicit disable not supported
	}
	return c.RecentTopicsInContext
}

type Config struct {
	Log struct {
		Level string `yaml:"level" env:"LAPLACED_LOG_LEVEL"`
	} `yaml:"log"`
	Server struct {
		ListenPort string `yaml:"listen_port" env:"LAPLACED_SERVER_PORT"`
		DebugMode  bool   `yaml:"debug_mode" env:"LAPLACED_SERVER_DEBUG"`
		Auth       struct {
			Enabled  bool   `yaml:"enabled" env:"LAPLACED_AUTH_ENABLED"`
			Username string `yaml:"username" env:"LAPLACED_AUTH_USERNAME"`
			Password string `yaml:"password" env:"LAPLACED_AUTH_PASSWORD"`
		} `yaml:"auth"`
	} `yaml:"server"`
	Telegram struct {
		Token         string `yaml:"token" env:"LAPLACED_TELEGRAM_TOKEN"`
		WebhookURL    string `yaml:"webhook_url" env:"LAPLACED_TELEGRAM_WEBHOOK_URL"`
		WebhookPath   string // Auto-generated from token hash (not configurable)
		WebhookSecret string // Auto-generated from token hash (not configurable)
		ProxyURL      string `yaml:"proxy_url" env:"LAPLACED_TELEGRAM_PROXY_URL"`
	} `yaml:"telegram"`
	OpenRouter OpenRouterConfig `yaml:"openrouter"`
	Agents     AgentsConfig     `yaml:"agents"`
	Embedding  EmbeddingConfig  `yaml:"embedding"`
	RAG        RAGConfig        `yaml:"rag"`
	Tools      []ToolConfig     `yaml:"tools"`
	Bot        BotConfig        `yaml:"bot"`
	Database   struct {
		Path string `yaml:"path" env:"LAPLACED_DATABASE_PATH"`
	} `yaml:"database"`
	Yandex YandexConfig `yaml:"yandex"`
}

// Load loads configuration from the specified file path.
// It first loads the embedded default configuration, then merges the user config on top.
// Finally, it overrides values with environment variables.
func Load(path string) (*Config, error) {
	// First, load the embedded default config
	var cfg Config
	if err := yaml.Unmarshal(defaultConfig, &cfg); err != nil {
		return nil, err
	}

	// If a path is specified and the file exists, merge user config on top
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, err
			}
			// File doesn't exist, just use defaults
			// Log this so users know their config wasn't loaded
			slog.Warn("config file not found, using defaults", "path", path)
		} else {
			// Expand environment variables in user config (legacy support)
			expandedData := []byte(os.ExpandEnv(string(data)))

			// Unmarshal user config on top of defaults (merges non-zero values)
			if err := yaml.Unmarshal(expandedData, &cfg); err != nil {
				return nil, err
			}
			slog.Info("loaded user config", "path", path)
		}
	}

	// Override with environment variables using cleanenv
	if err := cleanenv.ReadEnv(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// LoadDefault loads the embedded default configuration.
func LoadDefault() (*Config, error) {
	return Load("")
}

// DefaultConfigBytes returns the raw embedded default configuration.
// Useful for generating example config files.
func DefaultConfigBytes() []byte {
	return defaultConfig
}

// Validate checks configuration for required fields and valid ranges.
// Returns an error describing all validation failures.
func (c *Config) Validate() error {
	var errs []error

	// Required fields
	if c.Telegram.Token == "" {
		errs = append(errs, errors.New("telegram.token is required"))
	}
	if c.OpenRouter.APIKey == "" {
		errs = append(errs, errors.New("openrouter.api_key is required"))
	}
	if c.Database.Path == "" {
		errs = append(errs, errors.New("database.path is required"))
	}

	// Yandex requires api_key and folder_id if enabled
	if c.Yandex.Enabled {
		if c.Yandex.APIKey == "" {
			errs = append(errs, errors.New("yandex.api_key is required when yandex.enabled is true"))
		}
		if c.Yandex.FolderID == "" {
			errs = append(errs, errors.New("yandex.folder_id is required when yandex.enabled is true"))
		}
	}

	// Server auth requires username if enabled (password is auto-generated if not set)
	if c.Server.Auth.Enabled {
		if c.Server.Auth.Username == "" {
			errs = append(errs, errors.New("server.auth.username is required when server.auth.enabled is true"))
		}
	}

	// RAG thresholds must be in range [0, 1]
	if c.RAG.Enabled {
		if c.RAG.SimilarityThreshold < 0 || c.RAG.SimilarityThreshold > 1 {
			errs = append(errs, fmt.Errorf("rag.similarity_threshold must be between 0 and 1, got %f", c.RAG.SimilarityThreshold))
		}
		if c.RAG.ConsolidationSimilarityThreshold < 0 || c.RAG.ConsolidationSimilarityThreshold > 1 {
			errs = append(errs, fmt.Errorf("rag.consolidation_similarity_threshold must be between 0 and 1, got %f", c.RAG.ConsolidationSimilarityThreshold))
		}
		if c.RAG.MinSafetyThreshold < 0 || c.RAG.MinSafetyThreshold > 1 {
			errs = append(errs, fmt.Errorf("rag.min_safety_threshold must be between 0 and 1, got %f", c.RAG.MinSafetyThreshold))
		}

		// Positive integers
		if c.RAG.MaxContextMessages <= 0 {
			errs = append(errs, fmt.Errorf("rag.max_context_messages must be positive, got %d", c.RAG.MaxContextMessages))
		}
		if c.RAG.MaxProfileFacts <= 0 {
			errs = append(errs, fmt.Errorf("rag.max_profile_facts must be positive, got %d", c.RAG.MaxProfileFacts))
		}
		if c.RAG.RetrievedMessagesCount <= 0 {
			errs = append(errs, fmt.Errorf("rag.retrieved_messages_count must be positive, got %d", c.RAG.RetrievedMessagesCount))
		}
		if c.RAG.RetrievedTopicsCount <= 0 {
			errs = append(errs, fmt.Errorf("rag.retrieved_topics_count must be positive, got %d", c.RAG.RetrievedTopicsCount))
		}
		if c.RAG.MaxChunkSize <= 0 {
			errs = append(errs, fmt.Errorf("rag.max_chunk_size must be positive, got %d", c.RAG.MaxChunkSize))
		}

		// Duration format validation
		if c.RAG.BackfillInterval != "" {
			if _, err := time.ParseDuration(c.RAG.BackfillInterval); err != nil {
				errs = append(errs, fmt.Errorf("rag.backfill_interval: invalid duration format %q: %w", c.RAG.BackfillInterval, err))
			}
		}
		if c.RAG.ChunkInterval != "" {
			if _, err := time.ParseDuration(c.RAG.ChunkInterval); err != nil {
				errs = append(errs, fmt.Errorf("rag.chunk_interval: invalid duration format %q: %w", c.RAG.ChunkInterval, err))
			}
		}

	}

	// Agents validation
	if c.Agents.Default.Model == "" {
		errs = append(errs, errors.New("agents.default.model is required"))
	}
	if c.Agents.Chat.Name == "" {
		errs = append(errs, errors.New("agents.chat.name is required"))
	}

	// Embedding model validation
	if c.Embedding.Model == "" {
		errs = append(errs, errors.New("embedding.model is required"))
	}

	// Reranker agent validation
	r := &c.Agents.Reranker
	if r.Candidates <= 0 {
		errs = append(errs, fmt.Errorf("agents.reranker.candidates must be positive, got %d", r.Candidates))
	}
	if r.MaxTopics <= 0 {
		errs = append(errs, fmt.Errorf("agents.reranker.max_topics must be positive, got %d", r.MaxTopics))
	}
	if r.MaxToolCalls <= 0 {
		errs = append(errs, fmt.Errorf("agents.reranker.max_tool_calls must be positive, got %d", r.MaxToolCalls))
	}
	if r.Timeout != "" {
		if _, err := time.ParseDuration(r.Timeout); err != nil {
			errs = append(errs, fmt.Errorf("agents.reranker.timeout: invalid duration format %q: %w", r.Timeout, err))
		}
	}
	if r.TurnTimeout != "" {
		if _, err := time.ParseDuration(r.TurnTimeout); err != nil {
			errs = append(errs, fmt.Errorf("agents.reranker.turn_timeout: invalid duration format %q: %w", r.TurnTimeout, err))
		}
	}
	if r.ThinkingLevel != "" {
		validLevels := map[string]bool{"off": true, "minimal": true, "low": true, "medium": true, "high": true}
		if !validLevels[r.ThinkingLevel] {
			errs = append(errs, fmt.Errorf("agents.reranker.thinking_level: must be one of 'off', 'minimal', 'low', 'medium', 'high', got %q", r.ThinkingLevel))
		}
	}

	// Bot duration validation
	if c.Bot.TurnWaitDuration != "" {
		if _, err := time.ParseDuration(c.Bot.TurnWaitDuration); err != nil {
			errs = append(errs, fmt.Errorf("bot.turn_wait_duration: invalid duration format %q: %w", c.Bot.TurnWaitDuration, err))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

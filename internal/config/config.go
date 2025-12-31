package config

import (
	_ "embed"
	"os"

	"github.com/ilyakaznacheev/cleanenv"
	"gopkg.in/yaml.v3"
)

//go:embed default.yaml
var defaultConfig []byte

type BotConfig struct {
	Language          string  `yaml:"language" env:"LAPLACED_BOT_LANGUAGE"`
	BotName           string  `yaml:"bot_name" env:"LAPLACED_BOT_NAME"`
	AllowedUserIDs    []int64 `yaml:"allowed_user_ids" env:"LAPLACED_ALLOWED_USER_IDS"`
	SystemPrompt      string  `yaml:"system_prompt"`       // Deprecated: moved to i18n
	SystemPromptExtra string  `yaml:"system_prompt_extra"` // New
	TurnWaitDuration  string  `yaml:"turn_wait_duration"`
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
	Model           string      `yaml:"model" env:"LAPLACED_OPENROUTER_MODEL"`
	PDFParserEngine string      `yaml:"pdf_parser_engine"`
	RequestCost     float64     `yaml:"request_cost"`
	PriceTiers      []PriceTier `yaml:"price_tiers"`
}

type RAGConfig struct {
	Enabled                          bool    `yaml:"enabled" env:"LAPLACED_RAG_ENABLED"`
	EmbeddingModel                   string  `yaml:"embedding_model"`
	SummaryModel                     string  `yaml:"summary_model"`
	QueryModel                       string  `yaml:"query_model"`
	MaxContextMessages               int     `yaml:"max_context_messages"`
	RetrievedMessagesCount           int     `yaml:"retrieved_messages_count"`
	RetrievedTopicsCount             int     `yaml:"retrieved_topics_count"`
	SimilarityThreshold              float64 `yaml:"similarity_threshold"`
	ConsolidationSimilarityThreshold float64 `yaml:"consolidation_similarity_threshold"` // New
	MinSafetyThreshold               float64 `yaml:"min_safety_threshold"`               // New
	MaxChunkSize                     int     `yaml:"max_chunk_size"`                     // New
	BackfillBatchSize                int     `yaml:"backfill_batch_size"`
	BackfillInterval                 string  `yaml:"backfill_interval"`
	TopicModel                       string  `yaml:"topic_model"`
	ChunkInterval                    string  `yaml:"chunk_interval"`
	TopicExtractionPrompt            string  `yaml:"topic_extraction_prompt"` // Deprecated: moved to i18n
	EnrichmentPrompt                 string  `yaml:"enrichment_prompt"`       // Deprecated: moved to i18n
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
		Token      string `yaml:"token" env:"LAPLACED_TELEGRAM_TOKEN"`
		WebhookURL string `yaml:"webhook_url" env:"LAPLACED_TELEGRAM_WEBHOOK_URL"`
		ProxyURL   string `yaml:"proxy_url" env:"LAPLACED_TELEGRAM_PROXY_URL"`
	} `yaml:"telegram"`
	OpenRouter OpenRouterConfig `yaml:"openrouter"`
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
			println("WARNING: Config file not found at", path, "- using defaults")
		} else {
			// Expand environment variables in user config (legacy support)
			expandedData := []byte(os.ExpandEnv(string(data)))

			// Unmarshal user config on top of defaults (merges non-zero values)
			if err := yaml.Unmarshal(expandedData, &cfg); err != nil {
				return nil, err
			}
			println("INFO: Loaded user config from", path)
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

package config

import (
	_ "embed"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
	"github.com/runixer/laplaced/internal/llm"
	"gopkg.in/yaml.v3"
)

//go:embed default.yaml
var defaultConfig []byte

type BotConfig struct {
	Language          string          `yaml:"language" env:"LAPLACED_BOT_LANGUAGE"`
	AllowedUserIDs    []int64         `yaml:"allowed_user_ids" env:"LAPLACED_ALLOWED_USER_IDS"`
	SystemPromptExtra string          `yaml:"system_prompt_extra"`
	TurnWaitDuration  string          `yaml:"turn_wait_duration"`
	Streaming         StreamingConfig `yaml:"streaming"`
}

// StreamingConfig controls progressive bot replies via editMessageText.
//
// When Enabled, the bot sends a placeholder message immediately, edits it with
// tool-execution status during the agent's tool loop, and then progressively
// reveals the LLM's content as SSE deltas arrive. EditThrottleMs / EditMinChars
// throttle edits to keep under Telegram's edit rate limit. MaxBufferChars caps
// in-bubble streaming — past that the response falls back to the existing
// finalize+sendResponses path (separate messages).
type StreamingConfig struct {
	Enabled        bool `yaml:"enabled" env:"LAPLACED_BOT_STREAMING_ENABLED"`
	EditThrottleMs int  `yaml:"edit_throttle_ms" env:"LAPLACED_BOT_STREAMING_EDIT_THROTTLE_MS"`
	EditMinChars   int  `yaml:"edit_min_chars" env:"LAPLACED_BOT_STREAMING_EDIT_MIN_CHARS"`
	MaxBufferChars int  `yaml:"max_buffer_chars" env:"LAPLACED_BOT_STREAMING_MAX_BUFFER_CHARS"`
}

// GetEditThrottle returns the configured throttle as a Duration. Defaults to
// 1s when unset or non-positive.
func (s StreamingConfig) GetEditThrottle() time.Duration {
	if s.EditThrottleMs <= 0 {
		return time.Second
	}
	return time.Duration(s.EditThrottleMs) * time.Millisecond
}

// GetEditMinChars returns the minimum new-character threshold for an early
// edit (before the throttle elapses). Defaults to 80 when unset.
func (s StreamingConfig) GetEditMinChars() int {
	if s.EditMinChars <= 0 {
		return 80
	}
	return s.EditMinChars
}

// GetMaxBufferChars returns the in-bubble streaming cap. Defaults to 3400 — a
// safety margin under Telegram's 4096 UTF-16 limit accounting for HTML expansion.
func (s StreamingConfig) GetMaxBufferChars() int {
	if s.MaxBufferChars <= 0 {
		return 3400
	}
	return s.MaxBufferChars
}

// AgentConfig defines configuration for a single agent.
type AgentConfig struct {
	Name  string `yaml:"name"`
	Model string `yaml:"model"`
}

// ChatAgentConfig extends AgentConfig with chat-specific settings.
// Setting ThinkingLevel explicitly prevents Gemini 3.1 Pro from leaking internal
// reasoning into the user-visible content field (see docs/bugs/2026-04-22-laplace-thought-leak/).
type ChatAgentConfig struct {
	AgentConfig       `yaml:",inline"`
	ThinkingLevel     string `yaml:"thinking_level" env:"LAPLACED_AGENTS_CHAT_THINKING_LEVEL"`
	MaxToolIterations int    `yaml:"max_tool_iterations" env:"LAPLACED_AGENTS_CHAT_MAX_TOOL_ITERATIONS"`
}

// validThinkingLevels are the accepted thinking_level values for all agents.
// "minimal".."high" set an explicit reasoning effort. "auto" omits the
// reasoning field, letting the model pick its own budget (Gemini dynamic
// thinking). "off" also omits the field — on Gemini that means dynamic
// thinking too, NOT disabled; kept for backward compatibility.
var validThinkingLevels = map[string]bool{
	"auto": true, "off": true, "minimal": true, "low": true, "medium": true, "high": true,
}

// RerankerTypeConfig defines per-type reranker limits (v0.6.0).
type RerankerTypeConfig struct {
	CandidatesLimit int `yaml:"candidates_limit" env:"LAPLACED_RERANKER_TOPICS_CANDIDATES_LIMIT"`
	Max             int `yaml:"max" env:"LAPLACED_RERANKER_TOPICS_MAX"`
	MaxContextBytes int `yaml:"max_context_bytes,omitempty" env:"LAPLACED_RERANKER_TOPICS_MAX_CONTEXT_BYTES"` // For artifacts only (v0.6.0)
}

// RerankerArtifactsSessionConfig governs how many session-active artifacts are
// injected into the reranker candidate pool and how old they may be.
type RerankerArtifactsSessionConfig struct {
	Max    int    `yaml:"max" env:"LAPLACED_RERANKER_ARTIFACTS_SESSION_MAX"`
	MaxAge string `yaml:"max_age" env:"LAPLACED_RERANKER_ARTIFACTS_SESSION_MAX_AGE"`
}

// IsEnabled reports whether session-aware artifact injection is configured.
// Disabled (zero) by default — the operator must opt in via configs/default.yaml
// or env var. Tests using testutil.TestConfig() therefore won't trigger the
// new storage path unless they explicitly configure it.
func (c RerankerArtifactsSessionConfig) IsEnabled() bool {
	return c.Max > 0
}

// GetMaxAge returns the session age cap. Defaults to 24h when session is enabled
// but max_age is missing or invalid; returns 0 when session is disabled.
func (c RerankerArtifactsSessionConfig) GetMaxAge() time.Duration {
	if !c.IsEnabled() {
		return 0
	}
	if c.MaxAge == "" {
		return 24 * time.Hour
	}
	d, err := time.ParseDuration(c.MaxAge)
	if err != nil || d <= 0 {
		return 24 * time.Hour
	}
	return d
}

// RerankerArtifactsConfig extends per-type limits with session-aware injection
// (artifacts attached to messages still in the active session, topic_id IS NULL).
type RerankerArtifactsConfig struct {
	RerankerTypeConfig `yaml:",inline"`
	Session            RerankerArtifactsSessionConfig `yaml:"session"`
}

// RerankerAgentConfig extends AgentConfig with reranker-specific settings.
type RerankerAgentConfig struct {
	AgentConfig        `yaml:",inline"`
	Enabled            bool   `yaml:"enabled" env:"LAPLACED_RERANKER_ENABLED"`
	Timeout            string `yaml:"timeout" env:"LAPLACED_RERANKER_TIMEOUT"`
	TurnTimeout        string `yaml:"turn_timeout" env:"LAPLACED_RERANKER_TURN_TIMEOUT"`
	MaxToolCalls       int    `yaml:"max_tool_calls" env:"LAPLACED_RERANKER_MAX_TOOL_CALLS"`
	ThinkingLevel      string `yaml:"thinking_level" env:"LAPLACED_RERANKER_THINKING_LEVEL"`
	TargetContextChars int    `yaml:"target_context_chars" env:"LAPLACED_RERANKER_TARGET_CONTEXT_CHARS"`

	// Per-type limits (v0.6.0)
	Topics    RerankerTypeConfig      `yaml:"topics"`
	People    RerankerTypeConfig      `yaml:"people"`
	Artifacts RerankerArtifactsConfig `yaml:"artifacts"`

	// Legacy fields (deprecated, kept for migration)
	Candidates int `yaml:"candidates" env:"LAPLACED_RERANKER_CANDIDATES"`
	MaxTopics  int `yaml:"max_topics" env:"LAPLACED_RERANKER_MAX_TOPICS"`
	MaxPeople  int `yaml:"max_people" env:"LAPLACED_RERANKER_MAX_PEOPLE"`
}

// ArchivistAgentConfig extends AgentConfig with archivist-specific settings.
type ArchivistAgentConfig struct {
	AgentConfig   `yaml:",inline"`
	ThinkingLevel string `yaml:"thinking_level" env:"LAPLACED_ARCHIVIST_THINKING_LEVEL"`
	Timeout       string `yaml:"timeout" env:"LAPLACED_ARCHIVIST_TIMEOUT"`
	MaxToolCalls  int    `yaml:"max_tool_calls" env:"LAPLACED_ARCHIVIST_MAX_TOOL_CALLS"`
}

// GetModel returns the archivist's model, falling back to default if not set.
func (a *ArchivistAgentConfig) GetModel(defaultModel string) string {
	if a.Model != "" {
		return a.Model
	}
	return defaultModel
}

// ExtractorAgentConfig defines configuration for the Extractor agent (v0.6.0).
// Processing settings were moved from ArtifactsConfig to keep agent-related config together.
type ExtractorAgentConfig struct {
	AgentConfig `yaml:",inline"` // Name, Model

	// Processing settings (moved from ArtifactsConfig in v0.6.0)
	MaxFileSizeMB      int    `yaml:"max_file_size_mb" env:"LAPLACED_EXTRACTOR_MAX_FILE_SIZE_MB"`
	Timeout            string `yaml:"timeout" env:"LAPLACED_EXTRACTOR_TIMEOUT"`
	MaxRetries         int    `yaml:"max_retries" env:"LAPLACED_EXTRACTOR_MAX_RETRIES"`
	PollingInterval    string `yaml:"polling_interval" env:"LAPLACED_EXTRACTOR_POLLING_INTERVAL"`
	MaxConcurrent      int    `yaml:"max_concurrent" env:"LAPLACED_EXTRACTOR_MAX_CONCURRENT"`
	RecoveryThreshold  string `yaml:"recovery_threshold" env:"LAPLACED_EXTRACTOR_RECOVERY_THRESHOLD"`
	RecentMessageCount int    `yaml:"recent_message_count" env:"LAPLACED_EXTRACTOR_RECENT_MESSAGE_COUNT"` // Number of recent session messages to include in artifact context (0 = disable)
}

// GetModel returns the extractor's model, falling back to default if not set.
func (e *ExtractorAgentConfig) GetModel(defaultModel string) string {
	if e.Model != "" {
		return e.Model
	}
	return defaultModel
}

// GetTimeout returns the timeout for artifact processing.
// Defaults to 2 minutes if not configured.
func (e *ExtractorAgentConfig) GetTimeout() time.Duration {
	if e.Timeout == "" {
		return 2 * time.Minute
	}
	d, err := time.ParseDuration(e.Timeout)
	if err != nil || d == 0 {
		return 2 * time.Minute
	}
	return d
}

// GetPollingInterval returns the interval for polling pending artifacts.
// Defaults to 30 seconds if not configured.
func (e *ExtractorAgentConfig) GetPollingInterval() time.Duration {
	if e.PollingInterval == "" {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(e.PollingInterval)
	if err != nil || d == 0 {
		return 30 * time.Second
	}
	return d
}

// GetRecoveryThreshold returns the threshold for recovering zombie artifact states.
// Defaults to 10 minutes if not configured.
func (e *ExtractorAgentConfig) GetRecoveryThreshold() time.Duration {
	if e.RecoveryThreshold == "" {
		return 10 * time.Minute
	}
	d, err := time.ParseDuration(e.RecoveryThreshold)
	if err != nil || d == 0 {
		return 10 * time.Minute
	}
	return d
}

// ImageGeneratorConfig defines configuration for the image-generation agent
// that drives OpenRouter image-output models (v0.8.0).
type ImageGeneratorConfig struct {
	AgentConfig `yaml:",inline"` // Name, Model (e.g. google/gemini-3.1-flash-image-preview)

	Timeout            string `yaml:"timeout" env:"LAPLACED_IMAGE_GENERATOR_TIMEOUT"`
	DefaultAspectRatio string `yaml:"default_aspect_ratio" env:"LAPLACED_IMAGE_GENERATOR_DEFAULT_ASPECT_RATIO"`
	DefaultImageSize   string `yaml:"default_image_size" env:"LAPLACED_IMAGE_GENERATOR_DEFAULT_IMAGE_SIZE"`
	// SupportedImageSizes / SupportedAspectRatios drive the generate_image tool
	// JSON schema. They MUST match what the configured Model accepts upstream —
	// see the commented alternative in default.yaml for verified per-model sets.
	SupportedImageSizes   []string `yaml:"supported_image_sizes" env:"LAPLACED_IMAGE_GENERATOR_SUPPORTED_IMAGE_SIZES" env-separator:","`
	SupportedAspectRatios []string `yaml:"supported_aspect_ratios" env:"LAPLACED_IMAGE_GENERATOR_SUPPORTED_ASPECT_RATIOS" env-separator:","`
	MaxInputImages        int      `yaml:"max_input_images" env:"LAPLACED_IMAGE_GENERATOR_MAX_INPUT_IMAGES"`
	MaxOutputImages       int      `yaml:"max_output_images" env:"LAPLACED_IMAGE_GENERATOR_MAX_OUTPUT_IMAGES"`
	MaxInputImageBytes    int      `yaml:"max_input_image_bytes" env:"LAPLACED_IMAGE_GENERATOR_MAX_INPUT_IMAGE_BYTES"`
	// DocumentThresholdBytes: generated images larger than this are sent via
	// sendDocument instead of sendPhoto, preserving full resolution (Telegram
	// recompresses photos to ~1280 px on long side). Default 2 MB covers
	// 2K/4K outputs; set to 0 to always use sendPhoto.
	DocumentThresholdBytes int `yaml:"document_threshold_bytes" env:"LAPLACED_IMAGE_GENERATOR_DOCUMENT_THRESHOLD_BYTES"`
	// MaxConcurrent bounds how many generate_image tool calls from a single
	// assistant turn run in parallel (e.g. "draw three pictures"). Other tools
	// stay sequential. Defaults to 4.
	MaxConcurrent int `yaml:"max_concurrent" env:"LAPLACED_IMAGE_GENERATOR_MAX_CONCURRENT"`
}

// GetMaxConcurrent returns the parallel-generation cap, defaulting to 4.
func (c *ImageGeneratorConfig) GetMaxConcurrent() int {
	if c.MaxConcurrent <= 0 {
		return 4
	}
	return c.MaxConcurrent
}

// GetTimeout returns the per-call timeout. Defaults to 90s.
func (c *ImageGeneratorConfig) GetTimeout() time.Duration {
	if c.Timeout == "" {
		return 90 * time.Second
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil || d == 0 {
		return 90 * time.Second
	}
	return d
}

// ReactorAgentConfig configures the emoji-reaction agent. Standalone struct
// (not inline AgentConfig) so the env tags stay agent-unique.
type ReactorAgentConfig struct {
	Name    string `yaml:"name"`
	Model   string `yaml:"model" env:"LAPLACED_AGENTS_REACTOR_MODEL"`
	Enabled bool   `yaml:"enabled" env:"LAPLACED_AGENTS_REACTOR_ENABLED"`
}

// GetModel returns the reactor's model, falling back to default if not set.
func (r *ReactorAgentConfig) GetModel(defaultModel string) string {
	if r.Model != "" {
		return r.Model
	}
	return defaultModel
}

// AgentsConfig defines all agents in the system.
type AgentsConfig struct {
	Default        AgentConfig          `yaml:"default"`                            // Default model for all agents
	Chat           ChatAgentConfig      `yaml:"chat"`                               // Main bot - talks to users
	ChatModel      string               `yaml:"-" env:"LAPLACED_AGENTS_CHAT_MODEL"` // Override for chat agent model
	Archivist      ArchivistAgentConfig `yaml:"archivist"`                          // Extracts facts and people from conversations
	Enricher       AgentConfig          `yaml:"enricher"`                           // Expands search queries
	Reactor        ReactorAgentConfig   `yaml:"reactor"`                            // Decides emoji reactions to user messages
	Reranker       RerankerAgentConfig  `yaml:"reranker"`                           // Filters and ranks RAG candidates
	Splitter       AgentConfig          `yaml:"splitter"`                           // Splits large topics
	Merger         AgentConfig          `yaml:"merger"`                             // Merges similar topics
	Extractor      ExtractorAgentConfig `yaml:"extractor"`                          // Extracts content from artifacts
	ImageGenerator ImageGeneratorConfig `yaml:"image_generator"`                    // Generates/edits images (v0.8.0)
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

// GetChatModel returns the chat agent's model, applying env override if set.
func (a *AgentsConfig) GetChatModel() string {
	if a.ChatModel != "" {
		return a.ChatModel
	}
	return a.Chat.GetModel(a.Default.Model)
}

// GetChatMaxToolIterations returns how many tool-loop turns the chat agent may
// spend per reply before it is forced into a final synthesis turn without
// tools. Falls back to 5 when unset — traces show the model rarely gains new
// information after the 4th search; further turns are usually reformulations
// of the same query.
func (a *AgentsConfig) GetChatMaxToolIterations() int {
	if a.Chat.MaxToolIterations > 0 {
		return a.Chat.MaxToolIterations
	}
	return 5
}

// GetChatThinkingLevel returns the chat agent's reasoning effort level.
// Falls back to "low" when unset — the minimum supported on Gemini 3.1 Pro.
// Passing an explicit level prevents the model from leaking internal reasoning
// into content (see docs/bugs/2026-04-22-laplace-thought-leak/).
// "auto" (and legacy "off") omit the reasoning field entirely — Gemini then
// uses dynamic thinking and picks its own budget per request.
func (a *AgentsConfig) GetChatThinkingLevel() string {
	if a.Chat.ThinkingLevel != "" {
		return a.Chat.ThinkingLevel
	}
	return "low"
}

// ArtifactsConfig defines configuration for the artifacts system (v0.6.0).
// Processing settings moved to agents.extractor. RAG settings moved to rag and agents.reranker.artifacts.
type ArtifactsConfig struct {
	Enabled      bool     `yaml:"enabled" env:"LAPLACED_ARTIFACTS_ENABLED"`
	StoragePath  string   `yaml:"storage_path" env:"LAPLACED_ARTIFACTS_STORAGE_PATH"`
	AllowedTypes []string `yaml:"allowed_types"`

	// Voice settings (about storage filtering, not extraction)
	MinVoiceDurationSeconds int `yaml:"min_voice_duration_seconds" env:"LAPLACED_ARTIFACTS_MIN_VOICE_DURATION_SECONDS"` // 0 = save all, -1 = disable voice artifacts

	// S3, when present, switches the artifact blob store from the local disk
	// (StoragePath) to an S3-compatible bucket. Absence keeps the local backend,
	// so the home deployment is byte-identical. Capability block, not a mode flag.
	S3 *S3Config `yaml:"s3"`
}

// S3Config configures an S3-compatible artifact backend (Yandex Object Storage).
// access_key/secret_key may be "vault:" references — they're registered in
// Config.secretFields() so they resolve at startup.
type S3Config struct {
	Endpoint  string `yaml:"endpoint" env:"LAPLACED_ARTIFACTS_S3_ENDPOINT"`
	Region    string `yaml:"region" env:"LAPLACED_ARTIFACTS_S3_REGION"`
	Bucket    string `yaml:"bucket" env:"LAPLACED_ARTIFACTS_S3_BUCKET"`
	AccessKey string `yaml:"access_key" env:"LAPLACED_ARTIFACTS_S3_ACCESS_KEY"`
	SecretKey string `yaml:"secret_key" env:"LAPLACED_ARTIFACTS_S3_SECRET_KEY"`
}

// validate checks the S3 block for required fields. Called from Config.Validate
// only when the block is present. Credentials may still be unresolved vault:
// references at validation time, so they are not required here.
func (s *S3Config) validate() []error {
	var errs []error
	if s.Endpoint == "" {
		errs = append(errs, errors.New("artifacts.s3.endpoint is required"))
	}
	if s.Bucket == "" {
		errs = append(errs, errors.New("artifacts.s3.bucket is required"))
	}
	// SigV4 needs a region to sign requests; Yandex tolerates an empty one with a
	// custom endpoint, but stricter S3 backends reject the signature. Require it.
	if s.Region == "" {
		errs = append(errs, errors.New("artifacts.s3.region is required"))
	}
	return errs
}

// MemoryConfig defines configuration for memory operations.
type MemoryConfig struct {
	FactDefaultImportance int `yaml:"fact_default_importance" env:"LAPLACED_MEMORY_FACT_DEFAULT_IMPORTANCE"`
}

// SearchConfig defines configuration for search operations.
type SearchConfig struct {
	PeopleSimilarityThreshold float64 `yaml:"people_similarity_threshold" env:"LAPLACED_SEARCH_PEOPLE_SIMILARITY_THRESHOLD"`
	PeopleMaxResults          int     `yaml:"people_max_results" env:"LAPLACED_SEARCH_PEOPLE_MAX_RESULTS"`
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

// ToolConfigured reports whether a tool with the given name is exposed in the
// tools list. Tool schemas and prompt protocol sections are both derived from
// this list, so it is the single source of truth for tool exposure.
func (c *Config) ToolConfigured(name string) bool {
	for _, t := range c.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

// DisableTool removes a tool from the exposed tools list. Used at startup when
// a tool's backing dependency failed to initialize (e.g. read_url without a
// fetcher): dropping it here keeps the tool schema and the system prompt
// consistent, instead of steering the model toward a tool that always fails.
func (c *Config) DisableTool(name string) {
	kept := c.Tools[:0]
	for _, t := range c.Tools {
		if t.Name != name {
			kept = append(kept, t)
		}
	}
	c.Tools = kept
}

// FetcherConfig configures the web-page fetcher backing the read_url tool.
// Backend selects the implementation; there is deliberately no automatic
// fallback between backends — a silent downgrade would mask credit exhaustion
// and produce mysteriously degraded extractions.
type FetcherConfig struct {
	// Backend: "firecrawl" (api.firecrawl.dev REST scrape, JS rendering,
	// 1 credit/page) or "raw" (plain HTTP + text extraction, no external
	// service). "mcp" is reserved for a future backend.
	Backend string `yaml:"backend" env:"LAPLACED_FETCHER_BACKEND"`
	Timeout string `yaml:"timeout" env:"LAPLACED_FETCHER_TIMEOUT"`
	// MaxContentChars caps the tool result size in runes; longer pages are
	// truncated with a marker. Guards the main model's context from page dumps.
	MaxContentChars int             `yaml:"max_content_chars" env:"LAPLACED_FETCHER_MAX_CONTENT_CHARS"`
	Firecrawl       FirecrawlConfig `yaml:"firecrawl"`
}

// FirecrawlConfig holds the Firecrawl REST API settings. APIKey may be a
// "vault:" reference (registered in secretFields).
type FirecrawlConfig struct {
	BaseURL string `yaml:"base_url" env:"LAPLACED_FIRECRAWL_BASE_URL"`
	APIKey  string `yaml:"api_key" env:"LAPLACED_FIRECRAWL_API_KEY"`
}

// GetTimeout returns the per-fetch timeout. Defaults to 60s — Firecrawl
// JS rendering routinely takes 15-30s. Non-positive values also fall back:
// http.Client treats Timeout <= 0 as "no timeout at all", so passing a
// negative config value through would let read_url hang indefinitely.
func (c *FetcherConfig) GetTimeout() time.Duration {
	if c.Timeout == "" {
		return 60 * time.Second
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil || d <= 0 {
		return 60 * time.Second
	}
	return d
}

// GetMaxContentChars returns the result size cap in runes, defaulting to 15000.
func (c *FetcherConfig) GetMaxContentChars() int {
	if c.MaxContentChars <= 0 {
		return 15000
	}
	return c.MaxContentChars
}

// GetBaseURL returns the Firecrawl API base URL, defaulting to the public API.
func (c *FirecrawlConfig) GetBaseURL() string {
	if c.BaseURL == "" {
		return "https://api.firecrawl.dev"
	}
	return c.BaseURL
}

// MattermostConfig configures the Mattermost/Time transport (used when
// transport == "mattermost"). The proxy is per-client (HTTP proxy); never set a
// process-wide HTTP_PROXY, which would also route the LLM client.
type MattermostConfig struct {
	ServerURL      string   `yaml:"server_url" env:"LAPLACED_MATTERMOST_SERVER_URL"`
	BotToken       string   `yaml:"bot_token" env:"LAPLACED_MATTERMOST_BOT_TOKEN"`
	ProxyURL       string   `yaml:"proxy_url" env:"LAPLACED_MATTERMOST_PROXY_URL"`
	AllowedUserIDs []string `yaml:"allowed_user_ids" env:"LAPLACED_MATTERMOST_ALLOWED_USER_IDS" env-separator:","`
	// PrincipalResolver, when present, turns on principal identity resolution for
	// this transport's DMs. Its mere presence enables resolution
	// (organic config, not a mode flag); absence = passthrough (the default behavior).
	PrincipalResolver *PrincipalResolverConfig `yaml:"principal_resolver"`
}

// PrincipalResolverConfig enables and tunes federated-passive principal
// resolution for a transport. The trust gate is the transport's
// own auth_service (Mattermost GetUser): a local account (auth_service == "") is
// NEVER linked, and identities are NEVER linked by self-claimed email. Resolution
// is federated-passive only for now; an objectGUID/Keycloak lookup arrives later.
type PrincipalResolverConfig struct {
	// TrustedAuthServices restricts which Mattermost auth_service values are
	// trusted for principal linkage. Empty = trust any non-empty auth_service
	// (the default gate). Local accounts (auth_service == "") are never linked.
	TrustedAuthServices []string `yaml:"trusted_auth_services" env:"LAPLACED_MATTERMOST_TRUSTED_AUTH_SERVICES" env-separator:","`
	// AccessDeniedMessage is the verbatim text sent to a sender denied access
	// because they are not an SSO-authenticated user. Empty falls back to the
	// neutral localized default (i18n bot.access_denied). Deployment-specific
	// wording (e.g. "sign in via your corporate SSO first") belongs here, in a
	// gitignored overlay — never in tracked locale files.
	AccessDeniedMessage string `yaml:"access_denied_message" env:"LAPLACED_MATTERMOST_ACCESS_DENIED_MESSAGE"`
	// TrustedBots lists bot-account usernames (case-insensitive, leading "@"
	// tolerated) allowed to interact with the bot despite being local accounts
	// that fail the SSO trust gate. Empty = no bots trusted (fail-closed). Use for
	// trusted automation such as an alerting bot requesting an incident summary. A
	// bot not on this list is ignored silently — not sent an access-denied notice.
	TrustedBots []string `yaml:"trusted_bots" env:"LAPLACED_MATTERMOST_TRUSTED_BOTS" env-separator:","`
	// MaxBotChainDepth caps consecutive bot replies within a single thread before
	// the bot stops replying, breaking LLM-bot ping-pong loops. <= 0 uses a
	// built-in default. An authorized human posting in the thread resets the count.
	MaxBotChainDepth int `yaml:"max_bot_chain_depth" env:"LAPLACED_MATTERMOST_MAX_BOT_CHAIN_DEPTH"`
}

type LLMConfig struct {
	APIKey string `yaml:"api_key" env:"LAPLACED_LLM_API_KEY"`
	// BaseURL is the OpenAI-compatible endpoint the LLM client talks to.
	// Defaults to the public OpenRouter API; override to point at a self-hosted
	// OpenAI-compatible backend (litellm, vLLM, …).
	BaseURL string `yaml:"base_url" env:"LAPLACED_LLM_BASE_URL"`
	// ImageInputFormat selects how images/videos are encoded as LLM content
	// parts: "file" (default, OpenRouter/Gemini) or "openai" (image_url/video_url,
	// required by OpenAI-compatible backends like litellm/vLLM which reject "file").
	ImageInputFormat string                `yaml:"image_input_format" env:"LAPLACED_LLM_IMAGE_INPUT_FORMAT"`
	ProxyURL         string                `yaml:"proxy_url" env:"LAPLACED_LLM_PROXY_URL"`
	PDFParserEngine  string                `yaml:"pdf_parser_engine"`
	RequestCost      float64               `yaml:"request_cost"`
	PriceTiers       []PriceTier           `yaml:"price_tiers"`
	Provider         ProviderRoutingConfig `yaml:"provider"`
}

// ProviderRoutingConfig configures OpenRouter provider preference.
// See https://openrouter.ai/docs/features/provider-routing for semantics.
// When Order is empty, no routing header is sent — OpenRouter picks freely.
// AllowFallbacks is a pointer: unset means "use OpenRouter default (true)";
// explicit false means "never fall back outside the order list".
type ProviderRoutingConfig struct {
	Order []string `yaml:"order" env:"LAPLACED_LLM_PROVIDER_ORDER" env-separator:","`
	// AllowFallbacks is YAML-only: cleanenv doesn't support *bool via env tags.
	// The common case (prefer a provider with default fallback) needs only Order,
	// so this is not a practical limitation.
	AllowFallbacks *bool `yaml:"allow_fallbacks"`
}

// ToRouting converts the YAML config to an llm.ProviderRouting pointer.
// Returns nil when no preference is configured, so the client stays on
// OpenRouter's default behavior.
func (c ProviderRoutingConfig) ToRouting() *llm.ProviderRouting {
	if len(c.Order) == 0 && c.AllowFallbacks == nil {
		return nil
	}
	return &llm.ProviderRouting{
		Order:          c.Order,
		AllowFallbacks: c.AllowFallbacks,
	}
}

// EmbeddingConfig defines embedding model settings.
type EmbeddingConfig struct {
	Model      string `yaml:"model" env:"LAPLACED_EMBEDDING_MODEL"`
	Dimensions int    `yaml:"dimensions" env:"LAPLACED_EMBEDDING_DIMENSIONS"`
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

// GetMinSafetyThreshold returns the minimum cosine similarity for vector search.
// Falls back to DefaultMinSafetyThreshold (0.1) if not configured.
// This relaxed threshold prioritizes recall over precision.
func (c *RAGConfig) GetMinSafetyThreshold() float64 {
	if c.MinSafetyThreshold > 0 {
		return c.MinSafetyThreshold
	}
	return DefaultMinSafetyThreshold
}

// GetConsolidationThreshold returns the minimum similarity for topic consolidation.
// Falls back to DefaultConsolidationThreshold (0.75) if not configured.
// This strict threshold ensures we only merge very similar topics.
func (c *RAGConfig) GetConsolidationThreshold() float64 {
	if c.ConsolidationSimilarityThreshold > 0 {
		return c.ConsolidationSimilarityThreshold
	}
	return DefaultConsolidationThreshold
}

// GetRetrievedTopicsCount returns the max topics to retrieve without reranker.
// Falls back to DefaultRetrievedTopicsCount (10) if not configured.
func (c *RAGConfig) GetRetrievedTopicsCount() int {
	if c.RetrievedTopicsCount > 0 {
		return c.RetrievedTopicsCount
	}
	return DefaultRetrievedTopicsCount
}

// GetMaxMergedSizeChars returns the max character count for merged topics.
// Falls back to DefaultMaxMergedSizeChars (50000) if not configured.
func (c *RAGConfig) GetMaxMergedSizeChars() int {
	if c.MaxMergedSizeChars > 0 {
		return c.MaxMergedSizeChars
	}
	return DefaultMaxMergedSizeChars
}

// GetMaxChunkSize returns the max messages per chunk before forced split.
// Falls back to DefaultMaxChunkSize (400) if not configured.
func (c *RAGConfig) GetMaxChunkSize() int {
	if c.MaxChunkSize > 0 {
		return c.MaxChunkSize
	}
	return DefaultMaxChunkSize
}

// GetFactDefaultImportance returns the default importance for facts without explicit importance.
// Falls back to DefaultFactDefaultImportance (50) if not configured.
func (c *MemoryConfig) GetFactDefaultImportance() int {
	if c.FactDefaultImportance > 0 {
		return c.FactDefaultImportance
	}
	return DefaultFactDefaultImportance
}

// GetPeopleSimilarityThreshold returns the minimum similarity for people vector search.
// Falls back to DefaultPeopleSimilarityThreshold (0.3) if not configured.
func (c *SearchConfig) GetPeopleSimilarityThreshold() float64 {
	if c.PeopleSimilarityThreshold > 0 {
		return c.PeopleSimilarityThreshold
	}
	return DefaultPeopleSimilarityThreshold
}

// GetPeopleMaxResults returns the max results for people vector search.
// Falls back to DefaultPeopleMaxResults (5) if not configured.
func (c *SearchConfig) GetPeopleMaxResults() int {
	if c.PeopleMaxResults > 0 {
		return c.PeopleMaxResults
	}
	return DefaultPeopleMaxResults
}

// TelemetryConfig defines configuration for OpenTelemetry export (traces first,
// metrics and logs as subsequent iterations). Disabled by default — callers
// must flip `enabled: true` (or set LAPLACED_TELEMETRY_ENABLED=true) to start
// an exporter. When disabled, the global OTel provider stays no-op.
type TelemetryConfig struct {
	Enabled bool `yaml:"enabled" env:"LAPLACED_TELEMETRY_ENABLED"`
	// Exporter selects the span exporter backend. Valid values: "otlp"
	// (default, sends to an OTLP/gRPC collector like Alloy) and "stdout"
	// (pretty-prints spans to stderr; for local dev where network-level
	// trace delivery is not being tested).
	Exporter     string `yaml:"exporter" env:"LAPLACED_TELEMETRY_EXPORTER"`
	OTLPEndpoint string `yaml:"otlp_endpoint" env:"LAPLACED_TELEMETRY_OTLP_ENDPOINT"`
	ServiceName  string `yaml:"service_name" env:"LAPLACED_TELEMETRY_SERVICE_NAME"`
	// TraceContent, when true, asks tracing to record full content (LLM
	// request/response bodies, raw RAG queries, tool args/results) as span
	// events. Default off — flip only for debug sessions. Also surfaces as
	// resource attribute laplaced.trace_content on the captured trace.
	TraceContent bool `yaml:"trace_content" env:"LAPLACED_TELEMETRY_TRACE_CONTENT"`
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
	// Transport selects the chat backend: "telegram" (default) | "mattermost".
	Transport string `yaml:"transport" env:"LAPLACED_TRANSPORT"`
	Telegram  struct {
		Token         string `yaml:"token" env:"LAPLACED_TELEGRAM_TOKEN"`
		WebhookURL    string `yaml:"webhook_url" env:"LAPLACED_TELEGRAM_WEBHOOK_URL"`
		WebhookPath   string // Auto-generated from token hash (not configurable)
		WebhookSecret string // Auto-generated from token hash (not configurable)
		ProxyURL      string `yaml:"proxy_url" env:"LAPLACED_TELEGRAM_PROXY_URL"`
	} `yaml:"telegram"`
	Mattermost MattermostConfig `yaml:"mattermost"`
	LLM        LLMConfig        `yaml:"llm"`
	Agents     AgentsConfig     `yaml:"agents"`
	Embedding  EmbeddingConfig  `yaml:"embedding"`
	RAG        RAGConfig        `yaml:"rag"`
	Tools      []ToolConfig     `yaml:"tools"`
	Fetcher    FetcherConfig    `yaml:"fetcher"`
	Bot        BotConfig        `yaml:"bot"`
	Database   struct {
		// Driver selects the storage backend: "sqlite" (default) or "postgres".
		// Empty defaults to sqlite for backward compatibility.
		Driver string `yaml:"driver" env:"LAPLACED_DATABASE_DRIVER"`
		// Path is the SQLite database file path (driver=sqlite).
		Path string `yaml:"path" env:"LAPLACED_DATABASE_PATH"`
		// Postgres holds the connection params for driver=postgres. The password
		// MUST come from the LAPLACED_DATABASE_PASSWORD env var, never a committed literal.
		Postgres struct {
			Host     string `yaml:"host" env:"LAPLACED_DATABASE_HOST"`
			Port     int    `yaml:"port" env:"LAPLACED_DATABASE_PORT"`
			Database string `yaml:"database" env:"LAPLACED_DATABASE_NAME"`
			User     string `yaml:"user" env:"LAPLACED_DATABASE_USER"`
			Password string `yaml:"password" env:"LAPLACED_DATABASE_PASSWORD"`
			SSLMode  string `yaml:"sslmode" env:"LAPLACED_DATABASE_SSLMODE"`
		} `yaml:"postgres"`
	} `yaml:"database"`
	Artifacts ArtifactsConfig `yaml:"artifacts"`
	Memory    MemoryConfig    `yaml:"memory"`
	Search    SearchConfig    `yaml:"search"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
	// Vault, when present, enables pulling secrets from HashiCorp Vault. Absent
	// (nil) = secrets come only from literals / LAPLACED_* env vars (default).
	Vault *VaultConfig `yaml:"vault"`
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

	// Apply deprecated LAPLACED_OPENROUTER_* env vars before cleanenv so the
	// current LAPLACED_LLM_* names (applied next) take precedence when both
	// are set.
	applyDeprecatedLLMEnvVars(&cfg)

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

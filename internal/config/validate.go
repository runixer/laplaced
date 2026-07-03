package config

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// Validate checks configuration for required fields and valid ranges.
// It accumulates all validation failures across sections and returns them
// joined; nil means the config is valid.
//
// It also applies the v0.6.0 reranker config migration (see legacy.go)
// before validating that section, so callers get a normalized config.
func (c *Config) Validate() error {
	c.applyRerankerLegacyDefaults()

	var errs []error
	errs = append(errs, c.validateTransport()...)
	errs = append(errs, c.validateLLM()...)
	errs = append(errs, c.validateVault()...)
	errs = append(errs, c.validateDatabase()...)
	errs = append(errs, c.validateFetcher()...)
	errs = append(errs, c.validateServer()...)
	errs = append(errs, c.validateRAG()...)
	errs = append(errs, c.validateAgents()...)
	errs = append(errs, c.validateEmbedding()...)
	errs = append(errs, c.validateReranker()...)
	errs = append(errs, c.validateBot()...)
	errs = append(errs, c.validateArtifacts()...)
	errs = append(errs, c.validateTelemetry()...)
	errs = append(errs, c.validateImageGenerator()...)
	return errors.Join(errs...)
}

// validateTransport checks transport-specific required fields.
func (c *Config) validateTransport() []error {
	var errs []error
	if c.Transport == "mattermost" {
		if c.Mattermost.ServerURL == "" {
			errs = append(errs, errors.New("mattermost.server_url is required when transport is \"mattermost\""))
		}
		if c.Mattermost.BotToken == "" {
			errs = append(errs, errors.New("mattermost.bot_token is required when transport is \"mattermost\""))
		}
	} else if c.Telegram.Token == "" {
		errs = append(errs, errors.New("telegram.token is required"))
	}
	// A principal resolver only makes sense for the transport that owns its trust
	// source (Mattermost auth_service). Catch a misplaced block at load time
	// instead of silently never resolving.
	if c.Mattermost.PrincipalResolver != nil && c.Transport != "mattermost" {
		errs = append(errs, errors.New("mattermost.principal_resolver requires transport \"mattermost\""))
	}
	return errs
}

func (c *Config) validateLLM() []error {
	if c.LLM.APIKey == "" {
		return []error{errors.New("llm.api_key is required")}
	}
	return nil
}

func (c *Config) validateVault() []error {
	if c.Vault == nil {
		return nil
	}
	return c.Vault.validate()
}

func (c *Config) validateDatabase() []error {
	var errs []error
	switch c.Database.Driver {
	case "", "sqlite":
		if c.Database.Path == "" {
			errs = append(errs, errors.New("database.path is required"))
		}
	case "postgres":
		if c.Database.Postgres.Host == "" {
			errs = append(errs, errors.New("database.postgres.host is required when driver is \"postgres\""))
		}
		if c.Database.Postgres.Database == "" {
			errs = append(errs, errors.New("database.postgres.database is required when driver is \"postgres\""))
		}
		if c.Database.Postgres.User == "" {
			errs = append(errs, errors.New("database.postgres.user is required when driver is \"postgres\""))
		}
	default:
		errs = append(errs, fmt.Errorf("database.driver %q is not supported (use \"sqlite\" or \"postgres\")", c.Database.Driver))
	}
	return errs
}

// validateFetcher checks the fetcher backend, but only when the read_url tool
// is exposed; catch a typo'd backend at load time instead of at first tool
// call. The API key is deliberately NOT required here — it may be an
// unresolved vault: ref (S3 precedent), and a missing key just leaves the
// tool unwired.
func (c *Config) validateFetcher() []error {
	for _, tool := range c.Tools {
		if tool.Name == "read_url" {
			if c.Fetcher.Backend != "firecrawl" && c.Fetcher.Backend != "raw" {
				return []error{fmt.Errorf("fetcher.backend %q is not supported (use \"firecrawl\" or \"raw\")", c.Fetcher.Backend)}
			}
			break
		}
	}
	return nil
}

// validateServer checks server auth: username is required if enabled
// (password is auto-generated if not set).
func (c *Config) validateServer() []error {
	if c.Server.Auth.Enabled && c.Server.Auth.Username == "" {
		return []error{errors.New("server.auth.username is required when server.auth.enabled is true")}
	}
	return nil
}

func (c *Config) validateRAG() []error {
	if !c.RAG.Enabled {
		return nil
	}
	var errs []error

	// Thresholds must be in range [0, 1]
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
	return errs
}

func (c *Config) validateAgents() []error {
	var errs []error
	if c.Agents.Default.Model == "" {
		errs = append(errs, errors.New("agents.default.model is required"))
	}
	if c.Agents.Chat.Name == "" {
		errs = append(errs, errors.New("agents.chat.name is required"))
	}
	if c.Agents.Chat.ThinkingLevel != "" && !validThinkingLevels[c.Agents.Chat.ThinkingLevel] {
		errs = append(errs, fmt.Errorf("agents.chat.thinking_level: must be one of 'auto', 'off', 'minimal', 'low', 'medium', 'high', got %q", c.Agents.Chat.ThinkingLevel))
	}
	return errs
}

func (c *Config) validateEmbedding() []error {
	if c.Embedding.Model == "" {
		return []error{errors.New("embedding.model is required")}
	}
	return nil
}

// validateReranker expects the legacy field migration to have already run
// (applyRerankerLegacyDefaults in legacy.go).
func (c *Config) validateReranker() []error {
	r := &c.Agents.Reranker
	var errs []error
	if r.Topics.CandidatesLimit <= 0 {
		errs = append(errs, fmt.Errorf("agents.reranker.topics.candidates_limit must be positive, got %d", r.Topics.CandidatesLimit))
	}
	if r.Topics.Max <= 0 {
		errs = append(errs, fmt.Errorf("agents.reranker.topics.max must be positive, got %d", r.Topics.Max))
	}
	if r.People.CandidatesLimit <= 0 {
		errs = append(errs, fmt.Errorf("agents.reranker.people.candidates_limit must be positive, got %d", r.People.CandidatesLimit))
	}
	if r.People.Max <= 0 {
		errs = append(errs, fmt.Errorf("agents.reranker.people.max must be positive, got %d", r.People.Max))
	}
	if r.Artifacts.CandidatesLimit <= 0 {
		errs = append(errs, fmt.Errorf("agents.reranker.artifacts.candidates_limit must be positive, got %d", r.Artifacts.CandidatesLimit))
	}
	if r.Artifacts.Max <= 0 {
		errs = append(errs, fmt.Errorf("agents.reranker.artifacts.max must be positive, got %d", r.Artifacts.Max))
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
	if r.ThinkingLevel != "" && !validThinkingLevels[r.ThinkingLevel] {
		errs = append(errs, fmt.Errorf("agents.reranker.thinking_level: must be one of 'auto', 'off', 'minimal', 'low', 'medium', 'high', got %q", r.ThinkingLevel))
	}
	return errs
}

func (c *Config) validateBot() []error {
	if c.Bot.TurnWaitDuration != "" {
		if _, err := time.ParseDuration(c.Bot.TurnWaitDuration); err != nil {
			return []error{fmt.Errorf("bot.turn_wait_duration: invalid duration format %q: %w", c.Bot.TurnWaitDuration, err)}
		}
	}
	return nil
}

func (c *Config) validateArtifacts() []error {
	if !c.Artifacts.Enabled {
		return nil
	}
	var errs []error
	// storage_path backs the local disk store; an S3 block supersedes it.
	if c.Artifacts.StoragePath == "" && c.Artifacts.S3 == nil {
		errs = append(errs, errors.New("artifacts.storage_path is required when artifacts.enabled is true (or configure artifacts.s3)"))
	}
	if len(c.Artifacts.AllowedTypes) == 0 {
		errs = append(errs, errors.New("artifacts.allowed_types must not be empty"))
	}
	if c.Artifacts.S3 != nil {
		errs = append(errs, c.Artifacts.S3.validate()...)
	}
	// The extractor agent only runs when artifacts are enabled.
	if c.Agents.Extractor.MaxFileSizeMB <= 0 {
		errs = append(errs, fmt.Errorf("agents.extractor.max_file_size_mb must be positive, got %d", c.Agents.Extractor.MaxFileSizeMB))
	}
	return errs
}

// validateTelemetry: requirements depend on the chosen exporter.
func (c *Config) validateTelemetry() []error {
	if !c.Telemetry.Enabled {
		return nil
	}
	switch c.Telemetry.Exporter {
	case "", "otlp":
		if c.Telemetry.OTLPEndpoint == "" {
			return []error{errors.New("telemetry.otlp_endpoint is required when telemetry.enabled=true and exporter=otlp")}
		}
	case "stdout":
		// No extra requirements — writes to stderr.
	default:
		return []error{fmt.Errorf("telemetry.exporter must be one of 'otlp' or 'stdout', got %q", c.Telemetry.Exporter)}
	}
	return nil
}

// validateImageGenerator checks that defaults are inside the supported lists.
// This catches the misconfig "swapped Model but forgot to narrow
// supported_image_sizes", which would otherwise surface as a runtime 400 from
// the upstream provider.
func (c *Config) validateImageGenerator() []error {
	ig := &c.Agents.ImageGenerator
	var errs []error
	if ig.DefaultImageSize != "" && len(ig.SupportedImageSizes) > 0 && !slices.Contains(ig.SupportedImageSizes, ig.DefaultImageSize) {
		errs = append(errs, fmt.Errorf("agents.image_generator.default_image_size %q is not in supported_image_sizes %v", ig.DefaultImageSize, ig.SupportedImageSizes))
	}
	if ig.DefaultAspectRatio != "" && len(ig.SupportedAspectRatios) > 0 && !slices.Contains(ig.SupportedAspectRatios, ig.DefaultAspectRatio) {
		errs = append(errs, fmt.Errorf("agents.image_generator.default_aspect_ratio %q is not in supported_aspect_ratios %v", ig.DefaultAspectRatio, ig.SupportedAspectRatios))
	}
	return errs
}

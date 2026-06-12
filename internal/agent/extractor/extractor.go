package extractor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/prompts"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/storage"
)

// Request parameters for Extractor agent.
const (
	// ParamArtifact is the key for artifact to process (*storage.Artifact).
	ParamArtifact = "artifact"
)

// ExtractionResult contains the structured output from Gemini Flash.
// This is metadata-only output (~500 tokens) - no full text extraction.
type ExtractionResult struct {
	Summary  string   `json:"summary"`   // 2-4 sentence description for semantic search
	Keywords []string `json:"keywords"`  // 5-10 tags for discovery
	Entities []string `json:"entities"`  // Named entities (people, companies, code mentioned)
	RAGHints []string `json:"rag_hints"` // Questions this file might answer
}

// ProcessResult contains the final output of extraction.
type ProcessResult struct {
	ArtifactID int64
	Summary    string
	Keywords   []string
	Entities   []string
	RAGHints   []string
	Duration   time.Duration
	Tokens     agent.TokenUsage
}

// Extractor processes artifact files using multimodal LLM.
// Generates metadata-only output (summary, keywords, entities, rag_hints).
type Extractor struct {
	executor     *agent.Executor
	translator   *i18n.Translator
	cfg          *config.Config
	logger       *slog.Logger
	fileStorage  files.Storage
	llmClient    llm.Client
	artifactRepo storage.ArtifactRepository
}

// New creates a new Extractor agent.
func New(
	executor *agent.Executor,
	translator *i18n.Translator,
	cfg *config.Config,
	logger *slog.Logger,
	fileStorage files.Storage,
	llmClient llm.Client,
	artifactRepo storage.ArtifactRepository,
) *Extractor {
	return &Extractor{
		executor:     executor,
		translator:   translator,
		cfg:          cfg,
		logger:       logger.With("component", "extractor"),
		fileStorage:  fileStorage,
		llmClient:    llmClient,
		artifactRepo: artifactRepo,
	}
}

// Type returns the agent type.
func (ex *Extractor) Type() agent.AgentType {
	return agent.TypeExtractor
}

// Capabilities returns the agent's capabilities.
func (ex *Extractor) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		IsAgentic:      false, // Single-shot LLM call
		OutputFormat:   "json",
		SupportedMedia: []string{"image", "audio", "pdf", "video_note"},
	}
}

// Description returns a human-readable description.
func (ex *Extractor) Description() string {
	return "Processes artifact files and extracts structured content"
}

// Execute runs the extractor with the given request.
func (ex *Extractor) Execute(ctx context.Context, req *agent.Request) (response *agent.Response, err error) {
	// Extract artifact from request
	artifact := ex.getArtifact(req)
	if artifact == nil {
		return nil, fmt.Errorf("no artifact provided")
	}

	userID := artifact.UserID
	artifactID := artifact.ID

	ex.logger.Info("extracting artifact metadata",
		"user_id", userID,
		"artifact_id", artifactID,
		"file_type", artifact.FileType,
		"size", artifact.FileSize,
	)

	// Span boundary so artifact extraction failures (the JSON parse case we
	// saw on artifact 1768 is invisible today) become queryable via
	// `{ span.extractor.parse_error = true }`.
	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/agent/extractor").Start(
		ctx, "extractor.Execute",
		trace.WithAttributes(
			attribute.String("user.id", string(userID)),
			attribute.Int64("extractor.artifact_id", artifactID),
			attribute.String("extractor.file_type", artifact.FileType),
			attribute.Int64("extractor.file_size_bytes", artifact.FileSize),
		),
	)
	var (
		parseError      bool
		embeddingFailed bool
		jsonRepaired    bool
		emptyRetry      bool
	)
	defer func() {
		span.SetAttributes(
			attribute.Bool("extractor.parse_error", parseError),
			attribute.Bool("extractor.embedding_failed", embeddingFailed),
			attribute.Bool("extractor.json_repaired", jsonRepaired),
			attribute.Bool("extractor.empty_retry", emptyRetry),
		)
		_ = obs.ObserveErr(span, err)
		span.End()
	}()

	startTime := time.Now()

	// Step 1: Validate file size
	maxSizeMB := ex.cfg.Agents.Extractor.MaxFileSizeMB
	maxSizeBytes := int64(maxSizeMB) * 1024 * 1024

	if artifact.FileSize == 0 {
		err := fmt.Errorf("empty file: cannot process zero-size artifact")
		ex.markFailed(artifact, err.Error())
		return nil, err
	}

	if artifact.FileSize > maxSizeBytes {
		err := fmt.Errorf("file too large for extraction: %d bytes > %d MB",
			artifact.FileSize, maxSizeMB)
		ex.markFailed(artifact, err.Error())
		return nil, err
	}

	// Step 2: Update state to 'processing'
	artifact.State = "processing"
	if err := ex.artifactRepo.UpdateArtifact(*artifact); err != nil {
		return nil, fmt.Errorf("failed to update artifact state: %w", err)
	}

	// Step 3: Read file from storage
	fileData, err := ex.fileStorage.ReadFile(ctx, artifact.FilePath)
	if err != nil {
		err := fmt.Errorf("failed to read file: %w", err)
		ex.markFailed(artifact, err.Error())
		return nil, err
	}

	// Step 4: Build multimodal messages with file content
	messages, err := ex.buildMultimodalMessages(ctx, req, artifact, fileData)
	if err != nil {
		ex.markFailed(artifact, err.Error())
		return nil, fmt.Errorf("failed to build messages: %w", err)
	}

	// Step 5: Call extraction model from config (Flash for metadata)
	model := ex.cfg.Agents.Extractor.GetModel("google/gemini-3-flash-preview")

	llmReq := agent.SingleShotRequest{
		AgentType: agent.TypeExtractor,
		UserID:    userID,
		Model:     model,
		Messages:  messages,
		JSONMode:  true,
	}

	llmResp, err := ex.executor.ExecuteSingleShot(ctx, llmReq)
	if err != nil {
		err := fmt.Errorf("LLM call failed: %w", err)
		ex.markFailed(artifact, err.Error())
		return nil, err
	}

	// Step 6: Parse extraction result (metadata only)
	emptyRetry, _ = llmResp.Metadata["empty_retry"].(bool)
	var extraction ExtractionResult
	jsonRepaired, err = agent.UnmarshalLenient(llmResp.Content, &extraction)
	if err != nil {
		parseError = true
		err = fmt.Errorf("failed to parse extraction JSON: %w", err)
		ex.markFailed(artifact, err.Error())
		return nil, err
	}

	// Step 7: Generate embedding for summary only (for vector search)
	summaryEmbedding, err := ex.generateSummaryEmbedding(ctx, userID, extraction.Summary)
	if err != nil {
		embeddingFailed = true
		err = fmt.Errorf("failed to generate summary embedding: %w", err)
		ex.markFailed(artifact, err.Error())
		return nil, err
	}

	// Step 8: Update artifact with metadata (no full_text, no chunks)
	keywordsJSON, _ := json.Marshal(extraction.Keywords)
	entitiesJSON, _ := json.Marshal(extraction.Entities)
	ragHintsJSON, _ := json.Marshal(extraction.RAGHints)

	now := time.Now()
	summary := extraction.Summary
	keywords := string(keywordsJSON)
	entities := string(entitiesJSON)
	ragHints := string(ragHintsJSON)

	artifact.Summary = &summary
	artifact.Keywords = &keywords
	artifact.Embedding = summaryEmbedding
	// New fields
	artifact.Entities = &entities
	artifact.RAGHints = &ragHints
	// Reset retry tracking on success (v0.6.0 - CRIT-3)
	artifact.RetryCount = 0
	artifact.LastFailedAt = nil
	artifact.State = "ready"
	artifact.ProcessedAt = &now

	if err := ex.artifactRepo.UpdateArtifact(*artifact); err != nil {
		ex.markFailed(artifact, err.Error())
		return nil, fmt.Errorf("failed to update artifact: %w", err)
	}

	duration := time.Since(startTime)

	ex.logger.Info("artifact metadata extraction complete",
		"user_id", userID,
		"artifact_id", artifactID,
		"duration_ms", duration.Milliseconds(),
		"tokens", llmResp.Tokens.TotalTokens(),
	)

	// Return structured response
	result := &ProcessResult{
		ArtifactID: artifactID,
		Summary:    extraction.Summary,
		Keywords:   extraction.Keywords,
		Entities:   extraction.Entities,
		RAGHints:   extraction.RAGHints,
		Duration:   duration,
		Tokens:     llmResp.Tokens,
	}

	return &agent.Response{
		Content:    llmResp.Content,
		Structured: result,
		Duration:   duration,
		Tokens:     llmResp.Tokens,
		Metadata: map[string]any{
			"artifact_id": artifactID,
		},
	}, nil
}

// generateSummaryEmbedding creates embedding for summary text for vector search.
func (ex *Extractor) generateSummaryEmbedding(
	ctx context.Context,
	userID storage.ScopeID,
	summary string,
) ([]float32, error) {
	embeddingModel := ex.cfg.Embedding.Model

	embReq := llm.EmbeddingRequest{
		Model:      embeddingModel,
		Input:      []string{summary},
		Dimensions: ex.cfg.Embedding.Dimensions,
		LogMeta: map[string]any{
			"user_id": userID,
			"purpose": "artifact_summary",
		},
	}

	embResp, err := ex.llmClient.CreateEmbeddings(ctx, embReq)
	if err != nil {
		return nil, fmt.Errorf("embedding API call failed: %w", err)
	}

	if len(embResp.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}

	return embResp.Data[0].Embedding, nil
}

// getContext returns profile, recent topics, and inner circle from SharedContext.
// Returns empty defaults if SharedContext is not available (for tests/background jobs).
func (ex *Extractor) getContext(ctx context.Context, req *agent.Request) (profile, recentTopics, innerCircle string) {
	return agent.GetSharedContext(ctx, req)
}

// buildMultimodalMessages constructs multimodal messages with file content for the LLM.
func (ex *Extractor) buildMultimodalMessages(ctx context.Context, req *agent.Request, artifact *storage.Artifact, fileData []byte) ([]llm.Message, error) {
	// Get user context for personalized extraction
	profile, recentTopics, innerCircle := ex.getContext(ctx, req)

	// System prompt
	systemPrompt, err := ex.translator.GetTemplate(
		ex.cfg.Bot.Language,
		"extractor.system_prompt",
		prompts.ExtractorParams{
			BotName:      ex.cfg.Agents.Chat.Name,
			Profile:      profile,
			RecentTopics: recentTopics,
			InnerCircle:  innerCircle,
		},
	)
	if err != nil {
		return nil, err
	}

	// Format user context with XML tags (v0.6.0)
	var userContext string
	if artifact.UserContext != nil && *artifact.UserContext != "" {
		userContext = fmt.Sprintf(
			"\n\n<user_context>\n%s\n</user_context>",
			xmlEscape(*artifact.UserContext),
		)
	}

	// User prompt with file metadata
	userPrompt, err := ex.translator.GetTemplate(
		ex.cfg.Bot.Language,
		"extractor.user_prompt",
		prompts.ExtractorUserParams{
			OriginalName: artifact.OriginalName,
			FileType:     artifact.FileType,
			MimeType:     artifact.MimeType,
			FileSize:     artifact.FileSize,
			UserContext:  userContext,
		},
	)
	if err != nil {
		return nil, err
	}

	// Build content parts with file data
	base64Data := base64.StdEncoding.EncodeToString(fileData)

	var contentParts []interface{}

	// Add file part based on type. Visual media (image/video) is encoded per the
	// configured backend format via llm.MediaPart (image_url/video_url on
	// OpenAI-compatible backends, else `file`); other types use `file`.
	format := ex.cfg.LLM.ImageInputFormat
	mediaPart := func(mimeType, fileName string) interface{} {
		return llm.MediaPart(format, mimeType, fileName, fmt.Sprintf("data:%s;base64,%s", mimeType, base64Data))
	}
	switch artifact.FileType {
	case "image", "photo":
		mimeType := artifact.MimeType
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
		fileName := artifact.OriginalName
		if fileName == "" {
			fileName = fmt.Sprintf("image_%d", artifact.ID)
		}
		contentParts = append(contentParts, mediaPart(mimeType, fileName))

	case "pdf":
		fileName := artifact.OriginalName
		if fileName == "" {
			fileName = fmt.Sprintf("document_%d.pdf", artifact.ID)
		}
		contentParts = append(contentParts, mediaPart("application/pdf", fileName))

	case "voice", "audio", "video_note", "video":
		mimeType := artifact.MimeType
		if mimeType == "" {
			// Fallback mime types based on file type
			switch artifact.FileType {
			case "voice", "audio":
				mimeType = "audio/ogg"
			case "video_note", "video":
				mimeType = "video/mp4"
			}
		}

		// Build display filename
		fileName := artifact.OriginalName
		if fileName == "" {
			fileName = fmt.Sprintf("artifact_%d", artifact.ID)
		}

		contentParts = append(contentParts, mediaPart(mimeType, fileName))

	case "document":
		// For text documents, include content as text
		contentParts = append(contentParts, llm.TextPart{
			Type: "text",
			Text: string(fileData),
		})

	default:
		mimeType := artifact.MimeType
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		fileName := artifact.OriginalName
		if fileName == "" {
			fileName = fmt.Sprintf("artifact_%d", artifact.ID)
		}
		contentParts = append(contentParts, mediaPart(mimeType, fileName))
	}

	// Add user prompt text
	contentParts = append(contentParts, llm.TextPart{
		Type: "text",
		Text: userPrompt,
	})

	// Build messages
	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: contentParts},
	}

	return messages, nil
}

// markFailed updates artifact state to 'failed' with error message.
// Increments retry count and records failure timestamp (v0.6.0 - CRIT-3).
func (ex *Extractor) markFailed(artifact *storage.Artifact, errorMsg string) {
	ex.logger.Warn("artifact extraction failed",
		"user_id", artifact.UserID,
		"artifact_id", artifact.ID,
		"retry_count", artifact.RetryCount,
		"error", errorMsg,
	)

	artifact.State = "failed"
	artifact.ErrorMessage = &errorMsg
	// Increment retry tracking (v0.6.0 - CRIT-3)
	artifact.RetryCount++
	now := time.Now()
	artifact.LastFailedAt = &now
	artifact.ProcessedAt = &now

	if err := ex.artifactRepo.UpdateArtifact(*artifact); err != nil {
		ex.logger.Error("failed to mark artifact as failed",
			"artifact_id", artifact.ID,
			"error", err,
		)
	}
}

// getArtifact extracts artifact from request params.
func (ex *Extractor) getArtifact(req *agent.Request) *storage.Artifact {
	if req.Params == nil {
		return nil
	}
	if artifact, ok := req.Params[ParamArtifact].(*storage.Artifact); ok {
		return artifact
	}
	return nil
}

// xmlEscape escapes special XML characters (v0.6.0).
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

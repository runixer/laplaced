package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

// CallContext carries execution context needed by individual tool handlers.
// Some tools (generate_image) need the user's current-message image parts;
// all tools need UserID for data isolation.
type CallContext struct {
	UserID int64
	// CurrentMessageImages is the image FileParts attached to the current
	// user message. generate_image uses these as default input when the LLM
	// does not pass explicit artifact IDs.
	CurrentMessageImages []openrouter.FilePart
}

// Result is the richer return type of tool execution. Content is what gets
// fed back to the LLM; GeneratedArtifactIDs are artifact IDs produced
// during the call (e.g. by generate_image) for the orchestrator to surface
// in the final user reply.
type Result struct {
	Content              string
	GeneratedArtifactIDs []int64
}

// ToolExecutor handles tool execution for the bot.
// It dispatches tool calls to appropriate handlers and manages
// access to repositories and services.
type ToolExecutor struct {
	orClient        openrouter.Client
	factRepo        storage.FactRepository
	factHistoryRepo storage.FactHistoryRepository
	peopleRepo      storage.PeopleRepository
	ragService      *rag.Service
	cfg             *config.Config
	logger          *slog.Logger
	agentLogger     *agentlog.Logger // Optional: for Scout agent logging

	// Image generation dependencies (wired via setters when the image
	// generator is enabled; nil otherwise → generate_image tool returns
	// a configuration error instead of silently failing).
	imageGen     ImageGenerator
	artifactRepo storage.ArtifactRepository
	fileStorage  *files.FileStorage
}

// ImageGenerator is the narrow interface performImageGeneration needs from
// the imagegen agent. Kept internal here so tests can substitute without
// pulling the agent package.
type ImageGenerator interface {
	Generate(ctx context.Context, req ImageGenRequest) (*ImageGenResponse, error)
}

// ImageGenRequest mirrors imagegen.Request using types local to this
// package. The bot-level wiring adapts between this type and the agent's
// own Request type.
type ImageGenRequest struct {
	UserID      int64
	Prompt      string
	InputImages []openrouter.FilePart
	AspectRatio string
	ImageSize   string
}

// ImageGenResponse mirrors imagegen.Response.
type ImageGenResponse struct {
	Images      []ImageGenImage
	TextContent string
}

// ImageGenImage is a single generated image (mirrors imagegen.DecodedImage).
type ImageGenImage struct {
	MimeType string
	Data     []byte
}

// NewToolExecutor creates a new ToolExecutor with required dependencies.
func NewToolExecutor(
	orClient openrouter.Client,
	factRepo storage.FactRepository,
	factHistoryRepo storage.FactHistoryRepository,
	cfg *config.Config,
	logger *slog.Logger,
) *ToolExecutor {
	return &ToolExecutor{
		orClient:        orClient,
		factRepo:        factRepo,
		factHistoryRepo: factHistoryRepo,
		cfg:             cfg,
		logger:          logger.With("component", "tool_executor"),
	}
}

// SetPeopleRepository sets the optional people repository.
func (e *ToolExecutor) SetPeopleRepository(repo storage.PeopleRepository) {
	e.peopleRepo = repo
}

// SetRAGService sets the optional RAG service for search tools.
func (e *ToolExecutor) SetRAGService(svc *rag.Service) {
	e.ragService = svc
}

// SetAgentLogger sets the optional agent logger for Scout logging.
func (e *ToolExecutor) SetAgentLogger(logger *agentlog.Logger) {
	e.agentLogger = logger
}

// SetImageGenerator wires the image-generation agent. Required for the
// generate_image tool; without it the tool returns a configuration error.
func (e *ToolExecutor) SetImageGenerator(gen ImageGenerator) {
	e.imageGen = gen
}

// SetArtifactRepository wires the artifact repository. Required for the
// generate_image tool (to persist generated images).
func (e *ToolExecutor) SetArtifactRepository(repo storage.ArtifactRepository) {
	e.artifactRepo = repo
}

// SetFileStorage wires the file storage. Required for the generate_image
// tool (to save output PNGs to disk with hashing/dedup).
func (e *ToolExecutor) SetFileStorage(fs *files.FileStorage) {
	e.fileStorage = fs
}

// ExecuteToolCall dispatches tool execution by name.
// Returns a Result with Content (fed back to the LLM) and any generated
// artifact IDs (for media-producing tools like generate_image).
func (e *ToolExecutor) ExecuteToolCall(ctx context.Context, cc CallContext, toolName string, arguments string) (result *Result, err error) {
	// One span per dispatched call. The tool.name attribute pivots across
	// all six handlers. When a handler makes its own downstream LLM call
	// (e.g. performModelTool → openrouter.CreateChatCompletion), the inner
	// span naturally nests here via ctx, which is the trace we want.
	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/bot/tools").Start(
		ctx, "tool_executor.ExecuteToolCall",
		trace.WithAttributes(
			attribute.String("tool.name", toolName),
			attribute.Int64("user.id", cc.UserID),
		),
	)
	defer func() {
		span.SetAttributes(attribute.Bool("tool.ok", err == nil))
		if arguments != "" {
			obs.RecordContent(span, "tool.args", arguments)
		}
		if result != nil && result.Content != "" {
			obs.RecordContent(span, "tool.result", result.Content)
		}
		_ = obs.ObserveErr(span, err)
		span.End()
	}()

	// Find tool config
	var matchedTool *config.ToolConfig
	for _, t := range e.cfg.Tools {
		if t.Name == toolName {
			matchedTool = &t
			break
		}
	}

	if matchedTool == nil {
		return nil, fmt.Errorf("unknown tool: %s", toolName)
	}

	// Parse arguments
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, fmt.Errorf("failed to parse arguments: %w", err)
	}

	// Execute based on tool name
	switch matchedTool.Name {
	case "search_history":
		e.logger.Info("Executing history search tool", "tool", matchedTool.Name, "query", args["query"])
		return textResult(e.performHistorySearch(ctx, cc.UserID, args))

	case "manage_memory":
		e.logger.Info("Executing Manage Memory tool", "tool", matchedTool.Name)
		return textResult(e.performManageMemory(ctx, cc.UserID, args))

	case "search_people":
		e.logger.Info("Executing search people tool", "tool", matchedTool.Name, "query", args["query"])
		return textResult(e.performSearchPeople(ctx, cc.UserID, args))

	case "manage_people":
		e.logger.Info("Executing manage people tool", "tool", matchedTool.Name)
		return textResult(e.performManagePeople(ctx, cc.UserID, args))

	case "generate_image":
		e.logger.Info("Executing image generation tool", "tool", matchedTool.Name, "prompt", args["prompt"])
		return e.performImageGeneration(ctx, cc, args)

	default:
		// Model tool (custom LLM)
		e.logger.Info("Executing model tool", "tool", matchedTool.Name, "model", matchedTool.Model, "query", args["query"])
		return textResult(e.performModelTool(ctx, cc.UserID, matchedTool.Model, args))
	}
}

// textResult wraps a (string, error) return into a (*Result, error) with no
// generated artifacts. Used by tools that only produce text output.
func textResult(content string, err error) (*Result, error) {
	if err != nil {
		return nil, err
	}
	return &Result{Content: content}, nil
}

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

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

// ExecuteToolCall dispatches tool execution by name.
func (e *ToolExecutor) ExecuteToolCall(ctx context.Context, userID int64, toolName string, arguments string) (string, error) {
	// Find tool config
	var matchedTool *config.ToolConfig
	for _, t := range e.cfg.Tools {
		if t.Name == toolName {
			matchedTool = &t
			break
		}
	}

	if matchedTool == nil {
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}

	// Parse arguments
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	// Execute based on tool name
	switch matchedTool.Name {
	case "search_history":
		e.logger.Info("Executing history search tool", "tool", matchedTool.Name, "query", args["query"])
		return e.performHistorySearch(ctx, userID, args)

	case "manage_memory":
		e.logger.Info("Executing Manage Memory tool", "tool", matchedTool.Name)
		return e.performManageMemory(ctx, userID, args)

	case "search_people":
		e.logger.Info("Executing search people tool", "tool", matchedTool.Name, "query", args["query"])
		return e.performSearchPeople(ctx, userID, args)

	case "manage_people":
		e.logger.Info("Executing manage people tool", "tool", matchedTool.Name)
		return e.performManagePeople(ctx, userID, args)

	default:
		// Model tool (custom LLM)
		e.logger.Info("Executing model tool", "tool", matchedTool.Name, "model", matchedTool.Model, "query", args["query"])
		return e.performModelTool(ctx, userID, matchedTool.Model, args)
	}
}

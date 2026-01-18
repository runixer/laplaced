package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/archivist"
	"github.com/runixer/laplaced/internal/agent/enricher"
	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/agent/merger"
	"github.com/runixer/laplaced/internal/agent/reranker"
	"github.com/runixer/laplaced/internal/agent/splitter"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/memory"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

// Services holds all initialized services and agents.
// This struct is returned by SetupServices to provide all dependencies
// in a single call, eliminating code duplication between main bot and testbot.
type Services struct {
	// All agents
	EnricherAgent  *enricher.Enricher
	SplitterAgent  *splitter.Splitter
	MergerAgent    *merger.Merger
	ArchivistAgent *archivist.Archivist
	RerankerAgent  *reranker.Reranker
	LaplaceAgent   *laplace.Laplace

	// Services
	MemoryService  *memory.Service
	RAGService     *rag.Service
	ContextService *agent.ContextService
	AgentLogger    *agentlog.Logger
	AgentExecutor  *agent.Executor
	Translator     *i18n.Translator
}

// SetupServices initializes all core services and agents.
// This function is called by both the main bot and testbot to eliminate
// ~150 lines of duplicated code.
//
// The caller is responsible for:
// - Starting the RAG service (ragService.Start(ctx))
// - Stopping services when done (ragService.Stop(), store.Close())
func SetupServices(
	ctx context.Context,
	logger *slog.Logger,
	cfg *config.Config,
	store *storage.SQLiteStore,
	client openrouter.Client,
	translator *i18n.Translator,
) (*Services, error) {
	// Validate inputs
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if client == nil {
		return nil, fmt.Errorf("OpenRouter client is required")
	}
	if translator == nil {
		return nil, fmt.Errorf("translator is required")
	}

	services := &Services{}

	// Create agent logger for debugging LLM calls
	services.AgentLogger = agentlog.NewLogger(store, logger, cfg.Server.DebugMode)

	// Create context service for shared user context across agents
	services.ContextService = agent.NewContextService(store, store, cfg, logger)
	services.ContextService.SetPeopleRepository(store)

	// Create agent executor for LLM calls
	services.AgentExecutor = agent.NewExecutor(client, services.AgentLogger, logger)

	// Create agents
	services.EnricherAgent = enricher.New(services.AgentExecutor, translator, cfg)
	services.SplitterAgent = splitter.New(services.AgentExecutor, translator, cfg, store, store)
	services.MergerAgent = merger.New(services.AgentExecutor, translator, cfg, store, store)
	services.ArchivistAgent = archivist.New(services.AgentExecutor, translator, cfg, logger, services.AgentLogger)
	services.RerankerAgent = reranker.New(client, cfg, logger, translator, store, services.AgentLogger)

	// Register agents in registry for discovery
	agentRegistry := agent.NewRegistry()
	agentRegistry.Register(services.EnricherAgent)
	agentRegistry.Register(services.SplitterAgent)
	agentRegistry.Register(services.MergerAgent)
	agentRegistry.Register(services.ArchivistAgent)
	agentRegistry.Register(services.RerankerAgent)
	logger.Info("Agent registry initialized", "agents", len(agentRegistry.List()))

	// Create memory service
	services.MemoryService = memory.NewService(logger, cfg, store, store, store, client, translator)
	services.MemoryService.SetAgentLogger(services.AgentLogger)
	services.MemoryService.SetArchivistAgent(services.ArchivistAgent)
	services.MemoryService.SetPeopleRepository(store)
	services.ArchivistAgent.SetPeopleRepository(store)

	// Create RAG service
	services.RAGService = rag.NewService(logger, cfg, store, store, store, store, store, client, services.MemoryService, translator)
	services.RAGService.SetAgentLogger(services.AgentLogger)
	services.RAGService.SetPeopleRepository(store)
	services.RAGService.SetEnricherAgent(services.EnricherAgent)
	services.RAGService.SetSplitterAgent(services.SplitterAgent)
	services.RAGService.SetMergerAgent(services.MergerAgent)
	services.RAGService.SetRerankerAgent(services.RerankerAgent)
	services.MemoryService.SetVectorSearcher(services.RAGService)
	services.MemoryService.SetTopicRepository(store)

	// Create Laplace (main chat agent)
	// Note: Laplace is not registered in the agent registry because it has a different
	// interface (requires ToolHandler for tool execution callbacks)
	services.LaplaceAgent = laplace.New(cfg, client, services.RAGService, store, store, translator, logger)
	services.LaplaceAgent.SetAgentLogger(services.AgentLogger)

	services.Translator = translator

	return services, nil
}

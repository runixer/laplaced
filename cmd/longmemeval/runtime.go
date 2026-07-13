package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/app"
	"github.com/runixer/laplaced/internal/bot"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
)

type evalRuntime struct {
	cfg      *config.Config
	store    *storage.Store
	services *app.Services
	bot      *bot.Bot
	judge    *longMemEvalJudge
}

func newEvalRuntime(ctx context.Context, cfg *config.Config, logger *slog.Logger, opts options, dbPath string) (*evalRuntime, error) {
	runtime := &evalRuntime{cfg: cfg}
	var err error
	cleanup := true
	defer func() {
		if cleanup {
			_ = runtime.Close()
		}
	}()

	cfg.Database.Path = dbPath
	cfg.Bot.Language = "en"
	cfg.Artifacts.Enabled = false

	runtime.store, err = storage.NewSQLiteStore(logger, cfg.Database.Path)
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}
	if err := runtime.store.Init(); err != nil {
		return nil, fmt.Errorf("initialize store: %w", err)
	}
	runtime.store.SetEmbeddingVersion(storage.EmbeddingVersion(cfg.Embedding.Model, cfg.Embedding.Dimensions))

	translator, err := i18n.NewTranslator("en")
	if err != nil {
		return nil, fmt.Errorf("create translator: %w", err)
	}
	defaultClient, err := llm.NewClient(logger, cfg.LLM.APIKey, cfg.LLM.ProxyURL, cfg.LLM.BaseURL, cfg.LLM.Provider.ToRouting())
	if err != nil {
		return nil, fmt.Errorf("create LLM client: %w", err)
	}
	if opts.judge {
		runtime.judge = newLongMemEvalJudge(defaultClient, opts.judgeModel)
	}
	client, err := buildEvalClient(logger, defaultClient, opts)
	if err != nil {
		return nil, err
	}
	runtime.services, err = app.SetupServices(ctx, logger, cfg, runtime.store, client, translator)
	if err != nil {
		return nil, fmt.Errorf("setup services: %w", err)
	}
	runtime.bot, err = bot.NewBot(logger, &noOpBotAPI{}, cfg, runtime.store, runtime.store, runtime.store, runtime.store, runtime.store, runtime.store, client, runtime.services.RAGService, runtime.services.ContextService, translator)
	if err != nil {
		return nil, fmt.Errorf("create bot: %w", err)
	}
	runtime.bot.SetAgentLogger(runtime.services.AgentLogger)
	runtime.bot.SetLaplaceAgent(runtime.services.LaplaceAgent)

	if err := runtime.services.RAGService.ReembedIfNeeded(ctx); err != nil {
		return nil, fmt.Errorf("migrate embeddings: %w", err)
	}
	if err := runtime.services.RAGService.ReloadVectors(); err != nil {
		return nil, fmt.Errorf("load vectors: %w", err)
	}
	cleanup = false
	return runtime, nil
}

func buildEvalClient(logger *slog.Logger, defaultClient llm.Client, opts options) (llm.Client, error) {
	routes := make(map[agent.AgentType]agentClientRoute)
	if opts.chatBaseURL != "" {
		chatClient, err := llm.NewClient(logger, "local", opts.chatProxy, opts.chatBaseURL, nil)
		if err != nil {
			return nil, fmt.Errorf("create chat-only LLM client: %w", err)
		}
		for _, role := range []agent.AgentType{agent.TypeSplitter, agent.TypeArchivist, agent.TypeMerger, agent.TypeEnricher, agent.TypeReranker, agent.TypeLaplace} {
			routes[role] = agentClientRoute{client: chatClient, enableThinking: opts.chatThinking}
		}
	}
	for role, route := range opts.roleRoutes {
		roleClient, err := llm.NewClient(logger, "local", route.Proxy, route.BaseURL, nil)
		if err != nil {
			return nil, fmt.Errorf("create %s LLM client: %w", role, err)
		}
		routes[role] = agentClientRoute{client: roleClient, enableThinking: route.ChatThinking}
	}
	if len(routes) == 0 {
		return defaultClient, nil
	}
	return &routedClient{fallback: defaultClient, embeddings: defaultClient, routes: routes}, nil
}

func overrideChatModels(cfg *config.Config, model string) {
	cfg.Agents.Default.Model = model
	cfg.Agents.Chat.Model = model
	cfg.Agents.ChatModel = model
	cfg.Agents.Archivist.Model = model
	cfg.Agents.Enricher.Model = model
	cfg.Agents.Reactor.Model = model
	cfg.Agents.Reranker.Model = model
	cfg.Agents.Splitter.Model = model
	cfg.Agents.Merger.Model = model
	cfg.Agents.Extractor.Model = model
}

type importedSession struct {
	SessionID  string  `json:"session_id"`
	MessageIDs []int64 `json:"message_ids"`
}

func (r *evalRuntime) ingestSession(ctx context.Context, scopeID storage.ScopeID, session datedSession) (*rag.ProcessingStats, importedSession, error) {
	for i, message := range session.Messages {
		createdAt := session.Date.Add(time.Duration(i) * time.Millisecond)
		if err := r.store.ImportMessage(scopeID, storage.Message{Role: message.Role, Content: message.Content, CreatedAt: createdAt}); err != nil {
			return nil, importedSession{}, fmt.Errorf("import message %d: %w", i, err)
		}
	}
	imported, err := r.store.GetUnprocessedMessages(scopeID)
	if err != nil {
		return nil, importedSession{}, fmt.Errorf("load imported session %s: %w", session.ID, err)
	}
	if len(imported) != len(session.Messages) {
		return nil, importedSession{}, fmt.Errorf("session %s imported %d of %d messages", session.ID, len(imported), len(session.Messages))
	}
	mapping := importedSession{SessionID: session.ID, MessageIDs: make([]int64, len(imported))}
	for i, message := range imported {
		if message.Role != session.Messages[i].Role || message.Content != session.Messages[i].Content {
			return nil, importedSession{}, fmt.Errorf("session %s imported message %d does not match source", session.ID, i)
		}
		mapping.MessageIDs[i] = message.ID
	}
	stats, err := r.services.RAGService.ForceProcessUserWithProgress(ctx, scopeID, func(rag.ProgressEvent) {})
	if err != nil {
		return stats, mapping, fmt.Errorf("process session %s: %w", session.ID, err)
	}
	if stats.MessagesProcessed != len(session.Messages) {
		return stats, mapping, fmt.Errorf("session %s processed %d of %d messages", session.ID, stats.MessagesProcessed, len(session.Messages))
	}
	remaining, err := r.store.GetUnprocessedMessages(scopeID)
	if err != nil {
		return stats, mapping, fmt.Errorf("check unprocessed messages: %w", err)
	}
	if len(remaining) != 0 {
		return stats, mapping, fmt.Errorf("session %s left %d unprocessed messages", session.ID, len(remaining))
	}
	return stats, mapping, nil
}

func (r *evalRuntime) Close() error {
	if r.services != nil && r.services.RAGService != nil {
		r.services.RAGService.Stop()
	}
	var closeErr error
	if r.store != nil {
		closeErr = r.store.Close()
	}
	r.store = nil
	r.services = nil
	return closeErr
}

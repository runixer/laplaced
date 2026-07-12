package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

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
	tempDir  string
}

func newEvalRuntime(ctx context.Context, cfg *config.Config, logger *slog.Logger, opts options) (*evalRuntime, error) {
	tempDir, err := os.MkdirTemp("", "laplaced-longmemeval-*")
	if err != nil {
		return nil, fmt.Errorf("create temp directory: %w", err)
	}
	runtime := &evalRuntime{cfg: cfg, tempDir: tempDir}
	cleanup := true
	defer func() {
		if cleanup {
			_ = runtime.Close()
		}
	}()

	cfg.Database.Path = filepath.Join(tempDir, "eval.db")
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
	var client llm.Client
	if opts.chatBaseURL == "" {
		client = defaultClient
	} else {
		chatClient, chatErr := llm.NewClient(logger, "local", opts.chatProxy, opts.chatBaseURL, nil)
		if chatErr != nil {
			return nil, fmt.Errorf("create chat-only LLM client: %w", chatErr)
		}
		client = &routedClient{chat: chatClient, embeddings: defaultClient, enableThinking: opts.chatThinking}
		overrideChatModels(cfg, opts.chatModel)
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

func (r *evalRuntime) ingestSession(ctx context.Context, scopeID storage.ScopeID, session datedSession) (*rag.ProcessingStats, error) {
	for i, message := range session.Messages {
		createdAt := session.Date.Add(time.Duration(i) * time.Millisecond)
		if err := r.store.ImportMessage(scopeID, storage.Message{Role: message.Role, Content: message.Content, CreatedAt: createdAt}); err != nil {
			return nil, fmt.Errorf("import message %d: %w", i, err)
		}
	}
	stats, err := r.services.RAGService.ForceProcessUserWithProgress(ctx, scopeID, func(rag.ProgressEvent) {})
	if err != nil {
		return stats, fmt.Errorf("process session %s: %w", session.ID, err)
	}
	if stats.MessagesProcessed != len(session.Messages) {
		return stats, fmt.Errorf("session %s processed %d of %d messages", session.ID, stats.MessagesProcessed, len(session.Messages))
	}
	remaining, err := r.store.GetUnprocessedMessages(scopeID)
	if err != nil {
		return stats, fmt.Errorf("check unprocessed messages: %w", err)
	}
	if len(remaining) != 0 {
		return stats, fmt.Errorf("session %s left %d unprocessed messages", session.ID, len(remaining))
	}
	return stats, nil
}

func (r *evalRuntime) Close() error {
	if r.services != nil && r.services.RAGService != nil {
		r.services.RAGService.Stop()
	}
	var closeErr error
	if r.store != nil {
		closeErr = r.store.Close()
	}
	if r.tempDir != "" {
		// tempDir is returned by os.MkdirTemp above and never accepts user input.
		if err := os.RemoveAll(r.tempDir); closeErr == nil && err != nil { // #nosec G703
			closeErr = err
		}
	}
	return closeErr
}

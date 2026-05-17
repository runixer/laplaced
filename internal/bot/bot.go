package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/bot/tools"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
)

const (
	telegramMessageLimit = 4096
)

var (
	// Telegram allowed reaction emoji (Bot API validated list)
	// See: https://gist.github.com/Soulter/3f22c8e5f9c7e152e967e8bc28c97fc9
	availableReactions = []string{"👍", "❤️", "🤣", "😱", "😢", "🤔", "🔥", "👏"}
)

type Bot struct {
	api             telegram.BotAPI
	cfg             *config.Config
	userRepo        storage.UserRepository
	msgRepo         storage.MessageRepository
	statsRepo       storage.StatsRepository
	factRepo        storage.FactRepository
	factHistoryRepo storage.FactHistoryRepository
	peopleRepo      storage.PeopleRepository   // v0.5.1: People management
	artifactRepo    storage.ArtifactRepository // v0.6.0: Link artifacts to messages
	orClient        openrouter.Client
	ragService      *rag.Service
	contextService  *agent.ContextService
	laplaceAgent    *laplace.Laplace
	downloader      telegram.FileDownloader
	fileProcessor   *files.Processor
	messageGrouper  *MessageGrouper
	toolExecutor    *tools.ToolExecutor // v0.6.1: Tool execution
	fileStorage     *files.FileStorage  // v0.8.0: For media-reply path
	logger          *slog.Logger
	translator      *i18n.Translator
	agentLogger     *agentlog.Logger
	wg              sync.WaitGroup
}

func NewBot(logger *slog.Logger, api telegram.BotAPI, cfg *config.Config, userRepo storage.UserRepository, msgRepo storage.MessageRepository, statsRepo storage.StatsRepository, factRepo storage.FactRepository, factHistoryRepo storage.FactHistoryRepository, peopleRepo storage.PeopleRepository, orClient openrouter.Client, ragService *rag.Service, contextService *agent.ContextService, translator *i18n.Translator) (*Bot, error) {
	botLogger := logger.With("component", "bot")
	downloader, err := telegram.NewHTTPFileDownloader(api, "https://api.telegram.org", cfg.Telegram.ProxyURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create file downloader: %w", err)
	}

	fileProcessor := files.NewProcessor(downloader, translator, cfg.Bot.Language, botLogger)
	fileProcessor.SetMinVoiceDurationSec(cfg.Artifacts.MinVoiceDurationSeconds)

	// Create tool executor
	toolExecutor := tools.NewToolExecutor(orClient, factRepo, factHistoryRepo, cfg, botLogger)
	toolExecutor.SetPeopleRepository(peopleRepo)
	toolExecutor.SetRAGService(ragService)

	b := &Bot{
		api:             api,
		cfg:             cfg,
		userRepo:        userRepo,
		msgRepo:         msgRepo,
		statsRepo:       statsRepo,
		factRepo:        factRepo,
		factHistoryRepo: factHistoryRepo,
		peopleRepo:      peopleRepo,
		orClient:        orClient,
		ragService:      ragService,
		contextService:  contextService,
		downloader:      downloader,
		fileProcessor:   fileProcessor,
		toolExecutor:    toolExecutor,
		logger:          botLogger,
		translator:      translator,
	}

	turnWait, err := time.ParseDuration(cfg.Bot.TurnWaitDuration)
	if err != nil {
		return nil, fmt.Errorf("invalid turn_wait_duration: %w", err)
	}

	b.messageGrouper = NewMessageGrouper(b, botLogger, turnWait, b.processMessageGroup)

	if err := b.setCommands(); err != nil {
		return nil, fmt.Errorf("failed to set bot commands: %w", err)
	}

	// Warn if no users are allowed - bot will reject all messages
	if len(cfg.Bot.AllowedUserIDs) == 0 {
		botLogger.Warn("allowed_user_ids is empty - bot will reject ALL messages. Add user IDs to allow access.")
	}

	return b, nil
}

// SetAgentLogger sets the agent logger for debug logging
func (b *Bot) SetAgentLogger(logger *agentlog.Logger) {
	b.agentLogger = logger
	b.toolExecutor.SetAgentLogger(logger)
}

// SetLaplaceAgent sets the Laplace chat agent
func (b *Bot) SetLaplaceAgent(agent *laplace.Laplace) {
	b.laplaceAgent = agent
}

// SetFileHandler sets the optional file handler for artifact saving
func (b *Bot) SetFileHandler(handler files.FileSaver) {
	b.fileProcessor.SetFileHandler(handler)
}

// SetArtifactRepo sets the artifact repository for linking artifacts to messages
func (b *Bot) SetArtifactRepo(repo storage.ArtifactRepository) {
	b.artifactRepo = repo
	if b.toolExecutor != nil {
		b.toolExecutor.SetArtifactRepository(repo)
	}
}

// SetImageGenerator wires the image-generation agent so the generate_image
// tool becomes operational. No-op if the agent is nil — the tool simply
// returns a "not configured" error to the LLM.
func (b *Bot) SetImageGenerator(gen tools.ImageGenerator) {
	if b.toolExecutor != nil {
		b.toolExecutor.SetImageGenerator(gen)
	}
}

// SetFileStorage wires the on-disk file store used by the generate_image
// tool to persist output PNGs and by the media-reply path to read them.
func (b *Bot) SetFileStorage(fs *files.FileStorage) {
	b.fileStorage = fs
	if b.toolExecutor != nil {
		b.toolExecutor.SetFileStorage(fs)
	}
}

// SetFileProcessor replaces the file processor (for testing).
func (b *Bot) SetFileProcessor(processor *files.Processor) {
	b.fileProcessor = processor
}

func (b *Bot) setCommands() error {
	// Clear commands menu
	req := telegram.SetMyCommandsRequest{
		Commands: []telegram.BotCommand{},
	}
	return b.api.SetMyCommands(context.Background(), req)
}

func (b *Bot) API() telegram.BotAPI {
	return b.api
}

// GetActiveSessions returns information about active sessions (unprocessed messages) for all users.
func (b *Bot) GetActiveSessions() ([]rag.ActiveSessionInfo, error) {
	return b.ragService.GetActiveSessions()
}

// ForceCloseSession immediately processes unprocessed messages for a user into topics.
func (b *Bot) ForceCloseSession(ctx context.Context, userID int64) (int, error) {
	return b.ragService.ForceProcessUser(ctx, userID)
}

// ForceCloseSessionWithProgress immediately processes unprocessed messages with progress reporting.
func (b *Bot) ForceCloseSessionWithProgress(ctx context.Context, userID int64, onProgress rag.ProgressCallback) (*rag.ProcessingStats, error) {
	return b.ragService.ForceProcessUserWithProgress(ctx, userID, onProgress)
}

func (b *Bot) SetWebhook(webhookURL, secretToken string) error {
	req := telegram.SetWebhookRequest{
		URL:         webhookURL,
		SecretToken: secretToken,
	}
	return b.api.SetWebhook(context.Background(), req)
}

func (b *Bot) Stop() {
	b.logger.Info("Stopping bot...")

	// First, stop the message grouper to prevent new processing
	b.messageGrouper.Stop()

	// Then wait for active handlers to finish
	b.logger.Info("Waiting for active bot handlers to finish...")
	b.wg.Wait()
	b.logger.Info("Bot stopped.")
}

func (b *Bot) HandleUpdate(ctx context.Context, rawUpdate json.RawMessage, remoteAddr string) {
	var update telegram.Update
	if err := json.Unmarshal(rawUpdate, &update); err != nil {
		b.logger.Error("failed to unmarshal update", "error", err, "remote_addr", remoteAddr)
		return
	}
	b.ProcessUpdate(ctx, &update, remoteAddr)
}

// HandleUpdateAsync starts processing a raw update in a goroutine.
// It properly handles WaitGroup to ensure graceful shutdown.
// Used by webhook handler.
func (b *Bot) HandleUpdateAsync(ctx context.Context, rawUpdate json.RawMessage, remoteAddr string) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.HandleUpdate(ctx, rawUpdate, remoteAddr)
	}()
}

// ProcessUpdateAsync starts processing an update in a goroutine.
// It properly handles WaitGroup to ensure graceful shutdown.
func (b *Bot) ProcessUpdateAsync(ctx context.Context, update *telegram.Update, source string) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.ProcessUpdate(ctx, update, source)
	}()
}

func (b *Bot) ProcessUpdate(ctx context.Context, update *telegram.Update, source string) {
	if update.Message == nil {
		return
	}

	msg := update.Message
	user := msg.From
	userID := user.ID

	// Enrich the logger with user information
	logAttrs := []any{
		"update_id", update.UpdateID,
		"user_id", userID,
		"username", user.Username,
		"first_name", user.FirstName,
		"last_name", user.LastName,
		"source", source,
	}
	ctxLogger := b.logger.With(logAttrs...)
	ctxLogger.Debug("Received message")

	// Update user info in storage
	if err := b.userRepo.UpsertUser(storage.User{
		ID:        userID,
		Username:  user.Username,
		FirstName: user.FirstName,
		LastName:  user.LastName,
		LastSeen:  time.Now(),
	}); err != nil {
		ctxLogger.Error("failed to upsert user", "error", err)
	}

	if !b.isAllowed(userID) {
		ctxLogger.Warn("Unauthorized access")
		return
	}

	// Voice messages are now grouped with text messages for better context
	if msg.Text != "" || msg.Caption != "" || msg.Photo != nil || msg.Document != nil || msg.Voice != nil || msg.Audio != nil || msg.VideoNote != nil {
		b.handleGroupedMessage(msg)
	}
}

func (b *Bot) isAllowed(userID int64) bool {
	for _, id := range b.cfg.Bot.AllowedUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}

// intPtrOrNil возвращает указатель на int, если значение != 0, иначе nil.
// Это нужно для корректной работы omitempty в JSON - Telegram API
// интерпретирует message_thread_id: 0 как попытку отправить в топик с ID=0,
// которого не существует, что вызывает ошибку "invalid topic identifier".
func intPtrOrNil(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

func (b *Bot) sendAction(ctx context.Context, chatID int64, messageThreadID int, action string) {
	actionReq := telegram.SendChatActionRequest{
		ChatID:          chatID,
		MessageThreadID: intPtrOrNil(messageThreadID),
		Action:          action,
	}
	if err := b.api.SendChatAction(ctx, actionReq); err != nil {
		b.logger.Warn("failed to send action", "action", action, "error", err)
	}
}

func (b *Bot) sendTypingActionLoop(ctx context.Context, chatID int64, messageThreadID int) {
	// Use detached context with timeout for action requests.
	// This prevents "context canceled" errors when the parent context is canceled
	// while an HTTP request is in progress. The typing indicator is automatically
	// cleared by Telegram when the bot sends a message, so we don't need to
	// explicitly cancel ongoing requests.
	const actionTimeout = 5 * time.Second

	sendTyping := func() {
		actionCtx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		b.sendAction(actionCtx, chatID, messageThreadID, "typing")
	}

	sendTyping()

	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sendTyping()
		}
	}
}

func (b *Bot) handleGroupedMessage(msg *telegram.Message) {
	b.messageGrouper.AddMessage(msg)
}

func (b *Bot) sendResponses(ctx context.Context, chatID int64, responses []telegram.SendMessageRequest, logger *slog.Logger) {
	for i, resp := range responses {
		// Safety net: skip empty or whitespace-only messages to avoid Telegram API errors
		if strings.TrimSpace(resp.Text) == "" {
			logger.Warn("skipping empty response message", "chunk_index", i)
			continue
		}

		logger.Debug("Sending response to user",
			"chunk_index", i,
			"text", resp.Text,
			"parse_mode", resp.ParseMode,
		)

		if _, err := b.api.SendMessage(ctx, resp); err != nil {
			logger.Error("failed to send message", "error", err, "chunk_index", i)

			// Use a fresh context with timeout for retry operations
			// This ensures retries complete even if the original context was cancelled
			retryCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

			if strings.Contains(err.Error(), "can't parse entities") {
				logger.Warn("retrying to send message without MarkdownV2 due to parsing error")
				resp.ParseMode = ""
				if _, sendErr := b.api.SendMessage(retryCtx, resp); sendErr != nil {
					logger.Error("failed to send raw text message", "error", sendErr)
					errorMsg := telegram.SendMessageRequest{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.generic_error")}
					if _, finalErr := b.api.SendMessage(retryCtx, errorMsg); finalErr != nil {
						logger.Error("failed to send generic error message", "error", finalErr)
					}
				}
			} else {
				errorMsg := telegram.SendMessageRequest{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.generic_error")}
				if _, sendErr := b.api.SendMessage(retryCtx, errorMsg); sendErr != nil {
					logger.Error("failed to send generic error message", "error", sendErr)
				}
			}
			cancel()
			return
		}
	}
}

func (b *Bot) getTieredCost(promptTokens, completionTokens int, logger *slog.Logger) float64 {
	tiers := b.cfg.OpenRouter.PriceTiers
	if len(tiers) == 0 {
		logger.Warn("no price tiers configured in config.yaml")
		return 0.0
	}

	// Sort tiers by UpToTokens ascending
	sort.Slice(tiers, func(i, j int) bool {
		return tiers[i].UpToTokens < tiers[j].UpToTokens
	})

	var selectedTier config.PriceTier
	found := false
	for _, tier := range tiers {
		if promptTokens <= tier.UpToTokens {
			selectedTier = tier
			found = true
			break
		}
	}

	// If promptTokens is larger than the largest tier, use the largest tier
	if !found {
		selectedTier = tiers[len(tiers)-1]
	}

	logger.Debug("selected price tier",
		"prompt_tokens", promptTokens,
		"tier_up_to", selectedTier.UpToTokens,
		"prompt_cost_per_mil", selectedTier.PromptCost,
		"completion_cost_per_mil", selectedTier.CompletionCost,
	)

	promptCost := (float64(promptTokens) / 1_000_000) * selectedTier.PromptCost
	completionCost := (float64(completionTokens) / 1_000_000) * selectedTier.CompletionCost
	totalCost := promptCost + completionCost + b.cfg.OpenRouter.RequestCost

	return totalCost
}

// SendTestMessage sends a test message through the bot pipeline without Telegram.
// It returns detailed metrics for debugging purposes.
func (b *Bot) SendTestMessage(ctx context.Context, userID int64, text string, saveToHistory bool) (*rag.TestMessageResult, error) {
	startTotal := time.Now()
	logger := b.logger.With("user_id", userID, "test_message", true)

	result := &rag.TestMessageResult{}

	if b.laplaceAgent == nil {
		return nil, fmt.Errorf("laplace agent not configured")
	}

	// Save user message to history if requested
	if saveToHistory {
		if err := b.msgRepo.AddMessageToHistory(userID, storage.Message{Role: "user", Content: text}); err != nil {
			logger.Error("failed to add message to history", "error", err)
			return nil, fmt.Errorf("failed to save message: %w", err)
		}
	}

	// Load SharedContext
	if b.contextService != nil {
		shared := b.contextService.Load(ctx, userID)
		ctx = agent.WithContext(ctx, shared)
	}

	// Build request
	currentUserMessageContent := []interface{}{
		openrouter.TextPart{Type: "text", Text: text},
	}
	req := &laplace.Request{
		UserID:              userID,
		HistoryContent:      text,
		RawQuery:            text,
		CurrentMessageParts: currentUserMessageContent,
	}

	// Create tool handler
	toolHandler := b.newBotToolHandler(userID, logger)

	// Execute via Laplace agent
	resp, err := b.laplaceAgent.Execute(ctx, req, toolHandler)
	if err != nil {
		// Fatal error (not a partial execution failure)
		return nil, fmt.Errorf("laplace execution failed: %w", err)
	}

	// Check for partial execution error (e.g., max retries reached)
	if resp.Error != nil {
		// Log partial execution for debugging
		var cost float64
		if resp.TotalCost != nil {
			cost = *resp.TotalCost
		}
		b.laplaceAgent.LogExecution(ctx, userID, resp, cost)

		return nil, fmt.Errorf("laplace execution failed: %w", resp.Error)
	}

	// Extract timing breakdown from RAG info
	if resp.RAGInfo != nil {
		result.RAGDebugInfo = resp.RAGInfo
		result.TopicsMatched = len(resp.RAGInfo.Results)

		for _, topicRes := range resp.RAGInfo.Results {
			result.FactsInjected += len(topicRes.Messages)
		}
	}

	// Estimate embedding and search time (rough)
	result.TimingEmbedding = resp.LLMDuration / 10
	result.TimingSearch = resp.LLMDuration / 10

	// Generate context preview
	if len(resp.Messages) > 0 {
		contextBytes, _ := json.Marshal(resp.Messages)
		preview := string(contextBytes)
		if len(preview) > 500 {
			preview = preview[:500] + "..."
		}
		result.ContextPreview = preview
	}

	result.TimingLLM = resp.LLMDuration
	result.Response = resp.Content
	result.PromptTokens = resp.PromptTokens
	result.CompletionTokens = resp.CompletionTokens

	if resp.TotalCost != nil {
		result.TotalCost = *resp.TotalCost
	} else {
		result.TotalCost = b.getTieredCost(resp.PromptTokens, resp.CompletionTokens, logger)
	}
	result.TimingTotal = time.Since(startTotal)

	// Save assistant response to history if requested
	if saveToHistory {
		if err := b.msgRepo.AddMessageToHistory(userID, storage.Message{Role: "assistant", Content: resp.Content}); err != nil {
			logger.Error("failed to add assistant message to history", "error", err)
		}

		// Calculate cost and log
		var cost float64
		if resp.TotalCost != nil {
			cost = *resp.TotalCost
		} else {
			cost = b.getTieredCost(resp.PromptTokens, resp.CompletionTokens, logger)
		}

		// Record stats
		stat := storage.Stat{
			UserID:     userID,
			TokensUsed: resp.PromptTokens + resp.CompletionTokens,
			CostUSD:    cost,
		}
		if err := b.statsRepo.AddStat(stat); err != nil {
			logger.Error("failed to add stat", "error", err)
		}

		// Log to agent logger
		b.laplaceAgent.LogExecution(ctx, userID, resp, cost)
	}

	return result, nil
}

// botToolHandler implements laplace.ToolHandler for Bot.
type botToolHandler struct {
	bot    *Bot
	userID int64
	logger *slog.Logger
}

// newBotToolHandler creates a new tool handler for the given user.
// Ctx and per-call context data arrive via the laplace.ToolHandler interface.
func (b *Bot) newBotToolHandler(userID int64, logger *slog.Logger) *botToolHandler {
	return &botToolHandler{
		bot:    b,
		userID: userID,
		logger: logger,
	}
}

// ExecuteToolCall implements laplace.ToolHandler.
func (h *botToolHandler) ExecuteToolCall(ctx context.Context, tcc laplace.ToolCallContext, toolName, arguments string) (*laplace.ToolResult, error) {
	cc := tools.CallContext{
		UserID:               h.userID,
		CurrentMessageImages: tcc.CurrentMessageImages,
		Iteration:            tcc.Iteration,
	}
	result, err := h.bot.toolExecutor.ExecuteToolCall(ctx, cc, toolName, arguments)
	if err != nil {
		return nil, err
	}
	return &laplace.ToolResult{
		Content:              result.Content,
		GeneratedArtifactIDs: result.GeneratedArtifactIDs,
	}, nil
}

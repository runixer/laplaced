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

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/yandex"
)

const (
	telegramMessageLimit = 4096
)

var (
	availableReactions = []string{"👍", "❤️", "😂", "😮", "😢", "🤔"}
)

type Bot struct {
	api             telegram.BotAPI
	cfg             *config.Config
	userRepo        storage.UserRepository
	msgRepo         storage.MessageRepository
	statsRepo       storage.StatsRepository
	logRepo         storage.LogRepository
	factRepo        storage.FactRepository
	factHistoryRepo storage.FactHistoryRepository
	orClient        openrouter.Client
	speechKitClient yandex.Client
	ragService      *rag.Service
	downloader      telegram.FileDownloader
	messageGrouper  *MessageGrouper
	logger          *slog.Logger
	translator      *i18n.Translator
	wg              sync.WaitGroup
}

func NewBot(logger *slog.Logger, api telegram.BotAPI, cfg *config.Config, userRepo storage.UserRepository, msgRepo storage.MessageRepository, statsRepo storage.StatsRepository, logRepo storage.LogRepository, factRepo storage.FactRepository, factHistoryRepo storage.FactHistoryRepository, orClient openrouter.Client, speechKitClient yandex.Client, ragService *rag.Service, translator *i18n.Translator) (*Bot, error) {
	botLogger := logger.With("component", "bot")
	downloader, err := telegram.NewHTTPFileDownloader(api, "https://api.telegram.org", cfg.Telegram.ProxyURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create file downloader: %w", err)
	}

	b := &Bot{
		api:             api,
		cfg:             cfg,
		userRepo:        userRepo,
		msgRepo:         msgRepo,
		statsRepo:       statsRepo,
		logRepo:         logRepo,
		factRepo:        factRepo,
		factHistoryRepo: factHistoryRepo,
		orClient:        orClient,
		speechKitClient: speechKitClient,
		ragService:      ragService,
		downloader:      downloader,
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
	ctxLogger.Info("Received message")

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
	if msg.Text != "" || msg.Caption != "" || msg.Photo != nil || msg.Document != nil || msg.Voice != nil {
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

func (b *Bot) getTools() []openrouter.Tool {
	var tools []openrouter.Tool

	// 1. Configured Tools
	for _, toolCfg := range b.cfg.Tools {
		desc := toolCfg.Description
		if desc == "" {
			desc = b.translator.Get(b.cfg.Bot.Language, fmt.Sprintf("tools.%s.description", toolCfg.Name))
		}

		paramDesc := toolCfg.ParameterDescription
		if paramDesc == "" {
			paramDesc = b.translator.Get(b.cfg.Bot.Language, fmt.Sprintf("tools.%s.parameter_description", toolCfg.Name))
		}
		if paramDesc == "" {
			paramDesc = "Input prompt for the tool"
		}
		tool := openrouter.Tool{
			Type: "function",
			Function: openrouter.ToolFunction{
				Name:        toolCfg.Name,
				Description: desc,
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": paramDesc,
						},
					},
					"required": []string{"query"},
				},
			},
		}
		tools = append(tools, tool)
	}

	return tools
}

func (b *Bot) performModelTool(ctx context.Context, modelName string, query string) (string, error) {
	req := openrouter.ChatCompletionRequest{
		Model: modelName,
		Messages: []openrouter.Message{
			{
				Role: "user",
				Content: []interface{}{
					openrouter.TextPart{Type: "text", Text: query},
				},
			},
		},
	}

	resp, err := b.orClient.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "No results found.", nil
	}

	return resp.Choices[0].Message.Content, nil
}

func (b *Bot) sendResponses(ctx context.Context, chatID int64, responses []telegram.SendMessageRequest, logger *slog.Logger) {
	for i, resp := range responses {
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

func (b *Bot) formatRAGResults(results []rag.TopicSearchResult, query string) string {
	var ragContent strings.Builder
	ragContent.WriteString(fmt.Sprintf("# %s\n", b.translator.Get(b.cfg.Bot.Language, "rag.context_header")))
	ragContent.WriteString(fmt.Sprintf("# %s: %s\n", b.translator.Get(b.cfg.Bot.Language, "rag.query"), query))
	ragContent.WriteString(fmt.Sprintf("# %s: \n", b.translator.Get(b.cfg.Bot.Language, "rag.results")))

	for _, topicRes := range results {
		ragContent.WriteString(fmt.Sprintf("\n%s: %s (%s: %.2f)\n",
			b.translator.Get(b.cfg.Bot.Language, "rag.topic_header"),
			topicRes.Topic.Summary,
			b.translator.Get(b.cfg.Bot.Language, "rag.weight"),
			topicRes.Score))
		for i, msg := range topicRes.Messages {
			var textContent string

			if msg.Role == "user" {
				// Use existing content if possible (it has built-in headers like [User...])
				textContent = msg.Content
			} else {
				// Assistant/System messages: we build the header
				dateStr := msg.CreatedAt.Format("2006-01-02 15:04:05")
				roleTitle := cases.Title(language.English).String(msg.Role)
				textContent = fmt.Sprintf("[%s (%s)]: %s", roleTitle, dateStr, msg.Content)
			}

			ragContent.WriteString(fmt.Sprintf("%d. %s\n", i+1, textContent))
		}
	}

	ragContent.WriteString(fmt.Sprintf("# %s\n", b.translator.Get(b.cfg.Bot.Language, "rag.end_of_results")))
	return ragContent.String()
}

func (b *Bot) performRAGTool(ctx context.Context, userID int64, query string) (string, error) {
	opts := &rag.RetrievalOptions{
		SkipEnrichment: true,
	}
	results, _, err := b.ragService.Retrieve(ctx, userID, query, opts)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "No results found in memory.", nil
	}

	// Sort by weight ASC (lowest to highest) to match context injection behavior
	// Retrieve returns DESC (highest to lowest)
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}

	return b.formatRAGResults(results, query), nil
}

// formatCoreIdentityFacts formats core identity facts into a string for the system prompt.
// It separates facts about the user from facts about other entities.
func (b *Bot) formatCoreIdentityFacts(facts []storage.Fact) string {
	if len(facts) == 0 {
		return ""
	}

	var userFacts, otherFacts []storage.Fact
	for _, f := range facts {
		if strings.EqualFold(f.Entity, "User") {
			userFacts = append(userFacts, f)
		} else {
			otherFacts = append(otherFacts, f)
		}
	}

	var result string
	if len(userFacts) > 0 {
		result += fmt.Sprintf("%s\n", b.translator.Get(b.cfg.Bot.Language, "memory.facts_user_header"))
		for _, f := range userFacts {
			result += fmt.Sprintf("- [ID:%d] [%s] [%s/%s] (Updated: %s) %s\n",
				f.ID, f.Entity, f.Category, f.Type, f.LastUpdated.Format("2006-01-02"), f.Content)
		}
		result += "\n"
	}

	if len(otherFacts) > 0 {
		result += fmt.Sprintf("%s\n", b.translator.Get(b.cfg.Bot.Language, "memory.facts_others_header"))
		for _, f := range otherFacts {
			result += fmt.Sprintf("- [ID:%d] [%s] [%s/%s] (Updated: %s) %s\n",
				f.ID, f.Entity, f.Category, f.Type, f.LastUpdated.Format("2006-01-02"), f.Content)
		}
		result += "\n"
	}

	return strings.TrimRight(result, "\n")
}

// deduplicateTopics removes messages from retrieved topics that are already present in recent history.
func (b *Bot) deduplicateTopics(topics []rag.TopicSearchResult, recentHistory []storage.Message) []rag.TopicSearchResult {
	recentIDs := make(map[int64]bool, len(recentHistory))
	for _, msg := range recentHistory {
		recentIDs[msg.ID] = true
	}

	var filtered []rag.TopicSearchResult
	for _, topicRes := range topics {
		var filteredMsgs []storage.Message
		for _, msg := range topicRes.Messages {
			if !recentIDs[msg.ID] {
				filteredMsgs = append(filteredMsgs, msg)
			}
		}
		if len(filteredMsgs) > 0 {
			topicRes.Messages = filteredMsgs
			filtered = append(filtered, topicRes)
		}
	}
	return filtered
}

// buildContext assembles the full context for LLM generation.
//
// REFACTORING NOTE: This function has high cyclomatic complexity (~34) but is intentionally
// kept as a single function because:
// 1. It's a linear pipeline (steps 0-6) where each step depends on previous results
// 2. Three helpers are already extracted: formatCoreIdentityFacts, deduplicateTopics, filterShortMessages
// 3. The remaining complexity comes from error handling (normal Go pattern)
// 4. Splitting further would fragment understanding without improving testability
// See CLAUDE.md "Refactoring & Cyclomatic Complexity" section for decision framework.
func (b *Bot) buildContext(ctx context.Context, userID int64, currentMessageContent string, currentMessageRaw string, currentUserMessageParts []interface{}) ([]openrouter.Message, *rag.RetrievalDebugInfo, error) {
	// 0. Get Session Memory (Short-Term Archive)
	unprocessedHistory, err := b.msgRepo.GetUnprocessedMessages(userID)
	if err != nil {
		b.logger.Error("failed to get unprocessed messages", "error", err)
	}

	// 1. Get Core Identity Facts (Layer 1)
	allFacts, err := b.factRepo.GetFacts(userID)
	var coreIdentityFacts []storage.Fact
	if err == nil {
		for _, f := range allFacts {
			if f.Type == "identity" || f.Importance >= 90 {
				coreIdentityFacts = append(coreIdentityFacts, f)
			}
		}
	} else {
		b.logger.Error("failed to get facts", "error", err)
	}

	// Format Core Identity for System Prompt
	memoryBankFormatted := b.formatCoreIdentityFacts(coreIdentityFacts)

	recentHistory := unprocessedHistory

	// Limit recent history to avoid context overflow
	if b.cfg.RAG.MaxContextMessages > 0 && len(recentHistory) > b.cfg.RAG.MaxContextMessages {
		recentHistory = recentHistory[len(recentHistory)-b.cfg.RAG.MaxContextMessages:]
	}

	// 2. RAG Retrieval (Layer 2)
	var retrievedResults []rag.TopicSearchResult
	var retrievedFacts []storage.Fact
	var debugInfo *rag.RetrievalDebugInfo

	if b.cfg.RAG.Enabled {
		// Prepare context for enrichment
		var enrichmentContext []storage.Message
		if len(recentHistory) > 1 {
			availableHistory := recentHistory[:len(recentHistory)-1]
			count := 5
			if len(availableHistory) < count {
				count = len(availableHistory)
			}
			enrichmentContext = availableHistory[len(availableHistory)-count:]
		}

		var err error
		opts := &rag.RetrievalOptions{
			History:        enrichmentContext,
			SkipEnrichment: false,
		}
		retrievedResults, debugInfo, err = b.ragService.Retrieve(ctx, userID, currentMessageRaw, opts)
		if err != nil {
			b.logger.Error("RAG retrieval failed", "error", err)
		} else {
			retrievedResults = b.deduplicateTopics(retrievedResults, recentHistory)
		}

		// Retrieve relevant facts (Layer 2 - Context)
		queryForFacts := currentMessageRaw
		if debugInfo != nil && debugInfo.EnrichedQuery != "" {
			queryForFacts = debugInfo.EnrichedQuery
		}

		retrievedFacts, err = b.ragService.RetrieveFacts(ctx, userID, queryForFacts)
		if err != nil {
			b.logger.Error("Fact retrieval failed", "error", err)
		}
	}

	var orMessages []openrouter.Message

	// System Prompt + Core Identity
	fullSystemPrompt := b.cfg.Bot.SystemPrompt
	if fullSystemPrompt == "" {
		botName := b.cfg.Bot.BotName
		if botName == "" {
			botName = "Bot"
		}
		fullSystemPrompt = b.translator.Get(b.cfg.Bot.Language, "bot.system_prompt", botName)

		if b.cfg.Bot.SystemPromptExtra != "" {
			fullSystemPrompt += " " + b.cfg.Bot.SystemPromptExtra
		}
	}
	if memoryBankFormatted != "" {
		fullSystemPrompt += "\n\n" + memoryBankFormatted
	}

	if fullSystemPrompt != "" {
		orMessages = append(orMessages, openrouter.Message{
			Role: "system",
			Content: []interface{}{
				openrouter.TextPart{Type: "text", Text: fullSystemPrompt},
			},
		})
	}

	// RAG Result String (Topics + Facts)
	var ragContextMsg *openrouter.Message
	if len(retrievedResults) > 0 || len(retrievedFacts) > 0 {
		ragContent := b.formatRAGResults(retrievedResults, "")
		if debugInfo != nil {
			ragContent = b.formatRAGResults(retrievedResults, debugInfo.EnrichedQuery)
		}

		if len(retrievedFacts) > 0 {
			ragContent += fmt.Sprintf("\n%s\n", b.translator.Get(b.cfg.Bot.Language, "rag.relevant_facts_header"))
			for _, f := range retrievedFacts {
				// Skip if already in Core Identity
				isCore := false
				for _, core := range coreIdentityFacts {
					if core.Content == f.Content {
						isCore = true
						break
					}
				}
				if !isCore {
					ragContent += fmt.Sprintf("- [ID:%d] [%s] [%s/%s] (Updated: %s) %s\n", f.ID, f.Entity, f.Category, f.Type, f.LastUpdated.Format("2006-01-02"), f.Content)
				}
			}
		}

		ragContextMsg = &openrouter.Message{
			Role: "user",
			Content: []interface{}{
				openrouter.TextPart{Type: "text", Text: ragContent},
			},
		}
	}

	// Add Recent History
	var sessionChars int
	for i, hMsg := range recentHistory {
		// If this is the last message (current request) and we have RAG context, inject it before
		if i == len(recentHistory)-1 && ragContextMsg != nil {
			orMessages = append(orMessages, *ragContextMsg)
		}
		var contentParts []interface{}

		if hMsg.Content == currentMessageContent && currentUserMessageParts != nil && hMsg.Role == "user" {
			contentParts = currentUserMessageParts
		} else {
			contentParts = []interface{}{
				openrouter.TextPart{Type: "text", Text: hMsg.Content},
			}
		}
		sessionChars += len(hMsg.Content)
		orMessages = append(orMessages, openrouter.Message{Role: hMsg.Role, Content: contentParts})
	}

	// Record context tokens by source (approximate: 1 token ≈ 4 characters)
	if len(memoryBankFormatted) > 0 {
		RecordContextTokensBySource(userID, ContextSourceProfile, len(memoryBankFormatted)/4)
	}
	if ragContextMsg != nil {
		ragText := ""
		if parts, ok := ragContextMsg.Content.([]interface{}); ok {
			for _, part := range parts {
				if tp, ok := part.(openrouter.TextPart); ok {
					ragText += tp.Text
				}
			}
		}
		if len(ragText) > 0 {
			RecordContextTokensBySource(userID, ContextSourceTopics, len(ragText)/4)
		}
	}
	if sessionChars > 0 {
		RecordContextTokensBySource(userID, ContextSourceSession, sessionChars/4)
	}

	return orMessages, debugInfo, nil
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

// memoryOpParams holds parsed parameters for a memory operation.
type memoryOpParams struct {
	Action     string
	Entity     string
	Content    string
	Category   string
	FactType   string
	Reason     string
	Importance int
	FactID     int64
}

// parseMemoryOpParams extracts operation parameters from a map.
func parseMemoryOpParams(params map[string]interface{}) memoryOpParams {
	p := memoryOpParams{
		Action:   params["action"].(string),
		Entity:   "",
		Content:  "",
		Category: "",
		FactType: "",
		Reason:   "",
	}
	if v, ok := params["entity"].(string); ok {
		p.Entity = v
	}
	if v, ok := params["content"].(string); ok {
		p.Content = v
	}
	if v, ok := params["category"].(string); ok {
		p.Category = v
	}
	if v, ok := params["type"].(string); ok {
		p.FactType = v
	}
	if v, ok := params["reason"].(string); ok {
		p.Reason = v
	}
	if v, ok := params["importance"].(float64); ok {
		p.Importance = int(v)
	}
	if v, ok := params["fact_id"].(float64); ok {
		p.FactID = int64(v)
	}
	return p
}

func (b *Bot) performAddFact(ctx context.Context, userID int64, p memoryOpParams) (string, error) {
	if p.Content == "" {
		return "Error: Content is required for adding a fact.", fmt.Errorf("missing content")
	}

	resp, err := b.orClient.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
		Model: b.cfg.RAG.EmbeddingModel,
		Input: []string{p.Content},
	})
	if err != nil || len(resp.Data) == 0 {
		return "Error generating embedding.", err
	}

	// Apply defaults
	entity := p.Entity
	if entity == "" {
		entity = "User"
	}
	category := p.Category
	if category == "" {
		category = "other"
	}
	factType := p.FactType
	if factType == "" {
		factType = "context"
	}
	importance := p.Importance
	if importance == 0 {
		importance = 50
	}

	fact := storage.Fact{
		UserID:     userID,
		Entity:     entity,
		Content:    p.Content,
		Type:       factType,
		Importance: importance,
		Embedding:  resp.Data[0].Embedding,
		Relation:   "related_to",
		Category:   category,
		TopicID:    nil,
	}

	id, err := b.factRepo.AddFact(fact)
	if err != nil {
		return "Error adding fact.", err
	}

	if err := b.factHistoryRepo.AddFactHistory(storage.FactHistory{
		FactID:     id,
		UserID:     userID,
		Action:     "add",
		NewContent: p.Content,
		Reason:     p.Reason,
		Category:   category,
		Entity:     entity,
		Relation:   "related_to",
		Importance: importance,
		TopicID:    nil,
	}); err != nil {
		b.logger.Warn("failed to add fact history", "fact_id", id, "error", err)
	}

	return "Fact added successfully.", nil
}

func (b *Bot) performDeleteFact(ctx context.Context, userID int64, p memoryOpParams) (string, error) {
	if p.FactID == 0 {
		return "Error: Fact ID is required for deletion.", fmt.Errorf("missing fact_id")
	}

	// Fetch old fact for history
	oldFacts, err := b.factRepo.GetFactsByIDs([]int64{p.FactID})
	if err != nil {
		b.logger.Warn("failed to fetch old fact for history", "fact_id", p.FactID, "error", err)
	}
	var oldContent, category, entity, relation string
	var importance int
	if len(oldFacts) > 0 {
		oldContent = oldFacts[0].Content
		category = oldFacts[0].Category
		entity = oldFacts[0].Entity
		relation = oldFacts[0].Relation
		importance = oldFacts[0].Importance
	}

	if err := b.factRepo.DeleteFact(userID, p.FactID); err != nil {
		return "Error deleting fact.", err
	}

	if err := b.factHistoryRepo.AddFactHistory(storage.FactHistory{
		FactID:     p.FactID,
		UserID:     userID,
		Action:     "delete",
		OldContent: oldContent,
		Reason:     p.Reason,
		Category:   category,
		Entity:     entity,
		Relation:   relation,
		Importance: importance,
		TopicID:    nil,
	}); err != nil {
		b.logger.Warn("failed to add fact history", "fact_id", p.FactID, "error", err)
	}

	return "Fact deleted successfully.", nil
}

func (b *Bot) performUpdateFact(ctx context.Context, userID int64, p memoryOpParams) (string, error) {
	if p.FactID == 0 {
		return "Error: Fact ID is required for update.", fmt.Errorf("missing fact_id")
	}

	resp, err := b.orClient.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
		Model: b.cfg.RAG.EmbeddingModel,
		Input: []string{p.Content},
	})
	if err != nil || len(resp.Data) == 0 {
		return "Error generating embedding.", err
	}

	// Apply defaults
	factType := p.FactType
	if factType == "" {
		factType = "context"
	}
	importance := p.Importance
	if importance == 0 {
		importance = 50
	}

	// Fetch old fact for history
	oldFacts, err := b.factRepo.GetFactsByIDs([]int64{p.FactID})
	if err != nil {
		b.logger.Warn("failed to fetch old fact for history", "fact_id", p.FactID, "error", err)
	}
	var oldContent, category, entity, relation string
	if len(oldFacts) > 0 {
		oldContent = oldFacts[0].Content
		category = oldFacts[0].Category
		entity = oldFacts[0].Entity
		relation = oldFacts[0].Relation
	}

	fact := storage.Fact{
		ID:         p.FactID,
		UserID:     userID,
		Content:    p.Content,
		Type:       factType,
		Importance: importance,
		Embedding:  resp.Data[0].Embedding,
	}

	if err := b.factRepo.UpdateFact(fact); err != nil {
		return "Error updating fact.", err
	}

	if err := b.factHistoryRepo.AddFactHistory(storage.FactHistory{
		FactID:     p.FactID,
		UserID:     userID,
		Action:     "update",
		OldContent: oldContent,
		NewContent: p.Content,
		Reason:     p.Reason,
		Category:   category,
		Entity:     entity,
		Relation:   relation,
		Importance: importance,
		TopicID:    nil,
	}); err != nil {
		b.logger.Warn("failed to add fact history", "fact_id", p.FactID, "error", err)
	}

	return "Fact updated successfully.", nil
}

func (b *Bot) performManageMemory(ctx context.Context, userID int64, args map[string]interface{}) (string, error) {
	// The tool receives a JSON string inside the `query` argument
	query, ok := args["query"].(string)
	if !ok {
		return "Error: query argument is missing", nil
	}

	var root map[string]interface{}
	if err := json.Unmarshal([]byte(query), &root); err != nil {
		return fmt.Sprintf("Error parsing query JSON: %v", err), nil
	}

	var operations []map[string]interface{}

	// Check if it's a batch operation
	if ops, ok := root["operations"].([]interface{}); ok {
		for _, op := range ops {
			if opMap, ok := op.(map[string]interface{}); ok {
				operations = append(operations, opMap)
			}
		}
	} else {
		// Single operation (legacy or simple call)
		operations = append(operations, root)
	}

	var results []string
	var errorCount int

	for i, params := range operations {
		p := parseMemoryOpParams(params)

		var result string
		var err error

		switch p.Action {
		case "add":
			result, err = b.performAddFact(ctx, userID, p)
		case "delete":
			result, err = b.performDeleteFact(ctx, userID, p)
		case "update":
			result, err = b.performUpdateFact(ctx, userID, p)
		default:
			result = fmt.Sprintf("Unknown action: %s", p.Action)
			err = fmt.Errorf("unknown action")
		}

		if err != nil {
			errorCount++
			results = append(results, fmt.Sprintf("Op %d (%s): Failed - %s (%v)", i+1, p.Action, result, err))
		} else {
			results = append(results, fmt.Sprintf("Op %d (%s): Success", i+1, p.Action))
		}
	}

	finalResult := strings.Join(results, "\n")
	if errorCount > 0 {
		return fmt.Sprintf("Completed with %d errors:\n%s", errorCount, finalResult), nil
	}
	return fmt.Sprintf("Successfully processed %d operations:\n%s", len(operations), finalResult), nil
}

// SendTestMessage sends a test message through the bot pipeline without Telegram.
// It returns detailed metrics for debugging purposes.
func (b *Bot) SendTestMessage(ctx context.Context, userID int64, text string, saveToHistory bool) (*rag.TestMessageResult, error) {
	startTotal := time.Now()
	logger := b.logger.With("user_id", userID, "test_message", true)

	result := &rag.TestMessageResult{}

	// Save user message to history if requested
	if saveToHistory {
		if err := b.msgRepo.AddMessageToHistory(userID, storage.Message{Role: "user", Content: text}); err != nil {
			logger.Error("failed to add message to history", "error", err)
			return nil, fmt.Errorf("failed to save message: %w", err)
		}
	}

	// Build context with timing
	startContext := time.Now()
	currentUserMessageContent := []interface{}{
		openrouter.TextPart{Type: "text", Text: text},
	}
	orMessages, ragInfo, err := b.buildContext(ctx, userID, text, text, currentUserMessageContent)
	if err != nil {
		return nil, fmt.Errorf("failed to build context: %w", err)
	}
	contextDuration := time.Since(startContext)

	// Extract timing breakdown from RAG info
	if ragInfo != nil {
		result.RAGDebugInfo = ragInfo
		result.TopicsMatched = len(ragInfo.Results)

		// Count facts from results
		for _, topicRes := range ragInfo.Results {
			result.FactsInjected += len(topicRes.Messages)
		}
	}

	// Estimate embedding and search time from context building
	// (in reality these happen inside buildContext via ragService.Retrieve)
	result.TimingEmbedding = contextDuration / 3 // rough estimate
	result.TimingSearch = contextDuration / 3    // rough estimate

	// Generate context preview (first 500 chars)
	if len(orMessages) > 0 {
		contextBytes, _ := json.Marshal(orMessages)
		preview := string(contextBytes)
		if len(preview) > 500 {
			preview = preview[:500] + "..."
		}
		result.ContextPreview = preview
	}

	// Call LLM with timing
	startLLM := time.Now()

	var plugins []openrouter.Plugin
	if b.cfg.OpenRouter.PDFParserEngine != "" {
		plugins = append(plugins, openrouter.Plugin{
			ID: "file-parser",
			PDF: openrouter.PDFConfig{
				Engine: b.cfg.OpenRouter.PDFParserEngine,
			},
		})
	}

	tools := b.getTools()
	totalPromptTokens := 0
	totalCompletionTokens := 0
	var finalResponse string
	toolIterations := 0
	const maxToolIterations = 10

	for {
		if toolIterations >= maxToolIterations {
			logger.Warn("max tool iterations reached", "iterations", toolIterations)
			break
		}
		toolIterations++

		req := openrouter.ChatCompletionRequest{
			Model:    b.cfg.OpenRouter.Model,
			Messages: orMessages,
			Plugins:  plugins,
			Tools:    tools,
		}

		resp, err := b.orClient.CreateChatCompletion(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("failed to get completion: %w", err)
		}

		if len(resp.Choices) == 0 {
			return nil, fmt.Errorf("empty response from OpenRouter")
		}

		totalPromptTokens += resp.Usage.PromptTokens
		totalCompletionTokens += resp.Usage.CompletionTokens

		choice := resp.Choices[0].Message

		// Handle tool calls
		if len(choice.ToolCalls) > 0 {
			logger.Info("Model requested tool calls", "count", len(choice.ToolCalls))

			var content interface{} = choice.Content
			if choice.Content == "" {
				content = nil
			}

			orMessages = append(orMessages, openrouter.Message{
				Role:             "assistant",
				Content:          content,
				ToolCalls:        choice.ToolCalls,
				ReasoningDetails: choice.ReasoningDetails,
			})

			// Execute tool calls (without Telegram actions)
			toolMessages := b.executeToolCallsForTest(ctx, userID, choice.ToolCalls, logger)
			orMessages = append(orMessages, toolMessages...)
			continue
		}

		finalResponse = choice.Content
		break
	}

	result.TimingLLM = time.Since(startLLM)

	if strings.TrimSpace(finalResponse) == "" {
		finalResponse = b.translator.Get(b.cfg.Bot.Language, "bot.empty_response")
	}

	result.Response = finalResponse
	result.PromptTokens = totalPromptTokens
	result.CompletionTokens = totalCompletionTokens
	result.TotalCost = b.getTieredCost(totalPromptTokens, totalCompletionTokens, logger)
	result.TimingTotal = time.Since(startTotal)

	// Save assistant response to history if requested
	if saveToHistory {
		if err := b.msgRepo.AddMessageToHistory(userID, storage.Message{Role: "assistant", Content: finalResponse}); err != nil {
			logger.Error("failed to add assistant message to history", "error", err)
		}

		// Record metrics
		b.recordMetrics(userID, totalPromptTokens, totalCompletionTokens, ragInfo, orMessages, finalResponse, logger)
	}

	return result, nil
}

// executeToolCallsForTest executes tool calls without Telegram-specific actions.
func (b *Bot) executeToolCallsForTest(ctx context.Context, userID int64, toolCalls []openrouter.ToolCall, logger *slog.Logger) []openrouter.Message {
	var toolMessages []openrouter.Message

	for _, toolCall := range toolCalls {
		var matchedTool *config.ToolConfig
		for _, t := range b.cfg.Tools {
			if t.Name == toolCall.Function.Name {
				matchedTool = &t
				break
			}
		}

		if matchedTool != nil {
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
				logger.Error("failed to parse tool arguments", "error", err, "tool", matchedTool.Name)
				toolMessages = append(toolMessages, openrouter.Message{
					Role:       "tool",
					Content:    fmt.Sprintf("Error parsing arguments: %v", err),
					ToolCallID: toolCall.ID,
				})
				continue
			}

			var toolResult string
			var err error

			query, ok := args["query"].(string)
			if !ok {
				err = fmt.Errorf("query argument missing or not a string")
			} else {
				switch matchedTool.Name {
				case "memory_search":
					logger.Info("Executing RAG tool", "tool", matchedTool.Name, "query", query)
					toolResult, err = b.performRAGTool(ctx, userID, query)
				case "manage_memory":
					logger.Info("Executing Manage Memory tool", "tool", matchedTool.Name)
					toolResult, err = b.performManageMemory(ctx, userID, args)
				default:
					logger.Info("Executing model tool", "tool", matchedTool.Name, "model", matchedTool.Model, "query", query)
					toolResult, err = b.performModelTool(ctx, matchedTool.Model, query)
				}
			}

			if err != nil {
				logger.Error("tool execution failed", "error", err, "tool", matchedTool.Name)
				toolMessages = append(toolMessages, openrouter.Message{
					Role:       "tool",
					Content:    fmt.Sprintf("Tool execution failed: %v", err),
					ToolCallID: toolCall.ID,
				})
			} else {
				toolMessages = append(toolMessages, openrouter.Message{
					Role:       "tool",
					Content:    toolResult,
					ToolCallID: toolCall.ID,
				})
			}
		} else {
			logger.Warn("Unknown tool called", "tool_name", toolCall.Function.Name)
			toolMessages = append(toolMessages, openrouter.Message{
				Role:       "tool",
				Content:    fmt.Sprintf("Error: Unknown tool '%s'", toolCall.Function.Name),
				ToolCallID: toolCall.ID,
			})
		}
	}
	return toolMessages
}

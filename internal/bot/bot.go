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

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/agentlog"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
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
	orClient        openrouter.Client
	speechKitClient yandex.Client
	ragService      *rag.Service
	contextService  *agent.ContextService
	laplaceAgent    *laplace.Laplace
	downloader      telegram.FileDownloader
	fileProcessor   *files.Processor
	messageGrouper  *MessageGrouper
	logger          *slog.Logger
	translator      *i18n.Translator
	agentLogger     *agentlog.Logger
	wg              sync.WaitGroup
}

func NewBot(logger *slog.Logger, api telegram.BotAPI, cfg *config.Config, userRepo storage.UserRepository, msgRepo storage.MessageRepository, statsRepo storage.StatsRepository, factRepo storage.FactRepository, factHistoryRepo storage.FactHistoryRepository, orClient openrouter.Client, speechKitClient yandex.Client, ragService *rag.Service, contextService *agent.ContextService, translator *i18n.Translator) (*Bot, error) {
	botLogger := logger.With("component", "bot")
	downloader, err := telegram.NewHTTPFileDownloader(api, "https://api.telegram.org", cfg.Telegram.ProxyURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create file downloader: %w", err)
	}

	fileProcessor := files.NewProcessor(downloader, translator, cfg.Bot.Language, botLogger)

	b := &Bot{
		api:             api,
		cfg:             cfg,
		userRepo:        userRepo,
		msgRepo:         msgRepo,
		statsRepo:       statsRepo,
		factRepo:        factRepo,
		factHistoryRepo: factHistoryRepo,
		orClient:        orClient,
		speechKitClient: speechKitClient,
		ragService:      ragService,
		contextService:  contextService,
		downloader:      downloader,
		fileProcessor:   fileProcessor,
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
}

// SetLaplaceAgent sets the Laplace chat agent
func (b *Bot) SetLaplaceAgent(agent *laplace.Laplace) {
	b.laplaceAgent = agent
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

func (b *Bot) performModelTool(ctx context.Context, userID int64, modelName string, query string) (string, error) {
	startTime := time.Now()

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
		UserID: userID,
	}

	resp, err := b.orClient.CreateChatCompletion(ctx, req)
	duration := time.Since(startTime)

	if err != nil {
		// Log error to Scout agent
		if b.agentLogger != nil {
			b.agentLogger.Log(ctx, agentlog.Entry{
				UserID:       userID,
				AgentType:    agentlog.AgentScout,
				InputPrompt:  query,
				Model:        modelName,
				DurationMs:   int(duration.Milliseconds()),
				Success:      false,
				ErrorMessage: err.Error(),
			})
		}
		return "", err
	}

	result := "No results found."
	if len(resp.Choices) > 0 {
		result = resp.Choices[0].Message.Content
	}

	// Log success to Scout agent
	if b.agentLogger != nil {
		b.agentLogger.Log(ctx, agentlog.Entry{
			UserID:           userID,
			AgentType:        agentlog.AgentScout,
			InputPrompt:      query,
			InputContext:     resp.DebugRequestBody, // Full API request JSON
			OutputResponse:   result,
			OutputContext:    resp.DebugResponseBody, // Full API response JSON
			Model:            modelName,
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalCost:        resp.Usage.Cost,
			DurationMs:       int(duration.Milliseconds()),
			Success:          true,
		})
	}

	return result, nil
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

// escapeXMLAttr escapes special characters for use in XML attribute values
func escapeXMLAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func (b *Bot) formatRAGResults(results []rag.TopicSearchResult, query string) string {
	if len(results) == 0 {
		return ""
	}

	var ragContent strings.Builder
	ragContent.WriteString("<retrieved_context query=\"")
	ragContent.WriteString(escapeXMLAttr(query))
	ragContent.WriteString("\">\n")

	for _, topicRes := range results {
		// Get topic date from first message or topic creation
		topicDate := topicRes.Topic.CreatedAt.Format("2006-01-02")
		if len(topicRes.Messages) > 0 {
			topicDate = topicRes.Messages[0].CreatedAt.Format("2006-01-02")
		}

		ragContent.WriteString("  <topic id=\"")
		ragContent.WriteString(fmt.Sprintf("%d", topicRes.Topic.ID))
		ragContent.WriteString("\" summary=\"")
		ragContent.WriteString(escapeXMLAttr(topicRes.Topic.Summary))
		ragContent.WriteString("\" relevance=\"")
		ragContent.WriteString(fmt.Sprintf("%.2f", topicRes.Score))
		ragContent.WriteString("\" date=\"")
		ragContent.WriteString(topicDate)
		ragContent.WriteString("\">\n")

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

		ragContent.WriteString("  </topic>\n")
	}

	ragContent.WriteString("</retrieved_context>")
	return ragContent.String()
}

func (b *Bot) performHistorySearch(ctx context.Context, userID int64, query string) (string, error) {
	opts := &rag.RetrievalOptions{
		SkipEnrichment: true,
		Source:         "tool",
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
		Model: b.cfg.Embedding.Model,
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
		Model: b.cfg.Embedding.Model,
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
	toolHandler := b.newBotToolHandler(ctx, userID, logger)

	// Execute via Laplace agent
	resp, err := b.laplaceAgent.Execute(ctx, req, toolHandler)
	if err != nil {
		return nil, fmt.Errorf("laplace execution failed: %w", err)
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
	ctx    context.Context
	userID int64
	logger *slog.Logger
}

// newBotToolHandler creates a new tool handler for the given context.
func (b *Bot) newBotToolHandler(ctx context.Context, userID int64, logger *slog.Logger) *botToolHandler {
	return &botToolHandler{
		bot:    b,
		ctx:    ctx,
		userID: userID,
		logger: logger,
	}
}

// ExecuteToolCall executes a tool call by name and returns the result.
func (h *botToolHandler) ExecuteToolCall(toolName string, arguments string) (string, error) {
	// Find tool config
	var matchedTool *config.ToolConfig
	for _, t := range h.bot.cfg.Tools {
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

	// Get query parameter
	query, ok := args["query"].(string)
	if !ok {
		return "", fmt.Errorf("query argument missing or not a string")
	}

	// Execute based on tool name
	switch matchedTool.Name {
	case "search_history":
		h.logger.Info("Executing history search tool", "tool", matchedTool.Name, "query", query)
		return h.bot.performHistorySearch(h.ctx, h.userID, query)
	case "manage_memory":
		h.logger.Info("Executing Manage Memory tool", "tool", matchedTool.Name)
		return h.bot.performManageMemory(h.ctx, h.userID, args)
	default:
		h.logger.Info("Executing model tool", "tool", matchedTool.Name, "model", matchedTool.Model, "query", query)
		return h.bot.performModelTool(h.ctx, h.userID, matchedTool.Model, query)
	}
}

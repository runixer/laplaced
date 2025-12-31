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
	downloader := telegram.NewHTTPFileDownloader(api, "https://api.telegram.org")

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

func (b *Bot) SetWebhook(webhookURL string) error {
	req := telegram.SetWebhookRequest{
		URL: webhookURL,
	}
	return b.api.SetWebhook(context.Background(), req)
}

func (b *Bot) Stop() {
	b.logger.Info("Waiting for active bot handlers to finish...")
	b.wg.Wait()
	b.logger.Info("Bot handlers finished.")
}

func (b *Bot) HandleUpdate(ctx context.Context, rawUpdate json.RawMessage, remoteAddr string) {
	var update telegram.Update
	if err := json.Unmarshal(rawUpdate, &update); err != nil {
		b.logger.Error("failed to unmarshal update", "error", err, "remote_addr", remoteAddr)
		return
	}
	b.ProcessUpdate(ctx, &update, remoteAddr)
}

func (b *Bot) ProcessUpdate(ctx context.Context, update *telegram.Update, source string) {
	b.wg.Add(1)
	defer b.wg.Done()

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

	if msg.Voice != nil {
		b.handleVoiceMessage(ctx, msg, ctxLogger)
		return
	}

	if msg.Text != "" || msg.Caption != "" || msg.Photo != nil || msg.Document != nil {
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
	b.sendAction(ctx, chatID, messageThreadID, "typing")

	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.sendAction(ctx, chatID, messageThreadID, "typing")
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

			if strings.Contains(err.Error(), "can't parse entities") {
				logger.Warn("retrying to send message without MarkdownV2 due to parsing error")
				resp.ParseMode = ""
				if _, sendErr := b.api.SendMessage(context.Background(), resp); sendErr != nil {
					logger.Error("failed to send raw text message", "error", sendErr)
					errorMsg := telegram.SendMessageRequest{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.generic_error")}
					if _, finalErr := b.api.SendMessage(context.Background(), errorMsg); finalErr != nil {
						logger.Error("failed to send generic error message", "error", finalErr)
					}
				}
			} else {
				errorMsg := telegram.SendMessageRequest{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.generic_error")}
				if _, sendErr := b.api.SendMessage(context.Background(), errorMsg); sendErr != nil {
					logger.Error("failed to send generic error message", "error", sendErr)
				}
			}
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
				roleTitle := strings.Title(msg.Role)
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

func (b *Bot) handleVoiceMessage(ctx context.Context, msg *telegram.Message, logger *slog.Logger) {
	chatID := msg.Chat.ID
	userID := msg.From.ID

	logger.Info("processing voice message", "message_id", msg.MessageID)

	if b.speechKitClient == nil {
		logger.Info("speechkit client is disabled, ignoring voice message")
		b.sendResponses(ctx, chatID, []telegram.SendMessageRequest{{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.voice_recognition_disabled")}}, logger)
		return
	}

	typingCtx, cancelTyping := context.WithCancel(ctx)
	defer cancelTyping()
	go b.sendTypingActionLoop(typingCtx, chatID, msg.MessageThreadID)

	audioData, err := b.downloader.DownloadFile(ctx, msg.Voice.FileID)
	if err != nil {
		logger.Error("failed to download voice message file", "error", err, "file_id", msg.Voice.FileID)
		b.sendResponses(ctx, chatID, []telegram.SendMessageRequest{{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.api_error")}}, logger)
		return
	}

	speechCtx := context.Background()
	recognizedText, err := b.speechKitClient.Recognize(speechCtx, audioData)
	if err != nil {
		logger.Error("failed to recognize speech", "error", err)
		b.sendResponses(ctx, chatID, []telegram.SendMessageRequest{{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.api_error")}}, logger)
		return
	}

	if recognizedText == "" {
		logger.Warn("recognized text is empty, not processing further")
		return
	}

	fakeTextMessage := &telegram.Message{
		MessageID: msg.MessageID,
		From:      msg.From,
		Chat:      msg.Chat,
		Date:      msg.Date,
		Text:      fmt.Sprintf("%s %s", b.translator.Get(b.cfg.Bot.Language, "bot.voice_recognition_prefix"), recognizedText),
	}

	// For voice messages, we create a temporary group with just this message
	group := &MessageGroup{
		Messages: []*telegram.Message{fakeTextMessage},
		UserID:   userID,
	}
	b.processMessageGroup(context.Background(), group)
}

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
	var memoryBankFormatted string
	if len(coreIdentityFacts) > 0 {
		var userFacts []storage.Fact
		var otherFacts []storage.Fact

		for _, f := range coreIdentityFacts {
			if strings.EqualFold(f.Entity, "User") {
				userFacts = append(userFacts, f)
			} else {
				otherFacts = append(otherFacts, f)
			}
		}

		if len(userFacts) > 0 {
			memoryBankFormatted += fmt.Sprintf("%s\n", b.translator.Get(b.cfg.Bot.Language, "memory.facts_user_header"))
			for _, f := range userFacts {
				memoryBankFormatted += fmt.Sprintf("- [ID:%d] [%s] [%s/%s] (Updated: %s) %s\n", f.ID, f.Entity, f.Category, f.Type, f.LastUpdated.Format("2006-01-02"), f.Content)
			}
			memoryBankFormatted += "\n"
		}

		if len(otherFacts) > 0 {
			memoryBankFormatted += fmt.Sprintf("%s\n", b.translator.Get(b.cfg.Bot.Language, "memory.facts_others_header"))
			for _, f := range otherFacts {
				memoryBankFormatted += fmt.Sprintf("- [ID:%d] [%s] [%s/%s] (Updated: %s) %s\n", f.ID, f.Entity, f.Category, f.Type, f.LastUpdated.Format("2006-01-02"), f.Content)
			}
			memoryBankFormatted += "\n"
		}

		memoryBankFormatted = strings.TrimRight(memoryBankFormatted, "\n")
	}

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
			// Deduplicate topics
			recentIDs := make(map[int64]bool)
			for _, msg := range recentHistory {
				recentIDs[msg.ID] = true
			}

			var filteredTopics []rag.TopicSearchResult
			for _, topicRes := range retrievedResults {
				var filteredMsgs []storage.Message
				for _, msg := range topicRes.Messages {
					if !recentIDs[msg.ID] {
						filteredMsgs = append(filteredMsgs, msg)
					}
				}
				if len(filteredMsgs) > 0 {
					topicRes.Messages = filteredMsgs
					filteredTopics = append(filteredTopics, topicRes)
				}
			}
			retrievedResults = filteredTopics
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
		orMessages = append(orMessages, openrouter.Message{Role: hMsg.Role, Content: contentParts})
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
		action, _ := params["action"].(string)
		entity, _ := params["entity"].(string)
		content, _ := params["content"].(string)
		category, _ := params["category"].(string)
		factType, _ := params["type"].(string)
		reason, _ := params["reason"].(string)

		var importance int
		if impFloat, ok := params["importance"].(float64); ok {
			importance = int(impFloat)
		}

		var factID int64
		if idFloat, ok := params["fact_id"].(float64); ok {
			factID = int64(idFloat)
		}

		var result string
		var err error

		switch action {
		case "add":
			if content == "" {
				result = "Error: Content is required for adding a fact."
				err = fmt.Errorf("missing content")
			} else {
				resp, embErr := b.orClient.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
					Model: b.cfg.RAG.EmbeddingModel,
					Input: []string{content},
				})
				if embErr != nil || len(resp.Data) == 0 {
					result = "Error generating embedding."
					err = embErr
				} else {
					if entity == "" {
						entity = "User"
					}
					if category == "" {
						category = "other"
					}
					if factType == "" {
						factType = "context"
					}
					if importance == 0 {
						importance = 50
					}

					fact := storage.Fact{
						UserID:     userID,
						Entity:     entity,
						Content:    content,
						Type:       factType,
						Importance: importance,
						Embedding:  resp.Data[0].Embedding,
						Relation:   "related_to",
						Category:   category,
						TopicID:    nil, // Manual addition has no topic
					}

					id, addErr := b.factRepo.AddFact(fact)
					if addErr != nil {
						result = "Error adding fact."
						err = addErr
					} else {
						_ = b.factHistoryRepo.AddFactHistory(storage.FactHistory{
							FactID:     id,
							UserID:     userID,
							Action:     "add",
							NewContent: content,
							Reason:     reason,
							Category:   category,
							Entity:     entity,
							Relation:   "related_to",
							Importance: importance,
							TopicID:    nil,
						})
						result = "Fact added successfully."
					}
				}
			}

		case "delete":
			if factID == 0 {
				result = "Error: Fact ID is required for deletion."
				err = fmt.Errorf("missing fact_id")
			} else {
				// Fetch old fact for history
				oldFacts, _ := b.factRepo.GetFactsByIDs([]int64{factID})
				var oldContent string
				var category, entity, relation string
				var importance int
				if len(oldFacts) > 0 {
					oldContent = oldFacts[0].Content
					category = oldFacts[0].Category
					entity = oldFacts[0].Entity
					relation = oldFacts[0].Relation
					importance = oldFacts[0].Importance
				}

				if delErr := b.factRepo.DeleteFact(userID, factID); delErr != nil {
					result = "Error deleting fact."
					err = delErr
				} else {
					_ = b.factHistoryRepo.AddFactHistory(storage.FactHistory{
						FactID:     factID,
						UserID:     userID,
						Action:     "delete",
						OldContent: oldContent,
						Reason:     reason,
						Category:   category,
						Entity:     entity,
						Relation:   relation,
						Importance: importance,
						TopicID:    nil,
					})
					result = "Fact deleted successfully."
				}
			}

		case "update":
			if factID == 0 {
				result = "Error: Fact ID is required for update."
				err = fmt.Errorf("missing fact_id")
			} else {
				resp, embErr := b.orClient.CreateEmbeddings(ctx, openrouter.EmbeddingRequest{
					Model: b.cfg.RAG.EmbeddingModel,
					Input: []string{content},
				})
				if embErr != nil || len(resp.Data) == 0 {
					result = "Error generating embedding."
					err = embErr
				} else {
					if factType == "" {
						factType = "context"
					}
					if importance == 0 {
						importance = 50
					}

					// Fetch old fact for history
					oldFacts, _ := b.factRepo.GetFactsByIDs([]int64{factID})
					var oldContent string
					var category, entity, relation string
					if len(oldFacts) > 0 {
						oldContent = oldFacts[0].Content
						category = oldFacts[0].Category
						entity = oldFacts[0].Entity
						relation = oldFacts[0].Relation
					}

					fact := storage.Fact{
						ID:         factID,
						UserID:     userID,
						Content:    content,
						Type:       factType,
						Importance: importance,
						Embedding:  resp.Data[0].Embedding,
					}
					if updErr := b.factRepo.UpdateFact(fact); updErr != nil {
						result = "Error updating fact."
						err = updErr
					} else {
						_ = b.factHistoryRepo.AddFactHistory(storage.FactHistory{
							FactID:     factID,
							UserID:     userID,
							Action:     "update",
							OldContent: oldContent,
							NewContent: content,
							Reason:     reason,
							Category:   category,
							Entity:     entity,
							Relation:   relation,
							Importance: importance,
							TopicID:    nil,
						})
						result = "Fact updated successfully."
					}
				}
			}

		default:
			result = fmt.Sprintf("Unknown action: %s", action)
			err = fmt.Errorf("unknown action")
		}

		if err != nil {
			errorCount++
			results = append(results, fmt.Sprintf("Op %d (%s): Failed - %s (%v)", i+1, action, result, err))
		} else {
			results = append(results, fmt.Sprintf("Op %d (%s): Success", i+1, action))
		}
	}

	finalResult := strings.Join(results, "\n")
	if errorCount > 0 {
		return fmt.Sprintf("Completed with %d errors:\n%s", errorCount, finalResult), nil
	}
	return fmt.Sprintf("Successfully processed %d operations:\n%s", len(operations), finalResult), nil
}

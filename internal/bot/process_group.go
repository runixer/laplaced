package bot

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/markdown"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
)

func (b *Bot) processMessageGroup(ctx context.Context, group *MessageGroup) {
	if len(group.Messages) == 0 {
		return
	}

	lastMsg := group.Messages[len(group.Messages)-1]
	user := lastMsg.From

	// Track processing time and success for metrics
	startTime := time.Now()
	success := false
	defer func() {
		duration := time.Since(startTime).Seconds()
		RecordMessageProcessing(user.ID, duration, success)
	}()
	logger := b.logger.With(
		"user_id", user.ID,
		"username", user.Username,
		"first_name", user.FirstName,
		"last_name", user.LastName,
	)

	logger.Info("processing message group", "message_count", len(group.Messages))

	chatID := lastMsg.Chat.ID

	// Use non-cancellable context for all operations that should complete during shutdown.
	// This ensures RAG retrieval, LLM generation, and response sending all complete
	// even when graceful shutdown is triggered.
	shutdownSafeCtx := context.WithoutCancel(ctx)

	// React to the message with a certain probability
	if rand.Float32() < 0.1 { // 10% chance
		reactionStart := time.Now()
		reaction := availableReactions[rand.IntN(len(availableReactions))]
		reactionReq := telegram.SetMessageReactionRequest{
			ChatID:    chatID,
			MessageID: lastMsg.MessageID,
			Reaction: []telegram.ReactionType{
				{Type: "emoji", Emoji: reaction},
			},
		}
		if err := b.api.SetMessageReaction(shutdownSafeCtx, reactionReq); err != nil {
			logger.Warn("failed to set message reaction", "error", err)
		}
		RecordMessageReaction(user.ID, time.Since(reactionStart).Seconds())
	}

	typingCtx, cancelTyping := context.WithCancel(shutdownSafeCtx)
	defer cancelTyping()
	go b.sendTypingActionLoop(typingCtx, chatID, lastMsg.MessageThreadID)

	historyContent, rawQuery, currentUserMessageContent, err := b.prepareUserMessage(shutdownSafeCtx, group, logger)
	if err != nil {
		b.sendResponses(shutdownSafeCtx, chatID, []telegram.SendMessageRequest{{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.api_error")}}, logger)
		return
	}

	if len(currentUserMessageContent) == 0 {
		logger.Warn("message group was empty after processing")
		return
	}

	userID := group.UserID

	// Load SharedContext once for all agents in this request
	if b.contextService != nil {
		shared := b.contextService.Load(shutdownSafeCtx, userID)
		shutdownSafeCtx = agent.WithContext(shutdownSafeCtx, shared)
	}

	if err := b.msgRepo.AddMessageToHistory(userID, storage.Message{Role: "user", Content: historyContent}); err != nil {
		logger.Error("failed to add message to history", "error", err)
		return
	}

	// Per-message breakdown tracking
	var totalTelegramDuration time.Duration
	telegramCallCount := 0

	// Defer Telegram metrics recording to capture early returns
	defer func() {
		if telegramCallCount > 0 {
			RecordMessageTelegram(userID, totalTelegramDuration.Seconds(), telegramCallCount)
		}
	}()

	// Execute via Laplace agent
	if b.laplaceAgent == nil {
		logger.Error("laplace agent not configured")
		b.sendResponses(shutdownSafeCtx, chatID, []telegram.SendMessageRequest{{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.generic_error")}}, logger)
		return
	}

	// Create tool handler
	toolHandler := b.newBotToolHandler(shutdownSafeCtx, userID, logger)

	// Build request
	req := &laplace.Request{
		UserID:              userID,
		HistoryContent:      historyContent,
		RawQuery:            rawQuery,
		CurrentMessageParts: currentUserMessageContent,
		ChatID:              chatID,
		MessageThreadID:     lastMsg.MessageThreadID,
		ReplyToMsgID:        lastMsg.MessageID,
		OnIntermediateMessage: func(text string) {
			responses, err := b.finalizeResponse(chatID, lastMsg.MessageThreadID, userID, 0, text, logger)
			if err != nil {
				logger.Error("failed to finalize intermediate response", "error", err)
				return
			}
			tgStart := time.Now()
			b.sendResponses(shutdownSafeCtx, chatID, responses, logger)
			totalTelegramDuration += time.Since(tgStart)
			telegramCallCount += len(responses)
		},
		OnTypingAction: func() {
			b.sendAction(shutdownSafeCtx, chatID, lastMsg.MessageThreadID, "typing")
		},
	}

	// Execute
	resp, err := b.laplaceAgent.Execute(shutdownSafeCtx, req, toolHandler)
	if err != nil {
		logger.Error("laplace execution failed", "error", err)
		tgStart := time.Now()
		b.sendResponses(shutdownSafeCtx, chatID, []telegram.SendMessageRequest{{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.api_error")}}, logger)
		totalTelegramDuration += time.Since(tgStart)
		telegramCallCount++
		return
	}

	// Record metrics
	RecordMessageLLM(userID, resp.LLMDuration.Seconds(), resp.TotalTurns)
	if resp.ToolDuration > 0 {
		RecordMessageTools(userID, resp.ToolDuration.Seconds(), resp.TotalTurns)
	}

	// Finalize response
	responses, err := b.finalizeResponse(chatID, lastMsg.MessageThreadID, userID, lastMsg.MessageID, resp.Content, logger)
	if err != nil {
		logger.Error("failed to finalize response", "error", err)
	}

	// Calculate cost
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
	b.laplaceAgent.LogExecution(shutdownSafeCtx, userID, resp, cost)

	// Record context tokens by source
	RecordContextTokens(resp.PromptTokens)

	logger.Info("usage stats recorded",
		"prompt_tokens", resp.PromptTokens,
		"completion_tokens", resp.CompletionTokens,
		"total_tokens", stat.TokensUsed,
		"cost_usd", stat.CostUSD,
	)

	// Save assistant response to history
	if err := b.msgRepo.AddMessageToHistory(userID, storage.Message{Role: "assistant", Content: resp.Content}); err != nil {
		logger.Error("failed to add assistant message to history", "error", err)
	}

	// Send response
	tgStart := time.Now()
	b.sendResponses(shutdownSafeCtx, chatID, responses, logger)
	totalTelegramDuration += time.Since(tgStart)
	telegramCallCount += len(responses)

	success = true
}

func (b *Bot) prepareUserMessage(ctx context.Context, group *MessageGroup, logger *slog.Logger) (string, string, []interface{}, error) {
	// 1. Download all files in parallel across messages
	downloadedFiles := make([][]*files.ProcessedFile, len(group.Messages))

	g, gCtx := errgroup.WithContext(ctx)
	for i, msg := range group.Messages {
		i, msg := i, msg // capture for goroutine
		g.Go(func() error {
			result, err := b.fileProcessor.ProcessMessage(gCtx, msg, group.UserID)
			if err != nil {
				return err // context cancelled
			}
			downloadedFiles[i] = result
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return "", "", nil, err
	}

	// 2. Process messages sequentially (order preserved)
	var historyBuilder strings.Builder
	var rawQueryBuilder strings.Builder
	var llmParts []interface{}

	for i, msg := range group.Messages {
		// Text content (message text or caption)
		textContent := msg.BuildContent(b.translator, b.cfg.Bot.Language)
		if textContent != "" {
			appendWithNewline(&historyBuilder, textContent)
			llmParts = append(llmParts, openrouter.TextPart{Type: "text", Text: textContent})
		}

		// Process pre-downloaded files
		for _, f := range downloadedFiles[i] {
			// Add instruction before file if present (e.g., voice handling instructions)
			if f.Instruction != "" {
				prefix := msg.BuildPrefix(b.translator, b.cfg.Bot.Language)
				llmParts = append(llmParts, openrouter.TextPart{
					Type: "text",
					Text: fmt.Sprintf("%s: %s", prefix, f.Instruction),
				})
			}

			// Add file parts to LLM message
			llmParts = append(llmParts, f.LLMParts...)

			// Handle history and RAG query based on file type
			switch f.FileType {
			case files.FileTypeVoice:
				// Voice: add marker to history and RAG query
				prefix := msg.BuildPrefix(b.translator, b.cfg.Bot.Language)
				voiceMarker := b.translator.Get(b.cfg.Bot.Language, "bot.voice_message_marker")
				appendWithNewline(&historyBuilder, fmt.Sprintf("%s: %s", prefix, voiceMarker))
				appendWithNewline(&rawQueryBuilder, voiceMarker)

				logger.Info("processed voice message",
					"file_id", f.FileID,
					"size", f.Size,
					"duration_ms", f.Duration.Milliseconds(),
				)

			case files.FileTypeDocument:
				// Text document: content is already in LLMParts as TextPart
				// Also add to history for context
				if len(f.LLMParts) > 0 {
					if tp, ok := f.LLMParts[0].(openrouter.TextPart); ok {
						appendWithNewline(&historyBuilder, tp.Text)
					}
				}

			default:
				// Photo, Image, PDF: just log, no special history handling
				logger.Debug("processed file",
					"type", f.FileType,
					"file_id", f.FileID,
					"size", f.Size,
					"duration_ms", f.Duration.Milliseconds(),
				)
			}
		}

		// Build RAG query from text
		txt := msg.Text
		if txt == "" {
			txt = msg.Caption
		}
		appendWithNewline(&rawQueryBuilder, txt)
	}

	return historyBuilder.String(), rawQueryBuilder.String(), llmParts, nil
}

// appendWithNewline appends string to builder with newline separator if not empty.
func appendWithNewline(b *strings.Builder, s string) {
	if s == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString(s)
}

// splitByDelimiter splits text by ###SPLIT### delimiter, respecting code blocks.
// Delimiters inside code blocks are ignored. Empty parts are filtered out.
func splitByDelimiter(text string) []string {
	const delimiter = "###SPLIT###"

	if !strings.Contains(text, delimiter) {
		return []string{text}
	}

	codeBlocks := telegram.FindCodeBlocks(text)

	var splitPositions []int
	searchStart := 0
	for {
		idx := strings.Index(text[searchStart:], delimiter)
		if idx == -1 {
			break
		}
		pos := searchStart + idx
		if telegram.IsSafeSplitPosition(pos, codeBlocks) {
			splitPositions = append(splitPositions, pos)
		}
		searchStart = pos + len(delimiter)
	}

	if len(splitPositions) == 0 {
		return []string{text}
	}

	var parts []string
	start := 0
	for _, pos := range splitPositions {
		if part := strings.TrimSpace(text[start:pos]); part != "" {
			parts = append(parts, part)
		}
		start = pos + len(delimiter)
	}
	if remaining := strings.TrimSpace(text[start:]); remaining != "" {
		parts = append(parts, remaining)
	}

	if len(parts) == 0 {
		return []string{text}
	}
	return parts
}

func (b *Bot) finalizeResponse(chatID int64, messageThreadID int, userID int64, replyToMsgID int, responseText string, logger *slog.Logger) ([]telegram.SendMessageRequest, error) {
	// Split by explicit ###SPLIT### delimiter first (respects code blocks)
	delimiterParts := splitByDelimiter(responseText)

	// Then split each part by character limit (safety net for oversized chunks)
	const markdownSafeLimit = 3500
	var chunks []string
	for _, part := range delimiterParts {
		chunks = append(chunks, telegram.SplitMessageSmart(part, markdownSafeLimit)...)
	}

	var responses []telegram.SendMessageRequest
	for i, chunk := range chunks {
		// Convert each chunk to HTML
		htmlChunk, err := markdown.ToHTML(chunk)
		if err != nil {
			// Fallback: send as plain text with HTML escaping
			logger.Warn("failed to convert markdown to HTML, using plain text", "error", err)
			htmlChunk = html.EscapeString(chunk)
		}

		// Check UTF-16 length (Telegram's limit)
		if markdown.UTF16Length(htmlChunk) > telegramMessageLimit {
			logger.Warn("HTML chunk exceeds Telegram limit after conversion",
				"utf16_length", markdown.UTF16Length(htmlChunk),
				"limit", telegramMessageLimit)
		}

		newMsg := telegram.SendMessageRequest{
			ChatID:          chatID,
			MessageThreadID: intPtrOrNil(messageThreadID),
			Text:            htmlChunk,
			ParseMode:       "HTML",
		}
		if i == 0 && replyToMsgID != 0 {
			newMsg.ReplyToMessageID = replyToMsgID
		}
		responses = append(responses, newMsg)
	}

	return responses, nil
}

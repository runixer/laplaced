package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/markdown"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/rag"
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

	if err := b.msgRepo.AddMessageToHistory(userID, storage.Message{Role: "user", Content: historyContent}); err != nil {
		logger.Error("failed to add message to history", "error", err)
		return
	}

	orMessages, ragInfo, err := b.buildContext(shutdownSafeCtx, userID, historyContent, rawQuery, currentUserMessageContent)
	if err != nil {
		logger.Error("failed to build context", "error", err)
		b.sendResponses(shutdownSafeCtx, chatID, []telegram.SendMessageRequest{{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.generic_error")}}, logger)
		return
	}

	var plugins []openrouter.Plugin
	if b.cfg.OpenRouter.PDFParserEngine != "" {
		plugins = append(plugins, openrouter.Plugin{
			ID: "file-parser",
			PDF: openrouter.PDFConfig{
				Engine: b.cfg.OpenRouter.PDFParserEngine,
			},
		})
	}

	// Tool Handling Loop
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

		tools := b.getTools()

		req := openrouter.ChatCompletionRequest{
			Model:    b.cfg.OpenRouter.Model,
			Messages: orMessages,
			Plugins:  plugins,
			Tools:    tools,
			UserID:   user.ID,
		}

		resp, err := b.orClient.CreateChatCompletion(shutdownSafeCtx, req)
		if err != nil {
			logger.Error("failed to get completion from OpenRouter", "error", err)
			b.sendResponses(shutdownSafeCtx, chatID, []telegram.SendMessageRequest{{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.api_error")}}, logger)
			return
		}

		if len(resp.Choices) == 0 {
			logger.Warn("empty response from OpenRouter")
			b.sendResponses(shutdownSafeCtx, chatID, []telegram.SendMessageRequest{{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.empty_response")}}, logger)
			return
		}

		totalPromptTokens += resp.Usage.PromptTokens
		totalCompletionTokens += resp.Usage.CompletionTokens

		choice := resp.Choices[0].Message

		// Check for tool calls
		if len(choice.ToolCalls) > 0 {
			logger.Info("Model requested tool calls", "count", len(choice.ToolCalls))

			var content interface{} = choice.Content
			if choice.Content == "" {
				content = nil
			}

			// Send intermediate message to user if present
			if choice.Content != "" && strings.TrimSpace(choice.Content) != "" {
				intermediateResponses, err := b.finalizeResponse(chatID, lastMsg.MessageThreadID, userID, 0, choice.Content, logger)
				if err != nil {
					logger.Error("failed to finalize intermediate response", "error", err)
				} else {
					b.sendResponses(shutdownSafeCtx, chatID, intermediateResponses, logger)
				}
			}

			// Add assistant message with tool calls to history
			orMessages = append(orMessages, openrouter.Message{
				Role:             "assistant",
				Content:          content,
				ToolCalls:        choice.ToolCalls,
				ReasoningDetails: choice.ReasoningDetails,
			})

			toolMessages, err := b.executeToolCalls(shutdownSafeCtx, chatID, lastMsg.MessageThreadID, userID, choice.ToolCalls, logger)
			if err != nil {
				// executeToolCalls logs errors but returns messages with error info if needed
				// If it returns a critical error, we might want to stop, but currently it returns messages
				_ = err // Error already logged in executeToolCalls
			}
			orMessages = append(orMessages, toolMessages...)

			// Loop back to call model again with tool results
			continue
		}

		// Final response
		finalResponse = choice.Content
		break
	}

	// Handle empty response from model
	if strings.TrimSpace(finalResponse) == "" {
		logger.Warn("model returned empty response")
		finalResponse = b.translator.Get(b.cfg.Bot.Language, "bot.empty_response")
	}

	responses, err := b.finalizeResponse(chatID, lastMsg.MessageThreadID, userID, lastMsg.MessageID, finalResponse, logger)
	if err != nil {
		logger.Error("failed to finalize response", "error", err)
	}

	b.recordMetrics(userID, totalPromptTokens, totalCompletionTokens, ragInfo, orMessages, finalResponse, logger)

	// Record context tokens for metrics (approximate based on prompt tokens)
	RecordContextTokens(totalPromptTokens)

	b.sendResponses(shutdownSafeCtx, chatID, responses, logger)
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

func (b *Bot) executeToolCalls(ctx context.Context, chatID int64, messageThreadID int, userID int64, toolCalls []openrouter.ToolCall, logger *slog.Logger) ([]openrouter.Message, error) {
	var toolMessages []openrouter.Message

	for _, toolCall := range toolCalls {
		b.sendAction(ctx, chatID, messageThreadID, "typing")

		// 1. Handle Configured Tools
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

			// Standard tools expecting "query"
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
	return toolMessages, nil
}

func (b *Bot) finalizeResponse(chatID int64, messageThreadID int, userID int64, replyToMsgID int, responseText string, logger *slog.Logger) ([]telegram.SendMessageRequest, error) {
	// Split the raw Markdown first (conservative limit to account for HTML tags)
	const markdownSafeLimit = 3500
	chunks := telegram.SplitMessageSmart(responseText, markdownSafeLimit)

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

	if err := b.msgRepo.AddMessageToHistory(userID, storage.Message{Role: "assistant", Content: responseText}); err != nil {
		logger.Error("failed to add assistant message to history", "error", err)
		// We don't return error here because we still want to send the message
	}

	return responses, nil
}

func (b *Bot) recordMetrics(userID int64, promptTokens, completionTokens int, ragInfo *rag.RetrievalDebugInfo, orMessages []openrouter.Message, finalResponse string, logger *slog.Logger) {
	cost := b.getTieredCost(promptTokens, completionTokens, logger)

	stat := storage.Stat{
		UserID:     userID,
		TokensUsed: promptTokens + completionTokens,
		CostUSD:    cost,
	}
	if err := b.statsRepo.AddStat(stat); err != nil {
		logger.Error("failed to add stat", "error", err)
	}

	// Save RAG Log
	if ragInfo != nil {
		sysPrompt := ""
		if len(orMessages) > 0 && orMessages[0].Role == "system" {
			if str, ok := orMessages[0].Content.(string); ok {
				sysPrompt = str
			} else if parts, ok := orMessages[0].Content.([]interface{}); ok {
				// extract text parts
				for _, p := range parts {
					if tp, ok := p.(openrouter.TextPart); ok {
						sysPrompt += tp.Text
					}
				}
			}
		}

		// JSON marshaling
		contextBytes, _ := json.Marshal(orMessages)

		var resultsJSON []byte
		if len(ragInfo.Results) > 0 {
			resultsJSON, _ = json.Marshal(ragInfo.Results)
		}

		ragLog := storage.RAGLog{
			UserID:           userID,
			OriginalQuery:    ragInfo.OriginalQuery,
			EnrichedQuery:    ragInfo.EnrichedQuery,
			EnrichmentPrompt: ragInfo.EnrichmentPrompt,
			ContextUsed:      string(contextBytes),
			SystemPrompt:     sysPrompt,
			RetrievalResults: string(resultsJSON),
			LLMResponse:      finalResponse,
			EnrichmentTokens: ragInfo.EnrichmentTokens,
			GenerationTokens: promptTokens + completionTokens,
			TotalCostUSD:     cost,
		}

		if err := b.logRepo.AddRAGLog(ragLog); err != nil {
			logger.Error("failed to add rag log", "error", err)
		}
	}

	logger.Info("usage stats recorded",
		"prompt_tokens", promptTokens,
		"completion_tokens", completionTokens,
		"total_tokens", stat.TokensUsed,
		"cost_usd", stat.CostUSD,
	)
}

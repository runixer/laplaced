package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/runixer/laplaced/internal/agentlog"
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
	const maxEmptyRetries = 2
	emptyRetries := 0

	// Per-message breakdown tracking
	var totalLLMDuration time.Duration
	var totalToolDuration time.Duration
	var totalTelegramDuration time.Duration
	llmCallCount := 0
	toolCallCount := 0
	telegramCallCount := 0
	var lastRawRequestBody string // Capture last LLM request for debugging

	// Defer Telegram metrics recording to capture early returns
	defer func() {
		if telegramCallCount > 0 {
			RecordMessageTelegram(userID, totalTelegramDuration.Seconds(), telegramCallCount)
		}
	}()

	for {
		if toolIterations >= maxToolIterations {
			logger.Warn("max tool iterations reached", "iterations", toolIterations)
			break
		}
		toolIterations++

		tools := b.getTools()

		req := openrouter.ChatCompletionRequest{
			Model:    b.cfg.Agents.Chat.GetModel(b.cfg.Agents.Default.Model),
			Messages: orMessages,
			Plugins:  plugins,
			Tools:    tools,
			UserID:   user.ID,
		}

		llmStart := time.Now()
		resp, err := b.orClient.CreateChatCompletion(shutdownSafeCtx, req)
		llmDuration := time.Since(llmStart)
		totalLLMDuration += llmDuration
		llmCallCount++

		if err != nil {
			logger.Error("failed to get completion from OpenRouter", "error", err)
			tgStart := time.Now()
			b.sendResponses(shutdownSafeCtx, chatID, []telegram.SendMessageRequest{{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.api_error")}}, logger)
			totalTelegramDuration += time.Since(tgStart)
			telegramCallCount++
			return
		}

		if len(resp.Choices) == 0 {
			logger.Warn("empty choices from OpenRouter",
				"model", resp.Model,
				"prompt_tokens", resp.Usage.PromptTokens,
				"completion_tokens", resp.Usage.CompletionTokens,
			)
			RecordLLMAnomaly(userID, AnomalyEmptyResponse)
			tgStart := time.Now()
			b.sendResponses(shutdownSafeCtx, chatID, []telegram.SendMessageRequest{{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.empty_response")}}, logger)
			totalTelegramDuration += time.Since(tgStart)
			telegramCallCount++
			return
		}

		totalPromptTokens += resp.Usage.PromptTokens
		totalCompletionTokens += resp.Usage.CompletionTokens
		lastRawRequestBody = resp.DebugRequestBody // Capture for logging

		choice := resp.Choices[0]

		// Check for empty response (no content and no tool calls) - retry
		if strings.TrimSpace(choice.Message.Content) == "" && len(choice.Message.ToolCalls) == 0 {
			logger.Warn("empty LLM response (no content, no tools)",
				"finish_reason", choice.FinishReason,
				"model", resp.Model,
				"completion_tokens", resp.Usage.CompletionTokens,
				"retry_attempt", emptyRetries+1,
				"max_retries", maxEmptyRetries,
			)
			RecordLLMAnomaly(userID, AnomalyEmptyResponse)

			if emptyRetries < maxEmptyRetries {
				emptyRetries++
				logger.Info("retrying after empty response", "attempt", emptyRetries)
				continue
			}

			// Max retries reached
			logger.Warn("max empty response retries reached, giving up")
			RecordLLMAnomaly(userID, AnomalyRetryFailed)
			tgStart := time.Now()
			b.sendResponses(shutdownSafeCtx, chatID, []telegram.SendMessageRequest{{ChatID: chatID, Text: b.translator.Get(b.cfg.Bot.Language, "bot.empty_response")}}, logger)
			totalTelegramDuration += time.Since(tgStart)
			telegramCallCount++
			return
		}

		// If we had retries and finally got content, record success
		if emptyRetries > 0 {
			logger.Info("retry successful after empty response", "attempts", emptyRetries)
			RecordLLMAnomaly(userID, AnomalyRetrySuccess)
			emptyRetries = 0 // reset for potential future retries in tool loop
		}

		// Check for tool calls
		if len(choice.Message.ToolCalls) > 0 {
			logger.Info("Model requested tool calls", "count", len(choice.Message.ToolCalls))

			var content interface{} = choice.Message.Content
			if choice.Message.Content == "" {
				content = nil
			}

			// Send intermediate message to user if present
			if choice.Message.Content != "" && strings.TrimSpace(choice.Message.Content) != "" {
				intermediateResponses, err := b.finalizeResponse(chatID, lastMsg.MessageThreadID, userID, 0, choice.Message.Content, logger)
				if err != nil {
					logger.Error("failed to finalize intermediate response", "error", err)
				} else {
					tgStart := time.Now()
					b.sendResponses(shutdownSafeCtx, chatID, intermediateResponses, logger)
					totalTelegramDuration += time.Since(tgStart)
					telegramCallCount += len(intermediateResponses)
				}
			}

			// Add assistant message with tool calls to history
			orMessages = append(orMessages, openrouter.Message{
				Role:             "assistant",
				Content:          content,
				ToolCalls:        choice.Message.ToolCalls,
				ReasoningDetails: choice.Message.ReasoningDetails,
			})

			toolStart := time.Now()
			toolMessages, err := b.executeToolCalls(shutdownSafeCtx, chatID, lastMsg.MessageThreadID, userID, choice.Message.ToolCalls, logger)
			totalToolDuration += time.Since(toolStart)
			toolCallCount += len(choice.Message.ToolCalls)

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
		finalResponse = choice.Message.Content
		break
	}

	// Record per-message breakdown metrics
	RecordMessageLLM(userID, totalLLMDuration.Seconds(), llmCallCount)
	RecordMessageTools(userID, totalToolDuration.Seconds(), toolCallCount)

	// Handle empty response from model
	if strings.TrimSpace(finalResponse) == "" {
		logger.Warn("model returned empty response")
		finalResponse = b.translator.Get(b.cfg.Bot.Language, "bot.empty_response")
	}

	responses, err := b.finalizeResponse(chatID, lastMsg.MessageThreadID, userID, lastMsg.MessageID, finalResponse, logger)
	if err != nil {
		logger.Error("failed to finalize response", "error", err)
	}

	b.recordMetrics(userID, totalPromptTokens, totalCompletionTokens, ragInfo, orMessages, finalResponse, lastRawRequestBody, totalLLMDuration, logger)

	// Record context tokens for metrics (approximate based on prompt tokens)
	RecordContextTokens(totalPromptTokens)

	tgStart := time.Now()
	b.sendResponses(shutdownSafeCtx, chatID, responses, logger)
	totalTelegramDuration += time.Since(tgStart)
	telegramCallCount += len(responses)

	// Telegram metrics recorded by defer

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
				case "search_history":
					logger.Info("Executing history search tool", "tool", matchedTool.Name, "query", query)
					toolResult, err = b.performHistorySearch(ctx, userID, query)
				case "manage_memory":
					logger.Info("Executing Manage Memory tool", "tool", matchedTool.Name)
					toolResult, err = b.performManageMemory(ctx, userID, args)
				default:
					logger.Info("Executing model tool", "tool", matchedTool.Name, "model", matchedTool.Model, "query", query)
					toolResult, err = b.performModelTool(ctx, userID, matchedTool.Model, query)
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

// hallucinationTags are special tokens that LLMs sometimes hallucinate in text output.
// These should never appear in normal response text.
var hallucinationTags = []string{
	"</tool_code>",
	"</tool_call>",
	"</s>",
	"<|endoftext|>",
	"<|end|>",
	// Gemini sometimes outputs its internal tool call format as text instead of proper tool_calls.
	// This "default_api:" prefix is from OpenAPI Generator code patterns in training data.
	// See: https://github.com/OpenAPITools/openapi-generator
	"default_api:",
}

// consecutiveJSONBlocksPattern matches 3+ consecutive markdown JSON code blocks.
// This pattern detects runaway generation where model outputs endless JSON blocks.
var consecutiveJSONBlocksPattern = regexp.MustCompile("(?s)(```json\\s*\\{[^`]*\\}\\s*```\\s*){3,}")

// defaultAPIPattern matches Gemini's hallucinated tool call format.
// Format: default_api:<tool_name>{query:<json>}
// The JSON can contain nested braces, so we need to match balanced braces.
var defaultAPIPattern = regexp.MustCompile(`default_api:\w+\{`)

// sanitizeLLMResponse removes hallucination artifacts from LLM responses.
// It truncates on special tags and detects runaway JSON block generation.
// Returns sanitized text and whether any sanitization was applied.
func sanitizeLLMResponse(text string) (string, bool) {
	original := text
	sanitized := false

	// 1. Remove Gemini's "default_api:" hallucinated tool calls
	// These appear when the model outputs internal tool format as text
	if loc := defaultAPIPattern.FindStringIndex(text); loc != nil {
		// Find matching closing brace (handle nested braces)
		startIdx := loc[1] - 1 // Position of opening '{'
		endIdx := findMatchingBrace(text, startIdx)
		if endIdx != -1 {
			// Remove the entire default_api:...{...} block
			before := strings.TrimSpace(text[:loc[0]])
			after := strings.TrimSpace(text[endIdx+1:])
			if before != "" && after != "" {
				text = before + "\n\n" + after
			} else if after != "" {
				text = after
			} else {
				text = before
			}
			sanitized = true
		}
	}

	// 2. Truncate on other hallucination tags (but skip default_api: as it's handled above)
	for _, tag := range hallucinationTags {
		if tag == "default_api:" {
			continue // Already handled with brace matching
		}
		if idx := strings.Index(text, tag); idx != -1 {
			text = strings.TrimSpace(text[:idx])
			sanitized = true
		}
	}

	// 3. Truncate on 3+ consecutive JSON blocks (runaway generation)
	if loc := consecutiveJSONBlocksPattern.FindStringIndex(text); loc != nil {
		text = strings.TrimSpace(text[:loc[0]])
		sanitized = true
	}

	// Don't return empty string if we sanitized everything
	if sanitized && text == "" {
		return original, false
	}

	return text, sanitized
}

// findMatchingBrace finds the index of the closing brace that matches the opening brace at startIdx.
// Returns -1 if no matching brace is found.
func findMatchingBrace(text string, startIdx int) int {
	if startIdx >= len(text) || text[startIdx] != '{' {
		return -1
	}

	depth := 0
	inString := false
	escaped := false

	for i := startIdx; i < len(text); i++ {
		c := text[i]

		if escaped {
			escaped = false
			continue
		}

		if c == '\\' && inString {
			escaped = true
			continue
		}

		if c == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
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
	// Sanitize LLM response to remove hallucination artifacts
	originalLength := len(responseText)
	responseText, wasSanitized := sanitizeLLMResponse(responseText)
	if wasSanitized {
		logger.Warn("sanitized LLM response (removed hallucination artifacts)",
			"original_length", originalLength,
			"sanitized_length", len(responseText),
		)
		RecordLLMAnomaly(userID, AnomalySanitized)
	}

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

	if err := b.msgRepo.AddMessageToHistory(userID, storage.Message{Role: "assistant", Content: responseText}); err != nil {
		logger.Error("failed to add assistant message to history", "error", err)
		// We don't return error here because we still want to send the message
	}

	return responses, nil
}

func (b *Bot) recordMetrics(userID int64, promptTokens, completionTokens int, ragInfo *rag.RetrievalDebugInfo, orMessages []openrouter.Message, finalResponse string, rawRequestBody string, totalLLMDuration time.Duration, logger *slog.Logger) {
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
		// Format full prompt as human-readable text
		fullPrompt := formatMessagesForLog(orMessages)

		var resultsJSON []byte
		if len(ragInfo.Results) > 0 {
			resultsJSON, _ = json.Marshal(ragInfo.Results)
		}

		// Log to agent_logs for debugging
		if b.agentLogger != nil {
			b.agentLogger.Log(context.Background(), agentlog.Entry{
				UserID:           userID,
				AgentType:        agentlog.AgentLaplace,
				InputPrompt:      fullPrompt,
				InputContext:     rawRequestBody, // Full API request JSON
				OutputResponse:   finalResponse,
				Model:            b.cfg.Agents.Chat.GetModel(b.cfg.Agents.Default.Model),
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
				TotalCost:        &cost,
				DurationMs:       int(totalLLMDuration.Milliseconds()),
				Metadata: map[string]interface{}{
					"original_query":    ragInfo.OriginalQuery,
					"enriched_query":    ragInfo.EnrichedQuery,
					"retrieval_results": string(resultsJSON),
				},
				Success: true,
			})
		}
	}

	logger.Info("usage stats recorded",
		"prompt_tokens", promptTokens,
		"completion_tokens", completionTokens,
		"total_tokens", stat.TokensUsed,
		"cost_usd", stat.CostUSD,
	)
}

// formatMessagesForLog formats OpenRouter messages into a human-readable string for logging.
// Format is designed for easy visual parsing in debug UI.
func formatMessagesForLog(messages []openrouter.Message) string {
	var sb strings.Builder
	for i, msg := range messages {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		// Role header
		role := strings.ToUpper(msg.Role)
		sb.WriteString(fmt.Sprintf("=== %s ===\n", role))

		// Extract text content
		switch content := msg.Content.(type) {
		case string:
			sb.WriteString(content)
		case []interface{}:
			// Multimodal content - extract text parts
			for _, part := range content {
				if tp, ok := part.(openrouter.TextPart); ok {
					sb.WriteString(tp.Text)
				} else if m, ok := part.(map[string]interface{}); ok {
					if t, ok := m["type"].(string); ok && t == "text" {
						if text, ok := m["text"].(string); ok {
							sb.WriteString(text)
						}
					} else if t == "image_url" {
						sb.WriteString("[IMAGE]")
					} else if t == "input_audio" {
						sb.WriteString("[AUDIO]")
					}
				}
			}
		}

		// Show tool calls if present
		if len(msg.ToolCalls) > 0 {
			sb.WriteString("\n\n[TOOL CALLS]")
			for _, tc := range msg.ToolCalls {
				sb.WriteString(fmt.Sprintf("\n- %s(%s)", tc.Function.Name, tc.Function.Arguments))
			}
		}
	}
	return sb.String()
}

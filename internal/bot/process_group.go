package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/llm"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
)

// disclaimerPhrases are case-folded fragments that signal a model refusing to
// read a file. detectFalseDisclaimer flags a turn when one of these appears
// alongside a substantive answer body — i.e. the model said "couldn't read
// the file" and still answered correctly using its visible content. The
// canonical case is the historical artifact / live attachment filename
// collision, where reasoning-mode Pro models prepend a defensive disclaimer
// despite having read the live photo. Match list is intentionally short and
// high-precision; widen only after observing prod traces.
var disclaimerPhrases = []string{
	"не смог прочитать этот файл",
	"не удалось прочитать",
	"не распозналось",
	"формат не поддерживается",
	"i can't read this file",
	"i couldn't read",
	"i'm unable to read",
	"cannot interpret the image",
}

// detectFalseDisclaimer returns the matched disclaimer phrase when reply
// contains a refusal AND has a substantive body alongside it. Returns ""
// when no anomaly is suspected. minSubstantiveLen filters out genuine
// short refusals where the model truly couldn't read the file.
func detectFalseDisclaimer(reply string) string {
	const minSubstantiveLen = 300
	if len(reply) < minSubstantiveLen {
		return ""
	}
	lower := strings.ToLower(reply)
	for _, p := range disclaimerPhrases {
		if strings.Contains(lower, p) {
			return p
		}
	}
	return ""
}

// voiceQuotePrefix matches a leading "> 🎤 ...\n+" Telegram voice
// transcription quote that bot.voice_instruction tells the LLM to prepend
// to voice replies. detectEchoOfLastUserMessage strips it so the echo
// check compares the substantive reply, not the boilerplate quote.
var voiceQuotePrefix = regexp.MustCompile(`(?s)^>\s*🎤\s*[^\n]*\n+`)

// detectEchoOfLastUserMessage returns true when the assistant reply is
// (after trim + voice-quote stripping) byte-identical to the last user
// message text. Catches a Gemini 3.x degenerate failure mode where the
// model copies the user's input as its completion. Min length 30 chars
// to avoid false positives on trivially short replies (e.g. "ok").
func detectEchoOfLastUserMessage(reply, userText string) bool {
	const minLen = 30
	cleaned := strings.TrimSpace(voiceQuotePrefix.ReplaceAllString(reply, ""))
	user := strings.TrimSpace(userText)
	if len(cleaned) < minLen || len(user) < minLen {
		return false
	}
	return cleaned == user
}

// recordLaplaceAnomalies surfaces the four bot.anomaly.* span attributes
// on the root processMessageGroup span. Called once per turn after
// laplace.Execute returns, regardless of resp.Error — empty_response in
// particular fires on the retries-exhausted path which is also the error
// path. Sanitized / echo / false_disclaimer guards safely no-op when
// resp.Content is empty.
func recordLaplaceAnomalies(span trace.Span, resp *laplace.Response, userText string, hasCurrentMedia bool) {
	// false_disclaimer — model claimed it couldn't read the attachment
	// yet still produced a long answer. Only meaningful with attached media.
	if hasCurrentMedia {
		if phrase := detectFalseDisclaimer(resp.Content); phrase != "" {
			span.SetAttributes(
				attribute.Bool("bot.anomaly.false_disclaimer", true),
				attribute.String("bot.anomaly.disclaimer_phrase", phrase),
			)
		}
	}

	// empty_response — laplace produced no usable content. resp.Content
	// will be the localized fallback string (success path) or empty (error
	// path); either way the upstream failure is the signal.
	if resp.WasEmpty {
		span.SetAttributes(attribute.Bool("bot.anomaly.empty_response", true))
	}

	// sanitized — hallucination artifacts (</tool_code>, default_api: ...)
	// were stripped from the completion. OriginalContent (pre-sanitization)
	// goes on a span event so it inherits the trace_content privacy toggle.
	if resp.WasSanitized {
		span.SetAttributes(attribute.Bool("bot.anomaly.sanitized", true))
		obs.RecordContent(span, "bot.anomaly.sanitized_from", resp.OriginalContent)
	}

	// echo_user_message — Gemini degenerate failure mode where the model
	// returns the user's last message verbatim as its completion.
	if detectEchoOfLastUserMessage(resp.Content, userText) {
		span.SetAttributes(
			attribute.Bool("bot.anomaly.echo_user_message", true),
			attribute.Int("bot.anomaly.echo_output_tokens", resp.CompletionTokens),
		)
	}

	// fabricated_url — the model emitted source links whose URLs weren't
	// returned by any search tool this turn; the citation guard unwrapped them.
	// The stripped URLs go on a content-gated event for triage.
	if n := len(resp.StrippedURLs); n > 0 {
		span.SetAttributes(
			attribute.Bool("bot.anomaly.fabricated_url", true),
			attribute.Int("bot.anomaly.fabricated_url_count", n),
		)
		obs.RecordContent(span, "bot.anomaly.fabricated_urls", strings.Join(resp.StrippedURLs, " "))
	}
}

// errorReplyText picks the user-facing message for a laplace failure. A
// provider safety block (Gemini PROHIBITED_CONTENT, etc.) gets a specific
// message so the user knows the content filter — not a transient outage —
// rejected the request, and can act (drop the reference photo / rephrase)
// instead of blindly retrying into the same permanent block. It also tags the
// root span so the turn is queryable in Tempo via
// {span.bot.anomaly.safety_blocked=true}. Any other error falls back to the
// generic api_error.
func (b *Bot) errorReplyText(span trace.Span, err error) string {
	if llm.IsSafetyBlock(err) {
		span.SetAttributes(attribute.Bool("bot.anomaly.safety_blocked", true))
		return b.translator.Get(b.cfg.Bot.Language, "bot.safety_blocked")
	}
	return b.translator.Get(b.cfg.Bot.Language, "bot.api_error")
}

// prepareErrorText picks the user-facing message for a prepareUserMessage
// failure: specific texts for file validation errors (unsupported format,
// too large), the generic api_error otherwise.
func (b *Bot) prepareErrorText(err error) string {
	var unsupported *files.UnsupportedFormatError
	var tooLarge *files.FileTooLargeError
	switch {
	case errors.As(err, &unsupported):
		msg := b.translator.Get(b.cfg.Bot.Language, "bot.file_unsupported_format")
		ext := filepath.Ext(unsupported.FileName)
		if ext == "" && unsupported.MimeType != "" {
			ext = "(" + unsupported.MimeType + ")"
		}
		return strings.ReplaceAll(msg, "{ext}", ext)
	case errors.As(err, &tooLarge):
		msg := b.translator.Get(b.cfg.Bot.Language, "bot.file_too_large")
		sizeMB := float64(tooLarge.Size) / (1024 * 1024)
		return strings.ReplaceAll(msg, "{size}", fmt.Sprintf("%.1f", sizeMB))
	default:
		return b.translator.Get(b.cfg.Bot.Language, "bot.api_error")
	}
}

// linkArtifactsToLastMessage back-fills the just-stored user message's id on
// the artifacts saved for this group. Message ID is auto-increment, so we
// fetch the most recent history row for this user. Best-effort.
func (b *Bot) linkArtifactsToLastMessage(userID storage.ScopeID, processedFiles []*files.ProcessedFile, logger *slog.Logger) {
	if len(processedFiles) == 0 || b.artifactRepo == nil {
		return
	}
	lastMsg, err := b.msgRepo.GetRecentHistory(userID, 1)
	if err != nil || len(lastMsg) == 0 {
		return
	}
	messageID := lastMsg[0].ID
	for _, f := range processedFiles {
		if f.ArtifactID == nil {
			continue
		}
		if err := b.artifactRepo.UpdateMessageID(userID, *f.ArtifactID, messageID); err != nil {
			logger.Warn("failed to link artifact to message",
				"user_id", userID,
				"artifact_id", *f.ArtifactID,
				"message_id", messageID,
				"error", err,
			)
		}
	}
}

// countToolCalls walks the turn's assistant messages and returns the total
// tool-call count plus the distinct tool names in first-use order — the
// execution tracker doesn't expose this directly, but resp.Messages does.
func countToolCalls(messages []llm.Message) (int, []string) {
	total := 0
	var names []string
	seen := map[string]bool{}
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			total++
			if !seen[tc.Function.Name] {
				seen[tc.Function.Name] = true
				names = append(names, tc.Function.Name)
			}
		}
	}
	return total, names
}

// fileProcessingError wraps a file processing error for identification.
type fileProcessingError struct {
	err          error
	messageIndex int
}

func (e *fileProcessingError) Error() string {
	return e.err.Error()
}

func (e *fileProcessingError) Unwrap() error {
	return e.err
}

func (b *Bot) processMessageGroup(ctx context.Context, group *MessageGroup) {
	if len(group.Messages) == 0 {
		return
	}
	b.ensureTransport()

	lastMsg := group.Messages[len(group.Messages)-1]
	userID := group.UserID // resolved scope id (storage partition key)

	// Track processing time and success for metrics
	startTime := time.Now()
	success := false
	defer func() {
		duration := time.Since(startTime).Seconds()
		RecordMessageProcessing(userID, duration, success)
	}()

	// Root span for the whole turn. Child spans (RAG, reranker, LLM, tools)
	// will attach here in later iterations; this one establishes the trace
	// id and the user.id/message.count attributes every downstream query
	// in Tempo will filter by. Declared AFTER the metrics defer so it fires
	// first (LIFO) and can set error status before span.End().
	totalChars := 0
	for _, m := range group.Messages {
		totalChars += len(m.Text)
	}
	ctx, span := otel.Tracer("github.com/runixer/laplaced/internal/bot").Start(
		ctx, "bot.processMessageGroup",
		trace.WithAttributes(
			attribute.String("user.id", string(userID)),
			attribute.Int("message.count", len(group.Messages)),
			attribute.Int("message.total_chars", totalChars),
		),
	)
	// Captured by the deferred closure for end-of-turn aggregations. These
	// roll up into bot.* span attrs so a single TraceQL query like
	// {span.bot.had_errors=true} or {span.bot.total_cost_usd > 0.1} surfaces
	// problem turns without aggregating across child spans.
	var (
		botLLMCalls   int
		botToolCalls  int
		botCostUSD    float64
		botToolsUsed  []string // distinct tool names invoked this turn
		botHadErrors  bool
		botErrorKinds []string
	)
	defer func() {
		span.SetAttributes(
			attribute.Int("bot.llm_calls_count", botLLMCalls),
			attribute.Int("bot.tool_calls_count", botToolCalls),
			attribute.Float64("bot.total_cost_usd", botCostUSD),
			attribute.Bool("bot.had_errors", botHadErrors),
		)
		if len(botToolsUsed) > 0 {
			span.SetAttributes(attribute.StringSlice("bot.tools_used", botToolsUsed))
		}
		if len(botErrorKinds) > 0 {
			span.SetAttributes(attribute.StringSlice("bot.error_kinds", botErrorKinds))
		}
		if !success {
			span.SetStatus(codes.Error, "message processing failed")
		}
		span.End()
	}()

	// Bind trace_id/span_id onto the per-turn logger so every downstream
	// slog line carries the same correlation ids — Grafana's Tempo
	// derived-fields config then renders one-click "open in Tempo" buttons
	// on Loki log lines, removing the manual ts → user_id → trace_id
	// detective work today's investigations need.
	logger := obs.LoggerWithSpan(ctx, b.logger).With(
		"user_id", userID,
		"sender", lastMsg.SenderDisplay,
		"transport", b.transport.Kind(),
	)

	logger.Info("processing message group", "message_count", len(group.Messages))

	// v0.5.1: Extract people from forwarded messages (Telegram-only data)
	b.extractForwardedPeople(ctx, userID, group.Messages, logger)

	// Phase 6: in a channel, the mentioning sender is a participant — record them
	// in the channel's People graph. No-op for DMs.
	b.upsertChannelParticipant(userID, lastMsg)

	convID := lastMsg.ConversationID
	threadRoot := lastMsg.ThreadRoot

	// Use non-cancellable context for all operations that should complete during shutdown.
	// This ensures RAG retrieval, LLM generation, and response sending all complete
	// even when graceful shutdown is triggered.
	shutdownSafeCtx := context.WithoutCancel(ctx)

	typingCtx, cancelTyping := context.WithCancel(shutdownSafeCtx)
	defer cancelTyping()
	go b.sendTypingActionLoop(typingCtx, convID)

	historyContent, rawQuery, currentUserMessageContent, allProcessedFiles, err := b.prepareUserMessage(shutdownSafeCtx, group, logger)
	if rawQuery != "" {
		queryPreview, queryHash := obs.TextPreview(rawQuery, obs.DefaultPreviewLen)
		span.SetAttributes(
			attribute.String("bot.user_query_preview", queryPreview),
			attribute.String("bot.user_query_hash", queryHash),
		)
	}
	if err != nil {
		b.sendRendered(shutdownSafeCtx, convID, threadRoot, "", b.prepareErrorText(err), logger)
		return
	}

	if len(currentUserMessageContent) == 0 {
		logger.Warn("message group was empty after processing")
		return
	}

	// Record live attachments on the root span so triage can answer
	// "what did the user just send" without parsing the llm.request
	// body. Each entry is {filename, mime, sha256, size, source}; the sha256
	// matches artifacts.content_hash, so a snapshot replay can reconstruct
	// the original FilePart bytes from the artifact storage.
	hasCurrentMedia := false
	for _, p := range currentUserMessageContent {
		if _, ok := p.(llm.FilePart); ok {
			hasCurrentMedia = true
			break
		}
	}
	if hasCurrentMedia {
		span.SetAttributes(attribute.Bool("bot.has_current_media", true))
	}
	if obs.ContentEnabled() {
		if body := agent.FormatMediaParts(currentUserMessageContent, "current_message"); body != "" {
			obs.RecordContent(span, "bot.current_media", body)
		}
	}

	// Load SharedContext once for all agents in this request
	if b.contextService != nil {
		shared := b.contextService.Load(shutdownSafeCtx, userID)
		shutdownSafeCtx = agent.WithContext(shutdownSafeCtx, shared)
	}

	// Emoji reaction, decided by the reactor agent — fire-and-forget in
	// parallel with the main response flow.
	b.maybeReact(shutdownSafeCtx, userID, convID, lastMsg.MessageID, rawQuery, currentUserMessageContent, logger)

	// In a channel scope, attribute the message to its sender and record its
	// thread so background extraction can tell participants apart and the bot's
	// reply joins the same thread (enabling later thread-reply gating). DMs leave
	// these NULL, keeping the single-user history byte-identical.
	var chanAuthor, chanThreadRoot *string
	if !lastMsg.IsDirect {
		chanAuthor = strPtrOrNil(lastMsg.SenderDisplay)
		chanThreadRoot = strPtrOrNil(threadRoot)
	}
	if err := b.msgRepo.AddMessageToHistory(userID, storage.Message{
		Role:           "user",
		Content:        historyContent,
		Author:         chanAuthor,
		MessageID:      strPtrOrNil(lastMsg.MessageID),
		ConversationID: strPtrOrNil(convID),
		ThreadRoot:     chanThreadRoot,
	}); err != nil {
		logger.Error("failed to add message to history", "error", err)
		return
	}

	b.linkArtifactsToLastMessage(userID, allProcessedFiles, logger)

	// Execute via Laplace agent
	if b.laplaceAgent == nil {
		logger.Error("laplace agent not configured")
		b.sendRendered(shutdownSafeCtx, convID, threadRoot, "", b.translator.Get(b.cfg.Bot.Language, "bot.generic_error"), logger)
		return
	}

	// Create tool handler
	toolHandler := b.newBotToolHandler(userID, logger)

	// Delivery path for this turn: opens the streaming sink (owning the
	// in-flight reply bubble) when the transport supports streaming, buffered
	// sendRendered otherwise. Error/empty paths route through it too, so the
	// placeholder never gets orphaned. Also accumulates Telegram timing
	// counters — deferred flush captures early returns.
	path := b.newResponsePath(shutdownSafeCtx, userID, convID, threadRoot, lastMsg.MessageID, logger)
	defer path.recordTelegramMetrics()

	// Build request
	req := &laplace.Request{
		UserID:              userID,
		HistoryContent:      historyContent,
		RawQuery:            rawQuery,
		CurrentMessageParts: currentUserMessageContent,
		ChatID:              path.tgChatID,
		MessageThreadID:     path.tgThreadID,
		ReplyToMsgID:        path.tgReplyID,
		UseStreaming:        path.sink != nil,
		OnIntermediateMessage: func(text string) {
			path.sendIntermediate(shutdownSafeCtx, text)
		},
		OnToolStart: func(toolName, arguments string) {
			if path.sink != nil {
				path.sink.Status(toolName, arguments)
			}
			_ = b.transport.SendTyping(shutdownSafeCtx, convID)
		},
	}
	if path.sink != nil {
		req.OnContentDelta = path.sink.Delta
		req.OnRAGEnriched = path.sink.RAG
	}

	// Execute
	resp, err := b.laplaceAgent.Execute(shutdownSafeCtx, req, toolHandler)
	if err != nil {
		// Fatal error (not a partial execution failure). Generated images that
		// tool iterations already produced (and paid for) are still delivered.
		logger.Error("laplace execution fatal error", "error", err)
		botHadErrors = true
		botErrorKinds = append(botErrorKinds, "laplace_fatal")
		if b.deliverGeneratedOnError(shutdownSafeCtx, path, userID, convID, threadRoot, lastMsg.MessageID, resp, logger) {
			return
		}
		path.sendError(shutdownSafeCtx, b.errorReplyText(span, err))
		return
	}

	// Surface anomaly signals on the root span BEFORE branching on
	// resp.Error — empty_response in particular fires on the retries-
	// exhausted path which is also the error path. Sanitized / echo do
	// require non-empty content; their guards handle the no-op cases.
	recordLaplaceAnomalies(span, resp, lastMsg.Text, hasCurrentMedia)

	// Check for partial execution error (e.g., max retries reached)
	if resp.Error != nil {
		logger.Error("laplace execution failed", "error", resp.Error, "total_turns", resp.TotalTurns)
		botHadErrors = true
		botErrorKinds = append(botErrorKinds, "laplace_partial")
		if !b.deliverGeneratedOnError(shutdownSafeCtx, path, userID, convID, threadRoot, lastMsg.MessageID, resp, logger) {
			path.sendError(shutdownSafeCtx, b.errorReplyText(span, resp.Error))
		}

		// Log partial execution for debugging. Cost stays provider-reported-
		// or-zero: a partial turn may lack token counts, so the tiered
		// estimate would be garbage.
		var cost float64
		if resp.TotalCost != nil {
			cost = *resp.TotalCost
		}
		b.laplaceAgent.LogExecution(shutdownSafeCtx, userID, resp, cost)
		return
	}

	// Record metrics
	RecordMessageLLM(userID, resp.LLMDuration.Seconds(), resp.TotalTurns)
	if resp.ToolDuration > 0 {
		RecordMessageTools(userID, resp.ToolDuration.Seconds(), resp.TotalTurns)
	}

	cost := b.turnCost(resp, logger)

	// Roll up turn aggregations onto the root span.
	botLLMCalls = resp.TotalTurns
	botCostUSD = cost
	botToolCalls, botToolsUsed = countToolCalls(resp.Messages)

	// Record stats and the agent-log entry
	b.recordTurnStats(shutdownSafeCtx, userID, resp, cost, logger)

	// Record context tokens by source
	RecordContextTokens(resp.PromptTokens)

	logger.Info("usage stats recorded",
		"prompt_tokens", resp.PromptTokens,
		"completion_tokens", resp.CompletionTokens,
		"total_tokens", resp.PromptTokens+resp.CompletionTokens,
		"cost_usd", cost,
	)

	// Record TTFT (stream mode only — non-streaming path leaves it zero).
	if resp.FirstContentDelay > 0 {
		RecordMessageLLMFirstToken(userID, resp.FirstContentDelay.Seconds())
	}

	// Always-on, short preview of what the bot is about to send. Unlike
	// obs.RecordContent (which records the full body as a span event under
	// the content toggle), this is a single attribute that lives in the
	// Tempo index — usable directly in TraceQL like
	// `{span.bot.reply_preview =~ ".*Isotonic.*"}` to locate a turn by its
	// reply text without ssh/docker logs. The paired hash lets you join the
	// same reply across spans even after truncation.
	if resp.Content != "" {
		replyPreview, replyHash := obs.TextPreview(resp.Content, obs.DefaultPreviewLen)
		span.SetAttributes(
			attribute.String("bot.reply_preview", replyPreview),
			attribute.String("bot.reply_hash", replyHash),
		)
	}

	// Branch: if the turn produced generated images, route through the
	// media-aware reply path. Otherwise keep the text-only path.
	if len(resp.GeneratedArtifactIDs) > 0 {
		path.flushSinkBeforeMedia(shutdownSafeCtx, resp.Content)
		obs.RecordContent(span, "bot.reply_sent", resp.Content,
			attribute.Int64Slice("generated_artifact_ids", resp.GeneratedArtifactIDs))
		mediaDur, sentCount := b.sendResponseWithGeneratedImages(
			shutdownSafeCtx, userID, convID, threadRoot, lastMsg.MessageID,
			resp.Content, resp.GeneratedArtifactIDs, logger,
		)
		path.tgDuration += mediaDur
		path.tgCalls += sentCount
		success = true
		return
	}

	b.saveAssistantReply(userID, span, resp.Content, convID, chanThreadRoot, logger)

	// Deliver the final reply (streaming: bubble edit + overflow sends;
	// buffered: rendered chunks replying to the triggering message).
	path.sendFinal(shutdownSafeCtx, span, resp.Content)

	success = true
}

// streamFinalizeCallback returns a finalizeResponse adapter for the
// streaming sink. The sink doesn't know about ctx/chatID/threadID/logger,
// so we close over them here. Returned function is the second arg to
// streamSink.Finalize.
func (b *Bot) streamFinalizeCallback(ctx context.Context, chatID int64, threadID int, logger *slog.Logger) func(string) ([]telegram.SendMessageRequest, error) {
	return func(text string) ([]telegram.SendMessageRequest, error) {
		// replyToMsgID=0: only the placeholder (which we edit) had the reply
		// link; overflow chunks are plain follow-up sends.
		return b.finalizeResponse(ctx, chatID, threadID, 0, text, logger)
	}
}

func (b *Bot) prepareUserMessage(ctx context.Context, group *MessageGroup, logger *slog.Logger) (string, string, []interface{}, []*files.ProcessedFile, error) {
	// Build group text for artifact context: MessageGroup + recent session messages (v0.6.0)
	var groupTextBuilder strings.Builder

	// 1. Add recent session messages (if configured)
	recentCount := b.cfg.Agents.Extractor.RecentMessageCount
	if recentCount > 0 {
		// Get message IDs from current group to exclude them
		excludeIDs := make([]int64, len(group.Messages))
		for i, msg := range group.Messages {
			excludeIDs[i] = int64(atoiOrZero(msg.MessageID))
		}

		// Fetch recent session messages (excluding current group messages)
		recentMsgs, err := b.msgRepo.GetRecentSessionMessages(ctx, group.UserID, recentCount, excludeIDs)
		if err == nil && len(recentMsgs) > 0 {
			for _, msg := range recentMsgs {
				// Build content from stored messages (simplified, without full telegram.Message)
				content := fmt.Sprintf("[%s (saved)]: %s", msg.CreatedAt.Format("2006-01-02 15:04:05"), msg.Content)
				appendWithNewline(&groupTextBuilder, content)
			}
		}
		// Log error but don't fail - recent context is optional
		if err != nil {
			logger.Debug("failed to fetch recent session messages", "error", err)
		}
	}

	// 2. Add MessageGroup messages (current context)
	for _, msg := range group.Messages {
		if content := b.incomingContent(msg); content != "" {
			appendWithNewline(&groupTextBuilder, content)
		}
	}
	groupText := groupTextBuilder.String()

	// 1. Download all files in parallel across messages
	downloadedFiles := make([][]*files.ProcessedFile, len(group.Messages))

	g, gCtx := errgroup.WithContext(ctx)
	for i, msg := range group.Messages {
		i, msg := i, msg // capture for goroutine
		g.Go(func() error {
			result, err := b.fileProcessor.ProcessFiles(gCtx, msg.Files, group.UserID, groupText)
			if err != nil {
				// Check for file validation errors (unsupported format, too large)
				// These are user-facing errors, return them wrapped
				return &fileProcessingError{err: err, messageIndex: i}
			}
			downloadedFiles[i] = result
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		// Check if this is a file processing error (validation failure)
		var fileErr *fileProcessingError
		if errors.As(err, &fileErr) {
			// Return validation error for caller to handle
			return "", "", nil, nil, fileErr.err
		}
		// Other errors (like context cancellation) return as-is
		return "", "", nil, nil, err
	}

	// 2. Process messages sequentially (order preserved)
	var historyBuilder strings.Builder
	var rawQueryBuilder strings.Builder
	var llmParts []interface{}
	var allProcessedFiles []*files.ProcessedFile // Collect all files for artifact linking

	for i, msg := range group.Messages {
		// Text content (message text or caption)
		textContent := b.incomingContent(msg)
		if textContent != "" {
			appendWithNewline(&historyBuilder, textContent)
			llmParts = append(llmParts, llm.TextPart{Type: "text", Text: textContent})
		}

		// Process pre-downloaded files
		for _, f := range downloadedFiles[i] {
			// Collect for artifact linking
			allProcessedFiles = append(allProcessedFiles, f)

			// Add instruction before file if present (e.g., voice handling instructions)
			if f.Instruction != "" {
				llmParts = append(llmParts, llm.TextPart{
					Type: "text",
					Text: fmt.Sprintf("%s: %s", msg.Prefix, f.Instruction),
				})
			}

			// Add file parts to LLM message
			llmParts = append(llmParts, f.LLMParts...)

			// Handle history and RAG query based on file type
			switch f.FileType {
			case files.FileTypeVoice:
				// Voice: add marker to history and RAG query
				voiceMarker := b.translator.Get(b.cfg.Bot.Language, "bot.voice_message_marker")
				appendWithNewline(&historyBuilder, fmt.Sprintf("%s: %s", msg.Prefix, voiceMarker))
				appendWithNewline(&rawQueryBuilder, voiceMarker)

				logger.Info("processed voice message",
					"file_id", f.FileID,
					"size", f.Size,
					"duration_ms", f.Duration.Milliseconds(),
				)

			case files.FileTypeDocument:
				// Text document: save compact marker in history instead of full content
				// But include content in rawQuery for enricher/reranker to see current file
				if f.ArtifactID != nil {
					marker := fmt.Sprintf("📄 %s (artifact:%d)", f.FileName, *f.ArtifactID)
					appendWithNewline(&historyBuilder, marker)
				} else {
					// Fallback: no artifact (disabled or error)
					marker := fmt.Sprintf("📄 %s", f.FileName)
					appendWithNewline(&historyBuilder, marker)
				}
				// Extract text content from LLMParts for enricher/reranker
				// Text documents have TextPart with "filename:\n\ncontent" format
				for _, part := range f.LLMParts {
					if tp, ok := part.(llm.TextPart); ok {
						appendWithNewline(&rawQueryBuilder, tp.Text)
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
		appendWithNewline(&rawQueryBuilder, msg.Text)
	}

	return historyBuilder.String(), rawQueryBuilder.String(), llmParts, allProcessedFiles, nil
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

// splitByDelimiter splits text by ###SPLIT### delimiter, respecting protected
// blocks (code, tables). Delimiters inside them are ignored. Empty parts are
// filtered out.
func splitByDelimiter(text string) []string {
	const delimiter = "###SPLIT###"

	if !strings.Contains(text, delimiter) {
		return []string{text}
	}

	codeBlocks := telegram.FindProtectedBlocks(text)

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

// listItemRegex matches numbered list items like "1. ", "42. " at line start.
var listItemRegex = regexp.MustCompile(`(?m)^(\d+)\.\s`)

// fixListNumbering fixes numbered list continuity across message parts.
// When LLM splits a message mid-list, it sometimes restarts numbering from 1.
// This function detects and fixes such cases.
func fixListNumbering(parts []string) []string {
	if len(parts) < 2 {
		return parts
	}

	result := make([]string, len(parts))
	copy(result, parts)

	for i := 1; i < len(result); i++ {
		prevPart := result[i-1]
		currPart := result[i]

		// Find last number in previous part
		prevMatches := listItemRegex.FindAllStringSubmatch(prevPart, -1)
		if len(prevMatches) == 0 {
			continue
		}
		lastNum, _ := strconv.Atoi(prevMatches[len(prevMatches)-1][1])

		// Find first numbers in current part
		currMatches := listItemRegex.FindAllStringSubmatchIndex(currPart, 3)
		if len(currMatches) == 0 {
			continue
		}

		// Get first number
		firstNumStr := currPart[currMatches[0][2]:currMatches[0][3]]
		firstNum, _ := strconv.Atoi(firstNumStr)

		// Check if numbering is broken (starts with 1 when it shouldn't)
		expectedNum := lastNum + 1
		if firstNum == 1 && expectedNum > 1 {
			// Check if this looks like LLM "self-correction" bug or intentional new list
			// Bug pattern: 1, 7, 8 (jumped from 1 to 7, skipping 2-6)
			// Intentional: 1, 2, 3 (sequential from 1)
			if len(currMatches) >= 2 {
				secondNumStr := currPart[currMatches[1][2]:currMatches[1][3]]
				secondNum, _ := strconv.Atoi(secondNumStr)
				// If second item follows first sequentially (1->2), it's intentional new list
				if secondNum == firstNum+1 {
					continue
				}
			}

			// Fix the first number
			result[i] = currPart[:currMatches[0][2]] +
				strconv.Itoa(expectedNum) +
				currPart[currMatches[0][3]:]
		}
	}

	return result
}

// sendRendered renders canonical markdown to wire-format chunks and sends each
// through the active transport, replying to replyTo on the first chunk and
// keeping every chunk in threadRoot. On a send failure it emits a single
// generic-error message and stops. Returns the count of chunks sent and the
// transport-native id of the first chunk (the message a user would react to;
// "" if nothing was sent).
func (b *Bot) sendRendered(ctx context.Context, convID, threadRoot, replyTo, text string, logger *slog.Logger) (int, string) {
	chunks, err := b.renderer.Render(ctx, text)
	if err != nil {
		logger.Error("failed to render response", "error", err)
	}
	sent := 0
	firstMsgID := ""
	for i, chunk := range chunks {
		if strings.TrimSpace(chunk) == "" {
			logger.Warn("skipping empty response chunk", "chunk_index", i)
			continue
		}
		resp := OutgoingResponse{ConversationID: convID, Text: chunk, ThreadRoot: threadRoot}
		if i == 0 {
			resp.ReplyTo = replyTo
		}
		msgID, serr := b.transport.SendText(ctx, resp)
		if serr != nil {
			logger.Error("failed to send message", "error", serr, "chunk_index", i)
			b.sendGenericError(ctx, convID, threadRoot, logger)
			return sent, firstMsgID
		}
		if sent == 0 {
			firstMsgID = msgID
		}
		sent++
	}
	return sent, firstMsgID
}

// linkReplyTrace back-fills the transport-native message id on the just-stored
// assistant reply, making it resolvable from an inbound reaction (which then
// recovers the reply's trace_id, if tracing was on). Best-effort: a failure only
// means that one reply can't be flagged, never blocks it.
func (b *Bot) linkReplyTrace(userID storage.ScopeID, transportMsgID string, logger *slog.Logger) {
	if transportMsgID == "" {
		return
	}
	if err := b.msgRepo.SetReplyTransportID(userID, transportMsgID); err != nil {
		logger.Warn("failed to link reply to transport message id", "error", err)
	}
}

// sendGenericError sends the localized generic-error message, best-effort.
func (b *Bot) sendGenericError(ctx context.Context, convID, threadRoot string, logger *slog.Logger) {
	chunks, _ := b.renderer.Render(ctx, b.translator.Get(b.cfg.Bot.Language, "bot.generic_error"))
	for _, chunk := range chunks {
		if _, err := b.transport.SendText(ctx, OutgoingResponse{ConversationID: convID, Text: chunk, ThreadRoot: threadRoot}); err != nil {
			logger.Error("failed to send generic error message", "error", err)
			return
		}
	}
}

// finalizeResponse renders canonical markdown for the Telegram streaming path
// (placeholder edit + overflow sends). It delegates to the Telegram renderer,
// which owns chunking, list-numbering fixes, HTML conversion and the UTF-16
// budget enforcement, and only wraps the chunks into send requests.
func (b *Bot) finalizeResponse(ctx context.Context, chatID int64, messageThreadID int, replyToMsgID int, responseText string, logger *slog.Logger) ([]telegram.SendMessageRequest, error) {
	chunks, err := NewTelegramRenderer(logger).Render(ctx, responseText)
	if err != nil {
		return nil, err
	}

	var responses []telegram.SendMessageRequest
	for i, chunk := range chunks {
		newMsg := telegram.SendMessageRequest{
			ChatID:          chatID,
			MessageThreadID: intPtrOrNil(messageThreadID),
			Text:            chunk,
			ParseMode:       "HTML",
		}
		if i == 0 && replyToMsgID != 0 {
			newMsg.ReplyToMessageID = replyToMsgID
		}
		responses = append(responses, newMsg)
	}

	return responses, nil
}

// extractForwardedPeople extracts people information from forwarded messages.
//
// Complexity: MEDIUM (CC=33) - nested conditions, person lookup, deduplication
// Dependencies: peopleRepo
// Side effects: Creates/updates people in DB
// Error handling: Continues on individual errors, logs stats
//
// When a message is forwarded from a user, we can capture their telegram_id and username
// for the People graph (v0.5.1).
func (b *Bot) extractForwardedPeople(ctx context.Context, userID storage.ScopeID, messages []IncomingMessage, logger *slog.Logger) {
	if b.peopleRepo == nil {
		return
	}

	// Track stats for aggregated logging
	var created, updated, errors int
	var createdNames, updatedNames []string

	for _, msg := range messages {
		fwd := msg.Forward
		// Only process messages forwarded from users (not channels/hidden).
		if fwd == nil || !fwd.IsUser {
			continue
		}
		if fwd.IsBot {
			continue // Skip bots
		}
		senderID, err := strconv.ParseInt(fwd.SenderID, 10, 64)
		if err != nil {
			continue
		}
		if storage.PassthroughScopeID(transportTelegram, fwd.SenderID) == userID {
			continue // Skip self (user forwarding their own messages)
		}

		// Build display name from first/last name
		displayName := fwd.FirstName
		if fwd.LastName != "" {
			displayName = displayName + " " + fwd.LastName
		}
		if displayName == "" {
			if fwd.Username != "" {
				displayName = fwd.Username
			} else {
				continue // No name to use
			}
		}

		// Check if we already have this person by telegram_id
		existingPerson, err := b.peopleRepo.FindPersonByTelegramID(userID, senderID)
		if err != nil {
			errors++
			continue
		}

		if existingPerson != nil {
			// Update last_seen and mention_count
			existingPerson.LastSeen = time.Now()
			existingPerson.MentionCount++
			if fwd.Username != "" && (existingPerson.Username == nil || *existingPerson.Username != fwd.Username) {
				existingPerson.Username = &fwd.Username
			}
			if err := b.peopleRepo.UpdatePerson(*existingPerson); err != nil {
				errors++
			} else {
				updated++
				// Track unique names
				found := false
				for _, n := range updatedNames {
					if n == existingPerson.DisplayName {
						found = true
						break
					}
				}
				if !found {
					updatedNames = append(updatedNames, existingPerson.DisplayName)
				}
			}
		} else {
			// Check if person exists by username or name
			var foundPerson *storage.Person
			if fwd.Username != "" {
				foundPerson, _ = b.peopleRepo.FindPersonByUsername(userID, fwd.Username)
			}
			if foundPerson == nil {
				foundPerson, _ = b.peopleRepo.FindPersonByName(userID, displayName)
			}

			if foundPerson != nil {
				// Update existing person with telegram_id
				foundPerson.TelegramID = &senderID
				foundPerson.LastSeen = time.Now()
				foundPerson.MentionCount++
				if fwd.Username != "" && (foundPerson.Username == nil || *foundPerson.Username != fwd.Username) {
					foundPerson.Username = &fwd.Username
				}
				if err := b.peopleRepo.UpdatePerson(*foundPerson); err != nil {
					errors++
				} else {
					updated++
					found := false
					for _, n := range updatedNames {
						if n == foundPerson.DisplayName {
							found = true
							break
						}
					}
					if !found {
						updatedNames = append(updatedNames, foundPerson.DisplayName)
					}
				}
			} else {
				// Create new person from forwarded message
				now := time.Now()
				newPerson := storage.Person{
					UserID:       userID,
					DisplayName:  displayName,
					TelegramID:   &senderID,
					Circle:       storage.CircleOther,
					FirstSeen:    now,
					LastSeen:     now,
					MentionCount: 1,
				}
				if fwd.Username != "" {
					newPerson.Username = &fwd.Username
				}

				if _, err := b.peopleRepo.AddPerson(newPerson); err != nil {
					errors++
				} else {
					created++
					createdNames = append(createdNames, displayName)
				}
			}
		}
	}

	// Log aggregated summary
	if created > 0 || updated > 0 {
		logger.Info("extracted people from forwarded messages",
			"created", created,
			"updated", updated,
			"created_names", createdNames,
			"updated_names", updatedNames,
			"errors", errors)
	}
}

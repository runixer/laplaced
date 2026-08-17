package bot

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
)

// responsePath owns delivery of one turn's reply: through the persistent
// legacy edit sink, through an ephemeral rich draft, or through the buffered
// send pipeline. Centralizing the branch keeps each terminal transition
// single-owner instead of relying on every call site to remember it.
//
// It also accumulates the per-turn Telegram timing/call counters that were
// previously threaded through processMessageGroup by hand; flush them with
// recordTelegramMetrics once the turn ends.
type responsePath struct {
	bot    *Bot
	logger *slog.Logger
	userID storage.ScopeID

	convID     string
	threadRoot string
	replyTo    string // transport-native id of the triggering message
	richMode   string // off | shadow | send, resolved once from the native sender

	// Telegram-only routing ints for the streaming sink and the laplace
	// request, parsed from the neutral envelope. They are 0 for non-Telegram
	// transports, which never stream.
	tgChatID   int64
	tgThreadID int
	tgReplyID  int

	// sink is non-nil when streaming is enabled and the transport supports
	// it; it owns the in-flight reply bubble (placeholder → tool status →
	// progressive content → final HTML).
	sink *streamSink

	// richDraft is an ephemeral private-chat preview. It never represents
	// confirmed delivery: sendFinal still performs the ordinary buffered rich
	// send and only that persistent result may be stored in history.
	richDraft       *richDraftSink
	richDraftClosed bool

	tgDuration time.Duration
	tgCalls    int

	// Set only after Telegram confirmed a persistent message id. History is
	// saved first, then this id is linked to that exact row.
	deliveredMessageID  string // legacy streaming compatibility / primary id
	deliveredMessageIDs []string
	deliveryID          int64
}

// newResponsePath builds the delivery path for one turn, opening the
// streaming sink (and sending its placeholder) when the transport supports
// streaming.
func (b *Bot) newResponsePath(ctx context.Context, userID storage.ScopeID, nativeSenderID string, richContextEligible bool, convID, threadRoot, replyTo string, logger *slog.Logger) *responsePath {
	p := &responsePath{
		bot:        b,
		logger:     logger,
		userID:     userID,
		convID:     convID,
		threadRoot: threadRoot,
		replyTo:    replyTo,
		richMode:   config.TelegramRichMessagesOff,
		tgThreadID: atoiOrZero(threadRoot),
		tgReplyID:  atoiOrZero(replyTo),
	}
	p.tgChatID, _ = strconv.ParseInt(convID, 10, 64)
	if b.transport.Kind() == transportTelegram && richContextEligible {
		p.richMode = b.cfg.Telegram.RichMessages.ModeForNativeUser(nativeSenderID)
	}

	caps := b.transport.Capabilities()
	if caps.SupportsStreaming {
		if p.richMode == config.TelegramRichMessagesSend {
			// Rich drafts are an independent, explicitly opted-in preview path.
			// Never open a legacy placeholder for a rich final: the two formats
			// have different terminal ownership and fallback semantics.
			if !b.cfg.Telegram.RichMessages.DraftStreamingEnabled {
				return p
			}
			p.richDraft = newRichDraftSink(
				ctx, b.api, b.translator, b.cfg.Bot.Language,
				b.cfg.Bot.Streaming, p.tgChatID, p.tgThreadID, p.tgReplyID,
				logger,
			)
			if !p.richDraft.Active() {
				// An unavailable draft capability only disables the preview. The
				// final response remains the existing buffered Rich Message send.
				p.closeRichDraft()
			}
		} else if b.cfg.Bot.Streaming.Enabled {
			p.sink = newStreamSink(
				ctx, b.api, b.translator, b.cfg.Bot.Language,
				b.cfg.Bot.Streaming, p.tgChatID, p.tgThreadID, p.tgReplyID,
				logger,
			)
			// Account for the placeholder send in Telegram metrics.
			p.tgCalls++
			// A failed or malformed placeholder is not a streaming path.
			if p.sink.MessageID() == 0 {
				p.sink = nil
			}
		}
	}
	return p
}

func (p *responsePath) usesStreaming() bool {
	return p.sink != nil || p.usesRichDraft()
}

func (p *responsePath) usesRichDraft() bool {
	return p.richDraft != nil && p.richDraft.Active()
}

func (p *responsePath) streamStatus(toolName, arguments string) {
	if p.sink != nil {
		p.sink.Status(toolName, arguments)
	}
	if p.richDraft != nil {
		p.richDraft.Status(toolName, arguments)
	}
}

func (p *responsePath) streamRAG(enrichedQuery string) {
	if p.sink != nil {
		p.sink.RAG(enrichedQuery)
	}
	if p.richDraft != nil {
		p.richDraft.RAG(enrichedQuery)
	}
}

func (p *responsePath) streamDelta(text string) {
	if p.sink != nil {
		p.sink.Delta(text)
	}
	if p.richDraft != nil {
		p.richDraft.Delta(text)
	}
}

func (p *responsePath) closeRichDraft() {
	if p.richDraft == nil || p.richDraftClosed {
		return
	}
	p.accountClosedRichDraft(p.richDraft.Close())
}

// finalizeRichDraft closes the callback/heartbeat lifecycle first, then lets a
// buffered content tail use one bounded preview-only catch-up attempt. Its
// result never changes whether the persistent final is attempted.
func (p *responsePath) finalizeRichDraft() {
	if p.richDraft == nil || p.richDraftClosed {
		return
	}
	p.accountClosedRichDraft(p.richDraft.FinalizePreview())
}

func (p *responsePath) accountClosedRichDraft(stats richDraftStats) {
	p.richDraftClosed = true
	p.tgCalls += stats.updates
	p.tgDuration += stats.duration
	RecordMessageTelegramDraftCount(p.userID, stats.updates)
	RecordMessageTelegramRichDraftContentSnapshotCount(stats.contentSnapshots)
	if stats.overflow {
		IncMessageTelegramRichDraftOverflow()
	}
	IncMessageTelegramRichDraftTerminalCatchup(stats.terminalCatchup)
}

// recordTelegramMetrics flushes the accumulated Telegram counters. Deferred
// by the caller so early returns (error paths) are captured too.
func (p *responsePath) recordTelegramMetrics() {
	// Safety net for an unexpected early return: stop the heartbeat and account
	// for draft calls even when no terminal delivery branch was reached.
	p.closeRichDraft()
	if p.tgCalls > 0 {
		RecordMessageTelegram(p.userID, p.tgDuration.Seconds(), p.tgCalls)
	}
}

// finalizeCb builds the render callback streamSink.Finalize needs.
func (p *responsePath) finalizeCb(ctx context.Context) func(string) ([]telegram.SendMessageRequest, error) {
	return p.bot.streamFinalizeCallback(ctx, p.tgChatID, p.tgThreadID, p.logger)
}

// sendIntermediate delivers a mid-turn message emitted from the tool loop.
func (p *responsePath) sendIntermediate(ctx context.Context, text string) {
	start := time.Now()
	sent, _ := p.bot.sendRendered(ctx, p.convID, p.threadRoot, "", text, p.logger)
	p.tgDuration += time.Since(start)
	p.tgCalls += sent
}

// sendError delivers errText, routing through the sink when one is open so
// the placeholder never gets orphaned.
func (p *responsePath) sendError(ctx context.Context, errText string) {
	p.closeRichDraft()
	start := time.Now()
	if p.sink != nil {
		_, _, err := p.sink.Finalize(
			finalizeArgs{UserID: p.userID, HadError: true, ErrorText: errText},
			p.finalizeCb(ctx),
		)
		if err != nil {
			// The edit may have reached Telegram. Do not fall back to a fresh
			// send: that could duplicate the terminal error bubble.
			p.logger.Warn("streaming error finalization was not confirmed", "error", err)
		}
	} else {
		n, _ := p.bot.sendRendered(ctx, p.convID, p.threadRoot, "", errText, p.logger)
		p.tgCalls += n
	}
	p.tgDuration += time.Since(start)
}

// flushSinkBeforeMedia hands terminal ownership to the media-aware reply path.
// A Rich Message preview is stopped before the one-shot persistent rich-media
// upload, preventing a heartbeat from reviving the draft after the final send.
// The legacy edit sink, when present, still finalizes its established text
// bubble before the separate media delivery. The buffered path is otherwise a
// no-op.
func (p *responsePath) flushSinkBeforeMedia(ctx context.Context, content string) {
	p.finalizeRichDraft()
	if p.sink == nil {
		return
	}
	_, _, err := p.sink.Finalize(finalizeArgs{UserID: p.userID, FullText: content}, p.finalizeCb(ctx))
	if err != nil {
		// Media delivery is a separate side effect; never resend the text after
		// an ambiguous terminal edit. The media path keeps its existing policy.
		p.logger.Warn("streaming pre-media finalization was not confirmed", "error", err)
	}
	RecordMessageTelegramEditCount(p.userID, p.sink.editCount)
}

// sendFinal delivers the turn's final reply: the sink edits its bubble in
// place and returns any overflow chunks to send as follow-ups; the buffered
// path renders and sends fresh messages, replying to the triggering message
// on the first chunk. Both variants link the message the user would react to
// back to the stored reply and record bot.reply_sent on the root span.
func (p *responsePath) sendFinal(ctx context.Context, span trace.Span, content string) bool {
	// Finalizing a Rich Message draft is not delivery. Its bounded catch-up is
	// preview-only; the buffered branch below still performs exactly one
	// persistent final attempt and owns history.
	p.finalizeRichDraft()
	p.shadowRichRender(ctx, span, content)
	if p.sink != nil {
		extra, edits, finalErr := p.sink.Finalize(
			finalizeArgs{UserID: p.userID, FullText: content},
			p.finalizeCb(ctx),
		)
		RecordMessageTelegramEditCount(p.userID, edits)
		if finalErr != nil {
			// A network/5xx/malformed response has an unknown outcome: the edit
			// may already be visible. Never send overflow or a buffered fallback,
			// and never let sendFinalAndPersist record this reply as delivered.
			span.RecordError(finalErr)
			span.SetStatus(codes.Error, "streaming final edit was not confirmed")
			span.SetAttributes(
				attribute.Bool("bot.reply_failed", true),
				attribute.Bool("bot.streaming.final_edit_unconfirmed", true),
			)
			return false
		}
		// Link the streamed bubble (what the user reacts to) to the stored reply.
		if mid := p.sink.MessageID(); mid != 0 {
			p.deliveredMessageID = strconv.Itoa(mid)
		}
		// chunks=1+len(extra) — one bubble edit plus any follow-up messages.
		if len(extra) > 0 {
			start := time.Now()
			allSent, attempts := p.bot.sendResponses(ctx, p.tgChatID, extra, p.logger)
			p.tgDuration += time.Since(start)
			p.tgCalls += attempts
			if !allSent {
				span.SetAttributes(attribute.Bool("bot.reply_failed", true))
				return false
			}
		}
		obs.RecordContent(span, "bot.reply_sent", content,
			attribute.Int("chunks", 1+len(extra)))
		return true
	}

	start := time.Now()
	var result richDeliveryResult
	if p.effectiveRichMode() == config.TelegramRichMessagesSend {
		result = p.bot.sendRichRendered(ctx, p.convID, p.threadRoot, p.replyTo, content, p.logger, p.ledgerContext()...)
		recordRichFinalDelivery(richFinalMetric{
			contentKind:    richMetricContentText,
			path:           result.metricPath,
			outcome:        result.outcome,
			fallbackReason: result.fallbackReason,
			err:            result.err,
		})
	} else {
		result = p.bot.sendRenderedDelivery(ctx, p.convID, p.threadRoot, p.replyTo, content, p.logger)
	}
	p.tgDuration += time.Since(start)
	p.tgCalls += result.attempts
	if result.outcome != richDeliveryConfirmed {
		// A purely local render/preflight rejection made no persistent request.
		// It is therefore safe to send a fixed bounded error, while still not
		// storing the rejected model body as a delivered assistant reply.
		if result.outcome == richDeliveryRejected && result.attempts == 0 {
			p.tgCalls += p.bot.sendGenericError(ctx, p.convID, p.threadRoot, p.logger)
			span.SetAttributes(attribute.Bool("bot.reply_local_rejection_notified", true))
		}
		if result.err != nil {
			span.RecordError(result.err)
		}
		span.SetStatus(codes.Error, "telegram delivery "+string(result.outcome))
		span.SetAttributes(
			attribute.Bool("bot.reply_failed", true),
			attribute.Int("bot.reply_confirmed_chunks", result.sent),
		)
		return false
	}
	if result.sent == 0 {
		span.SetAttributes(attribute.Bool("bot.reply_failed", true))
		return false
	}
	p.deliveredMessageID = result.firstMsgID
	p.deliveredMessageIDs = append(p.deliveredMessageIDs[:0], result.confirmedIDs...)
	p.deliveryID = result.deliveryID
	obs.RecordContent(span, "bot.reply_sent", content, attribute.Int("chunks", result.sent))
	return true
}

func (p *responsePath) effectiveRichMode() string {
	if p.richMode != "" {
		return p.richMode
	}
	return config.TelegramRichMessagesOff
}

func (p *responsePath) ledgerContext() []deliveryLedgerContext {
	// Production construction requires DeliveryRepository. Keeping this
	// capability check here lets focused struct-literal tests exercise legacy
	// compatibility without silently accepting an explicitly supplied context:
	// createDeliveryLedger rejects context+nil repository.
	if p == nil || p.bot == nil || p.bot.deliveryRepo == nil {
		return nil
	}
	return []deliveryLedgerContext{{
		UserID:         p.userID,
		Transport:      p.bot.transport.Kind(),
		ConversationID: p.convID,
	}}
}

// shadowRichRender exercises the exact rich serializer and structural limits
// without sending or recording the response body. The normal legacy path still
// performs its own render, so shadow mode is a true dual-render comparison.
func (p *responsePath) shadowRichRender(ctx context.Context, span trace.Span, content string) {
	if p.effectiveRichMode() != config.TelegramRichMessagesShadow {
		return
	}
	preflight, err := preflightRichDelivery(ctx, content, NewTelegramRenderer(p.logger))
	if err != nil {
		recordRichShadowEvaluation(richMetricShadowRejected, richMetricFallbackHardPreflight)
		span.SetAttributes(
			attribute.Bool("bot.rich_message.shadow", true),
			attribute.Bool("bot.rich_message.shadow_failed", true),
		)
		return
	}
	if preflight.localFallback {
		recordRichShadowEvaluation(richMetricShadowLocalFallback, preflight.fallbackReason)
		span.SetAttributes(
			attribute.Bool("bot.rich_message.shadow", true),
			attribute.Bool("bot.rich_message.shadow_local_fallback", true),
			attribute.String("bot.rich_message.shadow_fallback_reason", preflight.fallbackReason),
			attribute.Int("bot.rich_message.shadow_parts", len(preflight.parts)),
		)
		return
	}
	recordRichShadowEvaluation(richMetricShadowNative, richMetricFallbackNone)
	characters, blocks := 0, 0
	for _, part := range preflight.parts {
		characters += part.stats.Characters
		blocks += part.stats.Blocks
	}
	span.SetAttributes(
		attribute.Bool("bot.rich_message.shadow", true),
		attribute.Int("bot.rich_message.shadow_parts", len(preflight.parts)),
		attribute.Int("bot.rich_message.shadow_characters", characters),
		attribute.Int("bot.rich_message.shadow_blocks", blocks),
	)
}

// sendFinalAndPersist keeps the delivery/history ordering structural: only a
// confirmed final creates the assistant history row, and only then is the
// confirmed transport id linked to that row.
func (p *responsePath) sendFinalAndPersist(ctx context.Context, span trace.Span, content string, threadRoot *string) bool {
	if !p.sendFinal(ctx, span, content) {
		return false
	}
	ids := p.deliveredMessageIDs
	if len(ids) == 0 && p.deliveredMessageID != "" {
		ids = []string{p.deliveredMessageID}
	}
	p.bot.persistConfirmedAssistantReply(p.userID, span, content, p.convID, threadRoot,
		p.deliveryID, ids, nil, p.logger)
	return true
}

// saveAssistantReply persists the assistant's reply to history. trace_id
// links the stored reply to the trace that produced it, so an inbound
// reaction on the reply (matched by transport message id, back-filled via
// linkReplyTrace once the send returns it) resolves straight to its trace.
// In a channel, threadRoot marks the thread the bot spoke in for thread-reply
// gating (DM: nil). The bool reports whether the insert succeeded so callers
// never attach transport ids or artifacts to an older unlinked assistant row.
// Persistence remains best-effort with respect to the already-confirmed send.
func (b *Bot) saveAssistantReply(userID storage.ScopeID, span trace.Span, content, convID string, threadRoot *string, logger *slog.Logger) bool {
	message := b.assistantReplyMessage(userID, span, content, convID, threadRoot, logger)
	if err := b.msgRepo.AddMessageToHistory(userID, message); err != nil {
		logger.Error("failed to add assistant message to history", "error", err)
		return false
	}
	return true
}

func (b *Bot) assistantReplyMessage(userID storage.ScopeID, span trace.Span, content, convID string, threadRoot *string, logger *slog.Logger) storage.Message {
	var replyTraceID *string
	if sc := span.SpanContext(); sc.HasTraceID() {
		replyTraceID = strPtrOrNil(sc.TraceID().String())
	}
	return storage.Message{
		Role:           "assistant",
		Content:        content,
		ConversationID: strPtrOrNil(convID),
		ThreadRoot:     threadRoot,
		TraceID:        replyTraceID,
		DoNotStore:     b.privacyModeEnabled(userID, logger),
	}
}

// persistConfirmedAssistantReply persists exactly one history row after every
// operation of a logical reply was confirmed. V2 ledger-backed deliveries use
// one transaction for history, all transport IDs, artifacts, and delivery
// linkage. Older paths retain their best-effort compatibility behavior.
func (b *Bot) persistConfirmedAssistantReply(
	userID storage.ScopeID,
	span trace.Span,
	content, convID string,
	threadRoot *string,
	deliveryID int64,
	messageIDs []string,
	artifactIDs []int64,
	logger *slog.Logger,
) bool {
	message := b.assistantReplyMessage(userID, span, content, convID, threadRoot, logger)
	if deliveryID > 0 {
		if b.deliveryRepo == nil {
			logger.Error("delivery was confirmed without a configured delivery repository", "delivery_id", deliveryID)
			return false
		}
		if _, err := b.deliveryRepo.PersistOutboundDeliveryReply(userID, deliveryID, message, artifactIDs); err != nil {
			logger.Error("failed to atomically persist confirmed delivery reply", "delivery_id", deliveryID, "error", err)
			return false
		}
		return true
	}

	if b.exactMsgRepo != nil {
		historyID, err := b.exactMsgRepo.AddMessageToHistoryReturningID(userID, message)
		if err != nil {
			logger.Error("failed to add assistant message to history", "error", err)
			return false
		}
		if len(messageIDs) > 0 {
			messages := make([]storage.TransportMessage, 0, len(messageIDs))
			seen := make(map[string]struct{}, len(messageIDs))
			for _, rawID := range messageIDs {
				id := strings.TrimSpace(rawID)
				if id == "" {
					continue
				}
				if _, exists := seen[id]; exists {
					continue
				}
				seen[id] = struct{}{}
				messages = append(messages, storage.TransportMessage{
					Transport: b.transport.Kind(), ConversationID: convID, MessageID: id,
					Ordinal: len(messages), IsPrimary: len(messages) == 0,
				})
			}
			if len(messages) > 0 {
				if err := b.exactMsgRepo.LinkReplyTransportMessages(userID, historyID, messages); err != nil {
					logger.Warn("failed to link exact reply transport ids", "history_id", historyID, "error", err)
				}
			}
		}
		for _, artifactID := range artifactIDs {
			if b.artifactRepo == nil {
				break
			}
			if err := b.artifactRepo.UpdateMessageID(userID, artifactID, historyID); err != nil {
				logger.Warn("failed to link generated artifact to exact assistant row", "artifact_id", artifactID, "error", err)
			}
		}
		return true
	}

	if !b.saveAssistantReply(userID, span, content, convID, threadRoot, logger) {
		return false
	}
	if len(messageIDs) > 0 {
		b.linkReplyTrace(userID, messageIDs[0], logger)
	}
	if len(artifactIDs) > 0 && b.artifactRepo != nil {
		lastMsgs, err := b.msgRepo.GetRecentHistory(userID, 1)
		if err != nil || len(lastMsgs) == 0 {
			logger.Warn("failed to resolve legacy assistant row for generated artifacts", "error", err)
			return true
		}
		for _, artifactID := range artifactIDs {
			if err := b.artifactRepo.UpdateMessageID(userID, artifactID, lastMsgs[0].ID); err != nil {
				logger.Warn("failed to link generated artifact to legacy assistant row", "artifact_id", artifactID, "error", err)
			}
		}
	}
	return true
}

// turnCost returns the turn's USD cost, preferring the provider-reported
// total over the config-tier estimate.
func (b *Bot) turnCost(resp *laplace.Response, logger *slog.Logger) float64 {
	if resp.TotalCost != nil {
		return *resp.TotalCost
	}
	return b.getTieredCost(resp.PromptTokens, resp.CompletionTokens, logger)
}

// recordTurnStats persists the turn's token/cost stat and the agent-log
// entry. Shared by the live message path and SendTestMessage.
func (b *Bot) recordTurnStats(ctx context.Context, userID storage.ScopeID, resp *laplace.Response, cost float64, logger *slog.Logger) {
	stat := storage.Stat{
		UserID:     userID,
		TokensUsed: resp.PromptTokens + resp.CompletionTokens,
		CostUSD:    cost,
	}
	if err := b.statsRepo.AddStat(stat); err != nil {
		logger.Error("failed to add stat", "error", err)
	}
	b.laplaceAgent.LogExecution(ctx, userID, resp, cost)
}

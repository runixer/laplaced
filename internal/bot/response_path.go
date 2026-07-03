package bot

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
)

// responsePath owns delivery of one turn's reply: through the streaming sink
// when the transport supports it (progressive edits of one bubble), through
// the buffered sendRendered pipeline otherwise. Centralizing the branch keeps
// the "sink.Finalize is called exactly once on every path" invariant
// structural instead of relying on each call site remembering it — an
// orphaned placeholder bubble is the failure mode.
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

	tgDuration time.Duration
	tgCalls    int
}

// newResponsePath builds the delivery path for one turn, opening the
// streaming sink (and sending its placeholder) when the transport supports
// streaming.
func (b *Bot) newResponsePath(ctx context.Context, userID storage.ScopeID, convID, threadRoot, replyTo string, logger *slog.Logger) *responsePath {
	p := &responsePath{
		bot:        b,
		logger:     logger,
		userID:     userID,
		convID:     convID,
		threadRoot: threadRoot,
		replyTo:    replyTo,
		tgThreadID: atoiOrZero(threadRoot),
		tgReplyID:  atoiOrZero(replyTo),
	}
	p.tgChatID, _ = strconv.ParseInt(convID, 10, 64)

	if b.cfg.Bot.Streaming.Enabled && b.transport.Capabilities().SupportsStreaming {
		p.sink = newStreamSink(
			ctx, b.api, b.translator, b.cfg.Bot.Language,
			b.cfg.Bot.Streaming, p.tgChatID, p.tgThreadID, p.tgReplyID,
			logger,
		)
		// Account for the placeholder send in Telegram metrics.
		p.tgCalls++
	}
	return p
}

// recordTelegramMetrics flushes the accumulated Telegram counters. Deferred
// by the caller so early returns (error paths) are captured too.
func (p *responsePath) recordTelegramMetrics() {
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
	start := time.Now()
	if p.sink != nil {
		p.sink.Finalize(finalizeArgs{UserID: p.userID, HadError: true, ErrorText: errText}, p.finalizeCb(ctx))
	} else {
		n, _ := p.bot.sendRendered(ctx, p.convID, p.threadRoot, "", errText, p.logger)
		p.tgCalls += n
	}
	p.tgDuration += time.Since(start)
}

// flushSinkBeforeMedia finalizes an open sink with the turn's text before the
// media-aware reply path takes over. Image generation isn't streaming-aware:
// the sink's bubble gets the text, then the media path sends images (and any
// follow-up text) as separate messages. No-op on the buffered path.
func (p *responsePath) flushSinkBeforeMedia(ctx context.Context, content string) {
	if p.sink == nil {
		return
	}
	p.sink.Finalize(finalizeArgs{UserID: p.userID, FullText: content}, p.finalizeCb(ctx))
	RecordMessageTelegramEditCount(p.userID, p.sink.editCount)
}

// sendFinal delivers the turn's final reply: the sink edits its bubble in
// place and returns any overflow chunks to send as follow-ups; the buffered
// path renders and sends fresh messages, replying to the triggering message
// on the first chunk. Both variants link the message the user would react to
// back to the stored reply and record bot.reply_sent on the root span.
func (p *responsePath) sendFinal(ctx context.Context, span trace.Span, content string) {
	if p.sink != nil {
		extra, edits := p.sink.Finalize(
			finalizeArgs{UserID: p.userID, FullText: content},
			p.finalizeCb(ctx),
		)
		RecordMessageTelegramEditCount(p.userID, edits)
		// Link the streamed bubble (what the user reacts to) to the stored reply.
		if mid := p.sink.MessageID(); mid != 0 {
			p.bot.linkReplyTrace(p.userID, strconv.Itoa(mid), p.logger)
		}
		// chunks=1+len(extra) — one bubble edit plus any follow-up messages.
		obs.RecordContent(span, "bot.reply_sent", content,
			attribute.Int("chunks", 1+len(extra)))
		if len(extra) > 0 {
			start := time.Now()
			p.bot.sendResponses(ctx, p.tgChatID, extra, p.logger)
			p.tgDuration += time.Since(start)
			p.tgCalls += len(extra)
		}
		return
	}

	start := time.Now()
	sent, firstMsgID := p.bot.sendRendered(ctx, p.convID, p.threadRoot, p.replyTo, content, p.logger)
	p.tgDuration += time.Since(start)
	p.tgCalls += sent
	// Link the first chunk (what the user reacts to) to the stored reply.
	p.bot.linkReplyTrace(p.userID, firstMsgID, p.logger)
	obs.RecordContent(span, "bot.reply_sent", content, attribute.Int("chunks", sent))
}

// saveAssistantReply persists the assistant's reply to history. trace_id
// links the stored reply to the trace that produced it, so an inbound
// reaction on the reply (matched by transport message id, back-filled via
// linkReplyTrace once the send returns it) resolves straight to its trace.
// In a channel, threadRoot marks the thread the bot spoke in for thread-reply
// gating (DM: nil). Best-effort: a failure is logged, never blocks the send.
func (b *Bot) saveAssistantReply(userID storage.ScopeID, span trace.Span, content, convID string, threadRoot *string, logger *slog.Logger) {
	var replyTraceID *string
	if sc := span.SpanContext(); sc.HasTraceID() {
		replyTraceID = strPtrOrNil(sc.TraceID().String())
	}
	if err := b.msgRepo.AddMessageToHistory(userID, storage.Message{
		Role:           "assistant",
		Content:        content,
		ConversationID: strPtrOrNil(convID),
		ThreadRoot:     threadRoot,
		TraceID:        replyTraceID,
	}); err != nil {
		logger.Error("failed to add assistant message to history", "error", err)
	}
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

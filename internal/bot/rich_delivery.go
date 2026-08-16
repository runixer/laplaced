package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/markdown"
	"github.com/runixer/laplaced/internal/telegram"
)

// Keep margins below Telegram's documented Rich Message limits (32,768
// semantic characters, 500 blocks, and 15 levels of nesting). The additional
// byte/part limits bound work and wire payloads that are not covered by the
// semantic-character limit (notably tag and URL attributes).
const (
	richMessageSafeCharacterLimit = 30_000
	richMessageSafeBlockLimit     = 450
	richMessageMaxDepth           = 14
	richMessageMaxTableColumns    = 20
	richMessageMaxSourceBytes     = 256 << 10
	richMessageMaxParts           = 16
	richMessageMaxRenderedBytes   = 128 << 10
	richMessageMaxFallbackChunks  = 96
)

type richRenderedPart struct {
	source         string
	html           string
	stats          markdown.RichStats
	legacyFallback []string
}

// richDeliveryOutcome describes what Telegram has confirmed about the final
// delivery attempt. Unknown is intentionally distinct from rejected: retrying
// or changing format after an unknown persistent-send outcome can duplicate a
// message that Telegram already accepted.
type richDeliveryOutcome string

const (
	richDeliveryConfirmed richDeliveryOutcome = "confirmed"
	richDeliveryRejected  richDeliveryOutcome = "rejected"
	richDeliveryUnknown   richDeliveryOutcome = "unknown"
)

type richDeliveryResult struct {
	outcome        richDeliveryOutcome
	sent           int
	firstMsgID     string
	confirmedIDs   []string
	attempts       int
	failedPart     *int
	failedChunk    *int
	metricPath     string
	fallbackReason string
	err            error
}

type richDeliveryPreflight struct {
	parts          []richRenderedPart
	localFallback  bool
	fallbackReason string
}

// splitRichSources applies cheap, hard resource bounds before invoking either
// Markdown renderer. A hard preflight failure sends nothing; it is not a reason
// to feed an unbounded payload into the legacy renderer.
func splitRichSources(text string) ([]string, error) {
	if !utf8.ValidString(text) {
		return nil, errors.New("rich source is not valid UTF-8")
	}
	if len(text) > richMessageMaxSourceBytes {
		return nil, fmt.Errorf("rich source has %d bytes, limit is %d", len(text), richMessageMaxSourceBytes)
	}

	parts := fixListNumbering(splitByDelimiter(text))
	if len(parts) > richMessageMaxParts {
		return nil, fmt.Errorf("rich source has %d parts, limit is %d", len(parts), richMessageMaxParts)
	}
	for i, source := range parts {
		if strings.TrimSpace(source) == "" {
			return nil, fmt.Errorf("rich source part %d is empty", i)
		}
	}
	if len(parts) == 0 {
		return nil, errors.New("rich source produced no message parts")
	}
	return parts, nil
}

func renderRichSources(sources []string) ([]richRenderedPart, error) {
	rendered := make([]richRenderedPart, 0, len(sources))
	for i, source := range sources {
		html, stats, err := markdown.ToRichHTML(source)
		if err != nil {
			return nil, fmt.Errorf("render part %d: %w", i, err)
		}
		if strings.TrimSpace(html) == "" {
			return nil, fmt.Errorf("render part %d: empty rich HTML", i)
		}
		if len(html) > richMessageMaxRenderedBytes {
			return nil, fmt.Errorf("render part %d: %d rendered bytes exceed limit %d", i, len(html), richMessageMaxRenderedBytes)
		}
		if stats.Characters > richMessageSafeCharacterLimit {
			return nil, fmt.Errorf("render part %d: %d semantic characters exceed safe limit %d", i, stats.Characters, richMessageSafeCharacterLimit)
		}
		if stats.Blocks > richMessageSafeBlockLimit {
			return nil, fmt.Errorf("render part %d: %d blocks exceed safe limit %d", i, stats.Blocks, richMessageSafeBlockLimit)
		}
		if stats.MaxDepth > richMessageMaxDepth {
			return nil, fmt.Errorf("render part %d: nesting depth %d exceeds safe limit %d", i, stats.MaxDepth, richMessageMaxDepth)
		}
		if stats.MaxTableColumns > richMessageMaxTableColumns {
			return nil, fmt.Errorf("render part %d: table has %d columns, limit is %d", i, stats.MaxTableColumns, richMessageMaxTableColumns)
		}
		rendered = append(rendered, richRenderedPart{source: source, html: html, stats: stats})
	}
	return rendered, nil
}

// renderRichParts is kept as the pure rich-render preflight used by tests and
// future non-send consumers. The delivery path additionally pre-renders its
// policy-equivalent legacy representation before making a network call.
func renderRichParts(text string) ([]richRenderedPart, error) {
	sources, err := splitRichSources(text)
	if err != nil {
		return nil, err
	}
	return renderRichSources(sources)
}

// preflightRichDelivery prepares both representations before any persistent
// send. This prevents a late fallback render failure from leaving a half-rich,
// half-missing response and guarantees that fallback uses the same link/media/
// mention policy as native Rich Messages.
func preflightRichDelivery(ctx context.Context, text string, renderer *TelegramRenderer) (richDeliveryPreflight, error) {
	sources, err := splitRichSources(text)
	if err != nil {
		return richDeliveryPreflight{}, err
	}

	parts := make([]richRenderedPart, len(sources))
	fallbackChunks := 0
	for i, source := range sources {
		chunks, renderErr := renderer.renderSafeRichFallback(ctx, source)
		if renderErr != nil {
			return richDeliveryPreflight{}, fmt.Errorf("render safe fallback part %d: %w", i, renderErr)
		}
		if len(chunks) == 0 {
			return richDeliveryPreflight{}, fmt.Errorf("render safe fallback part %d: no chunks", i)
		}
		for j, chunk := range chunks {
			if strings.TrimSpace(chunk) == "" {
				return richDeliveryPreflight{}, fmt.Errorf("render safe fallback part %d chunk %d: empty", i, j)
			}
			if markdown.UTF16Length(chunk) > telegramMessageLimit {
				return richDeliveryPreflight{}, fmt.Errorf("render safe fallback part %d chunk %d exceeds wire limit", i, j)
			}
		}
		fallbackChunks += len(chunks)
		if fallbackChunks > richMessageMaxFallbackChunks {
			return richDeliveryPreflight{}, fmt.Errorf("safe fallback has %d chunks, limit is %d", fallbackChunks, richMessageMaxFallbackChunks)
		}
		parts[i] = richRenderedPart{source: source, legacyFallback: chunks}
	}

	richParts, richErr := renderRichSources(sources)
	if richErr != nil {
		return richDeliveryPreflight{
			parts:          parts,
			localFallback:  true,
			fallbackReason: "rich_render_or_limit",
		}, nil
	}
	for i := range parts {
		parts[i].html = richParts[i].html
		parts[i].stats = richParts[i].stats
	}
	return richDeliveryPreflight{parts: parts}, nil
}

func richOutcomeForError(err error) richDeliveryOutcome {
	var apiErr *telegram.APIError
	if errors.As(err, &apiErr) && apiErr.Code >= 400 && apiErr.Code < 500 {
		return richDeliveryRejected
	}
	return richDeliveryUnknown
}

func (r *richDeliveryResult) confirmMessage(msgID string) {
	if r.firstMsgID == "" {
		r.firstMsgID = msgID
	}
	r.confirmedIDs = append(r.confirmedIDs, msgID)
	r.sent = len(r.confirmedIDs)
}

func deliveryIndex(i int) *int {
	return &i
}

func finishRichDelivery(span trace.Span, result richDeliveryResult) richDeliveryResult {
	attrs := []attribute.KeyValue{
		attribute.String("bot.rich_message.delivery_outcome", string(result.outcome)),
		attribute.Int("bot.rich_message.confirmed_messages", len(result.confirmedIDs)),
		attribute.Int("bot.rich_message.delivery_attempts", result.attempts),
	}
	if result.failedPart != nil {
		attrs = append(attrs, attribute.Int("bot.rich_message.failed_part", *result.failedPart))
	}
	if result.failedChunk != nil {
		attrs = append(attrs, attribute.Int("bot.rich_message.failed_chunk", *result.failedChunk))
	}
	span.SetAttributes(attrs...)
	return result
}

// sendLegacyRichFallback sends only already-rendered chunks. It deliberately
// does not call the generic sendRendered helper: that helper can hide errors
// behind a generic-error send, while this path must preserve exact confirmed /
// rejected / unknown semantics for the persistent answer.
func (b *Bot) sendLegacyRichFallback(
	ctx context.Context,
	convID, threadRoot, replyTo string,
	parts []richRenderedPart,
	startPart int,
	result richDeliveryResult,
) richDeliveryResult {
	for i := startPart; i < len(parts); i++ {
		for j, chunk := range parts[i].legacyFallback {
			partReplyTo := ""
			if result.sent == 0 {
				partReplyTo = replyTo
			}

			result.attempts++
			msgID, err := b.transport.SendText(ctx, OutgoingResponse{
				ConversationID: convID,
				Text:           chunk,
				ThreadRoot:     threadRoot,
				ReplyTo:        partReplyTo,
			})
			if err != nil {
				result.outcome = richOutcomeForError(err)
				result.failedPart = deliveryIndex(i)
				result.failedChunk = deliveryIndex(j)
				result.err = fmt.Errorf("send safe fallback part %d chunk %d: %w", i, j, err)
				return result
			}
			if msgID == "" {
				result.outcome = richDeliveryUnknown
				result.failedPart = deliveryIndex(i)
				result.failedChunk = deliveryIndex(j)
				result.err = fmt.Errorf("send safe fallback part %d chunk %d returned no stable message id", i, j)
				return result
			}
			result.confirmMessage(msgID)
		}
	}
	result.outcome = richDeliveryConfirmed
	return result
}

// sendRichRendered is the buffered v1 final-response path. Every fallback
// chunk is prepared before the first network call. A confirmed Rich Message
// format rejection may switch to legacy starting at that part; an ambiguous
// failure never resends because Telegram may already have accepted the answer.
func (b *Bot) sendRichRendered(ctx context.Context, convID, threadRoot, replyTo, text string, logger *slog.Logger) richDeliveryResult {
	span := trace.SpanFromContext(ctx)
	renderer := NewTelegramRenderer(logger)
	preflight, err := preflightRichDelivery(ctx, text, renderer)
	if err != nil {
		logger.Error("rich message hard preflight rejected response", "error", err)
		span.SetAttributes(attribute.Bool("bot.rich_message.preflight_rejected", true))
		return finishRichDelivery(span, richDeliveryResult{
			outcome:        richDeliveryRejected,
			metricPath:     richMetricPathPreflightRejected,
			fallbackReason: richMetricFallbackHardPreflight,
			err:            err,
		})
	}

	if preflight.localFallback {
		logger.Warn("rich message local preflight selected safe legacy fallback", "reason", preflight.fallbackReason)
		span.SetAttributes(
			attribute.Bool("bot.rich_message.local_fallback", true),
			attribute.String("bot.rich_message.local_fallback_reason", preflight.fallbackReason),
		)
		result := b.sendLegacyRichFallback(ctx, convID, threadRoot, replyTo, preflight.parts, 0, richDeliveryResult{
			metricPath:     richMetricPathLocalFallback,
			fallbackReason: preflight.fallbackReason,
		})
		return finishRichDelivery(span, result)
	}

	totalCharacters, totalBlocks := 0, 0
	for _, part := range preflight.parts {
		totalCharacters += part.stats.Characters
		totalBlocks += part.stats.Blocks
	}
	span.SetAttributes(
		attribute.Bool("bot.rich_message.used", true),
		attribute.Int("bot.rich_message.parts", len(preflight.parts)),
		attribute.Int("bot.rich_message.characters", totalCharacters),
		attribute.Int("bot.rich_message.blocks", totalBlocks),
	)

	result := richDeliveryResult{
		metricPath:     richMetricPathNative,
		fallbackReason: richMetricFallbackNone,
	}
	for i, part := range preflight.parts {
		partReplyTo := ""
		if result.sent == 0 {
			partReplyTo = replyTo
		}

		result.attempts++
		msgID, sendErr := b.transport.SendText(ctx, OutgoingResponse{
			ConversationID: convID,
			Text:           part.html,
			ThreadRoot:     threadRoot,
			ReplyTo:        partReplyTo,
			Format:         ResponseFormatRichHTML,
		})
		if sendErr != nil {
			if errors.Is(sendErr, ErrRichMessageRejected) {
				result.failedPart = deliveryIndex(i)
				result.failedChunk = nil
				result.metricPath = richMetricPathAPIFallback
				result.fallbackReason = richMetricFallbackFormatRejected
				span.SetAttributes(
					attribute.Bool("bot.rich_message.api_fallback", true),
					attribute.Int("bot.rich_message.fallback_part", i),
				)
				logger.Warn("rich message format rejected; using pre-rendered safe legacy fallback", "part_index", i)
				result = b.sendLegacyRichFallback(ctx, convID, threadRoot, replyTo, preflight.parts, i, result)
				return finishRichDelivery(span, result)
			}

			result.outcome = richOutcomeForError(sendErr)
			result.failedPart = deliveryIndex(i)
			result.failedChunk = nil
			result.err = fmt.Errorf("send rich part %d: %w", i, sendErr)
			if result.outcome == richDeliveryUnknown {
				logger.Error("failed to send rich message; answer not resent after unknown outcome", "error", sendErr, "part_index", i)
				span.SetAttributes(attribute.Bool("bot.rich_message.ambiguous_failure", true))
			}
			span.SetAttributes(attribute.Int("bot.rich_message.failed_part", i))
			return finishRichDelivery(span, result)
		}
		if msgID == "" {
			result.outcome = richDeliveryUnknown
			result.failedPart = deliveryIndex(i)
			result.failedChunk = nil
			result.err = fmt.Errorf("send rich part %d returned no stable message id", i)
			span.SetAttributes(
				attribute.Bool("bot.rich_message.ambiguous_failure", true),
				attribute.Int("bot.rich_message.failed_part", i),
			)
			return finishRichDelivery(span, result)
		}
		result.confirmMessage(msgID)
	}

	result.outcome = richDeliveryConfirmed
	return finishRichDelivery(span, result)
}

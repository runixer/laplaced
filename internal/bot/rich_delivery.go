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
	richSplitDelimiter            = "###SPLIT###"
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
	richDeliveryConfirmed       richDeliveryOutcome = "confirmed"
	richDeliveryRejected        richDeliveryOutcome = "rejected"
	richDeliveryPartialRejected richDeliveryOutcome = "partial_rejected"
	richDeliveryUnknown         richDeliveryOutcome = "unknown"
	richDeliveryPartialUnknown  richDeliveryOutcome = "partial_unknown"
)

type richDeliveryResult struct {
	outcome        richDeliveryOutcome
	deliveryID     int64
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

// validateRichSourceBounds applies cheap hard bounds before invoking either
// Markdown renderer. A hard preflight failure sends nothing; it is not a reason
// to feed an unbounded payload into the legacy renderer.
func validateRichSourceBounds(text string) error {
	if !utf8.ValidString(text) {
		return errors.New("rich source is not valid UTF-8")
	}
	if len(text) > richMessageMaxSourceBytes {
		return fmt.Errorf("rich source has %d bytes, limit is %d", len(text), richMessageMaxSourceBytes)
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("rich source is empty")
	}
	return nil
}

func mergeBotRichStats(total *markdown.RichStats, next markdown.RichStats) {
	total.Characters += next.Characters
	total.Blocks += next.Blocks
	if next.MaxDepth > total.MaxDepth {
		total.MaxDepth = next.MaxDepth
	}
	if next.MaxTableColumns > total.MaxTableColumns {
		total.MaxTableColumns = next.MaxTableColumns
	}
}

func richPartWithinLimits(html string, stats markdown.RichStats) bool {
	return strings.TrimSpace(html) != "" &&
		len(html) <= richMessageMaxRenderedBytes &&
		stats.Characters <= richMessageSafeCharacterLimit &&
		stats.Blocks <= richMessageSafeBlockLimit &&
		stats.MaxDepth <= richMessageMaxDepth &&
		stats.MaxTableColumns <= richMessageMaxTableColumns
}

var (
	errRichPartFanout = errors.New("rich message part fan-out exceeds limit")
	errRichSplitEmpty = errors.New("rich split contains no message content")
)

// splitStandaloneRichSources recognizes a marker on its own physical line only
// when its exact bytes belong to a direct Paragraph Text node. This positive
// AST allowlist accepts ordinary soft-break prose while excluding every inline
// container plus hidden link/reference/raw-HTML syntax without reconstructing
// their Markdown source envelopes.
func splitStandaloneRichSources(text string) ([]string, error) {
	document, err := markdown.ParseRichFragments(text)
	if err != nil {
		return nil, err
	}
	protocol, err := scanStandaloneRichProtocolLines(document)
	if err != nil {
		return nil, err
	}
	markers := make([]markdown.RichSourceRange, 0, len(protocol))
	for _, line := range protocol {
		if line.kind == generatedMediaProtocolSplit {
			markers = append(markers, line.sourceRange)
		}
	}
	if len(markers) == 0 {
		return []string{text}, nil
	}

	sources := make([]string, 0, len(markers)+1)
	start := 0
	for _, marker := range markers {
		if source := text[start:marker.Start]; strings.TrimSpace(source) != "" {
			sources = append(sources, source)
		}
		start = marker.End
	}
	if source := text[start:]; strings.TrimSpace(source) != "" {
		sources = append(sources, source)
	}
	if len(sources) == 0 {
		return nil, errRichSplitEmpty
	}
	if len(sources) > richMessageMaxParts {
		return nil, fmt.Errorf("%w: %d parts, limit is %d", errRichPartFanout, len(sources), richMessageMaxParts)
	}
	return sources, nil
}

// renderRichParts resolves protocol boundaries, parses each resulting source
// independently, and packs complete top-level blocks.
// `###SPLIT###` is a hard boundary only when it is its own top-level block;
// occurrences inside prose, code, tables, or another atomic block stay text.
// Automatic packing never cuts a code block, formula, table, list, quote, or
// any other fragment produced by ParseRichFragments.
func renderRichParts(text string) ([]richRenderedPart, error) {
	if err := validateRichSourceBounds(text); err != nil {
		return nil, err
	}
	sources, err := splitStandaloneRichSources(text)
	if err != nil {
		return nil, err
	}

	parts := make([]richRenderedPart, 0, min(len(sources), richMessageMaxParts))
	var source, html strings.Builder
	var stats markdown.RichStats
	flush := func() error {
		if source.Len() == 0 && html.Len() == 0 {
			return errors.New("rich split produced an empty part")
		}
		if !richPartWithinLimits(html.String(), stats) {
			return fmt.Errorf("rich part exceeds structural or wire limits")
		}
		parts = append(parts, richRenderedPart{source: source.String(), html: html.String(), stats: stats})
		if len(parts) > richMessageMaxParts {
			return fmt.Errorf("%w: more than %d packed parts", errRichPartFanout, richMessageMaxParts)
		}
		source.Reset()
		html.Reset()
		stats = markdown.RichStats{}
		return nil
	}

	fragmentIndex := 0
	for sourceIndex, explicitSource := range sources {
		document, parseErr := markdown.ParseRichFragments(explicitSource)
		if parseErr != nil {
			return nil, fmt.Errorf("parse rich source part %d: %w", sourceIndex, parseErr)
		}
		if len(document.Fragments) == 0 {
			return nil, fmt.Errorf("rich source part %d produced no blocks", sourceIndex)
		}
		for _, fragment := range document.Fragments {
			candidateStats := stats
			mergeBotRichStats(&candidateStats, fragment.Stats)
			candidateHTML := html.String() + fragment.HTML
			if (source.Len() > 0 || html.Len() > 0) && !richPartWithinLimits(candidateHTML, candidateStats) {
				if err := flush(); err != nil {
					return nil, fmt.Errorf("pack before fragment %d: %w", fragmentIndex, err)
				}
				candidateStats = fragment.Stats
				candidateHTML = fragment.HTML
			}
			if !richPartWithinLimits(candidateHTML, candidateStats) && strings.TrimSpace(fragment.HTML) != "" {
				return nil, fmt.Errorf("atomic rich fragment %d (%s) exceeds structural or wire limits", fragmentIndex, fragment.Kind)
			}
			source.WriteString(fragment.LegacySource)
			html.WriteString(fragment.HTML)
			stats = candidateStats
			fragmentIndex++
		}
		if err := flush(); err != nil {
			return nil, fmt.Errorf("hard split after source part %d: %w", sourceIndex, err)
		}
	}
	return parts, nil
}

func renderSafeFallbackSources(ctx context.Context, renderer *TelegramRenderer, text string) ([]string, error) {
	sources, err := splitStandaloneRichSources(text)
	if err != nil {
		return nil, err
	}
	var chunks []string
	for i, source := range sources {
		partChunks, renderErr := renderer.renderSafeRichFallbackPart(ctx, source)
		if renderErr != nil {
			return nil, fmt.Errorf("render safe fallback source %d: %w", i, renderErr)
		}
		chunks = append(chunks, partChunks...)
	}
	return chunks, nil
}

// preflightRichDelivery prepares both representations before any persistent
// send. This prevents a late fallback render failure from leaving a half-rich,
// half-missing response and guarantees that fallback uses the same link/media/
// mention policy as native Rich Messages.
func preflightRichDelivery(ctx context.Context, text string, renderer *TelegramRenderer) (richDeliveryPreflight, error) {
	if err := validateRichSourceBounds(text); err != nil {
		return richDeliveryPreflight{}, err
	}

	richParts, richErr := renderRichParts(text)
	if richErr != nil {
		if errors.Is(richErr, errRichPartFanout) || errors.Is(richErr, errRichSplitEmpty) {
			return richDeliveryPreflight{}, richErr
		}
		chunks, fallbackErr := renderSafeFallbackSources(ctx, renderer, text)
		if fallbackErr != nil {
			return richDeliveryPreflight{}, fmt.Errorf("render safe fallback after rich packing: %w", fallbackErr)
		}
		if len(chunks) == 0 || len(chunks) > richMessageMaxFallbackChunks {
			return richDeliveryPreflight{}, fmt.Errorf("safe fallback has %d chunks, limit is %d", len(chunks), richMessageMaxFallbackChunks)
		}
		for i, chunk := range chunks {
			if strings.TrimSpace(chunk) == "" || markdown.UTF16Length(chunk) > telegramMessageLimit {
				return richDeliveryPreflight{}, fmt.Errorf("safe fallback chunk %d is empty or exceeds wire limit", i)
			}
		}
		return richDeliveryPreflight{
			parts:          []richRenderedPart{{source: text, legacyFallback: chunks}},
			localFallback:  true,
			fallbackReason: "rich_render_or_limit",
		}, nil
	}

	parts := make([]richRenderedPart, len(richParts))
	fallbackChunks := 0
	for i, richPart := range richParts {
		chunks, renderErr := renderer.renderSafeRichFallbackPart(ctx, richPart.source)
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
		parts[i] = richPart
		parts[i].legacyFallback = chunks
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

func richTextFallbackOperations(convID, threadRoot string, parts []richRenderedPart, start int) []deliveryOperation {
	var operations []deliveryOperation
	for i := start; i < len(parts); i++ {
		operations = append(operations, generatedLegacyTextOperations(convID, threadRoot, parts[i].legacyFallback)...)
	}
	return operations
}

func richTextDeliveryPlan(convID, threadRoot, replyTo string, preflight richDeliveryPreflight) (deliveryPlan, error) {
	if len(preflight.parts) == 0 {
		return deliveryPlan{}, errors.New("rich delivery preflight has no parts")
	}
	if preflight.localFallback {
		operations := richTextFallbackOperations(convID, threadRoot, preflight.parts, 0)
		if len(operations) > 0 {
			operations[0].Text.ReplyTo = replyTo
		}
		plan := deliveryPlan{Operations: operations}
		return plan, plan.validate()
	}
	operations := make([]deliveryOperation, 0, len(preflight.parts))
	for i, part := range preflight.parts {
		op := deliveryOperation{
			Kind: persistentOperationRichText,
			Text: &OutgoingResponse{
				ConversationID: convID,
				ThreadRoot:     threadRoot,
				Text:           part.html,
				Format:         ResponseFormatRichHTML,
			},
			formatFallback: richTextFallbackOperations(convID, threadRoot, preflight.parts, i),
		}
		if i == 0 {
			op.Text.ReplyTo = replyTo
			if len(op.formatFallback) > 0 {
				op.formatFallback[0].Text.ReplyTo = replyTo
			}
		}
		operations = append(operations, op)
	}
	plan := deliveryPlan{Operations: operations}
	return plan, plan.validate()
}

// sendRichRendered is the buffered V2 final-response path. It executes one
// immutable plan shared by ordinary rich text, block-aware splits and the
// content-free delivery ledger. A confirmed format rejection may switch only
// to the pre-rendered suffix; an ambiguous failure never resends.
func (b *Bot) sendRichRendered(ctx context.Context, convID, threadRoot, replyTo, text string, logger *slog.Logger, ledger ...deliveryLedgerContext) richDeliveryResult {
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
		plan, planErr := richTextDeliveryPlan(convID, threadRoot, replyTo, preflight)
		if planErr != nil {
			return finishRichDelivery(span, richDeliveryResult{
				outcome:        richDeliveryRejected,
				metricPath:     richMetricPathPreflightRejected,
				fallbackReason: richMetricFallbackHardPreflight,
				err:            planErr,
			})
		}
		result := b.executeDeliveryPlan(ctx, plan, ledger...)
		result.metricPath = richMetricPathLocalFallback
		result.fallbackReason = preflight.fallbackReason
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

	plan, planErr := richTextDeliveryPlan(convID, threadRoot, replyTo, preflight)
	if planErr != nil {
		return finishRichDelivery(span, richDeliveryResult{
			outcome:        richDeliveryRejected,
			metricPath:     richMetricPathPreflightRejected,
			fallbackReason: richMetricFallbackHardPreflight,
			err:            planErr,
		})
	}
	result := b.executeDeliveryPlan(ctx, plan, ledger...)
	if result.outcome == richDeliveryUnknown || result.outcome == richDeliveryPartialUnknown {
		logger.Error("failed to send rich message; answer not resent after unknown outcome", "error", result.err)
		span.SetAttributes(attribute.Bool("bot.rich_message.ambiguous_failure", true))
	}
	if result.metricPath == richMetricPathAPIFallback {
		span.SetAttributes(attribute.Bool("bot.rich_message.api_fallback", true))
	}
	return finishRichDelivery(span, result)
}

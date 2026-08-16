package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/storage"
)

// sendResponseWithGeneratedImages handles the reply path when Laplace produced
// one or more generated images as artifacts. It is transport-neutral: it loads
// the artifacts, builds a transport.OutgoingMedia, and hands it to the active
// transport. History and links are created only after the media and every text
// follow-up have confirmed stable transport ids.
//
// Flow:
//  1. Load each artifact (user-isolated) and read its file bytes.
//  2. Prepend compact 🎨 markers to the text for history storage.
//  3. Split the response into a caption (≤ the transport's media caption budget)
//     and follow-up text; send the media with the caption, then the follow-up
//     through the standard rendered-text path.
//  4. Save the complete assistant history row and link its transport/artifact
//     ids only after every persistent send was confirmed.
//
// An error or malformed SendMedia success may mean the request reached the
// server, so the function never resends that payload through a text-only path.
type generatedDeliveryResult struct {
	outcome          richDeliveryOutcome
	duration         time.Duration
	attempts         int
	primaryMessageID string
	persisted        bool
	err              error
}

func (b *Bot) sendResponseWithGeneratedImages(
	ctx context.Context,
	path *responsePath,
	historyThreadRoot *string,
	responseText string,
	artifactIDs []int64,
	logger *slog.Logger,
) (result generatedDeliveryResult) {
	if path == nil {
		result.outcome = richDeliveryRejected
		result.err = fmt.Errorf("generated media delivery path is nil")
		return result
	}
	metricPath := richMetricPathTextFallback
	metricFallbackReason := richMetricFallbackMediaUnavailable
	nativeAttachmentCount, nativeAttachmentBytes := 0, 0
	richMode := path.effectiveRichMode()
	if richMode == config.TelegramRichMessagesSend {
		defer func() {
			recordRichFinalDelivery(richFinalMetric{
				contentKind:       richMetricContentPhoto,
				path:              metricPath,
				outcome:           result.outcome,
				fallbackReason:    metricFallbackReason,
				err:               result.err,
				nativeAttachments: nativeAttachmentCount,
				nativeBytes:       nativeAttachmentBytes,
			})
		}()
	}
	shadowOutcome := richMetricShadowLocalFallback
	shadowFallbackReason := richMetricFallbackMediaUnavailable
	if richMode == config.TelegramRichMessagesShadow {
		// Generated-media turns do not pass through responsePath.sendFinal, so
		// this function owns their one shadow observation. Keep it installed
		// before repository/storage checks so even unavailable artifacts produce
		// one bounded, actionable decision instead of disappearing from shadow
		// telemetry.
		defer func() {
			recordRichShadowEvaluation(shadowOutcome, shadowFallbackReason)
		}()
	}
	userID := path.userID
	if b.artifactRepo == nil {
		logger.Error("artifact repo not configured but generated artifacts present")
		return b.sendTextOnlyFallback(ctx, path, historyThreadRoot, responseText, logger)
	}

	if b.fileStorage == nil {
		logger.Error("file storage not configured — cannot send generated images")
		return b.sendTextOnlyFallback(ctx, path, historyThreadRoot, responseText, logger)
	}

	// 1. Load artifacts in the order produced and read their bytes.
	loaded := b.loadArtifactBytes(ctx, userID, artifactIDs, logger)
	if len(loaded) == 0 {
		logger.Error("no generated artifacts loadable from disk — falling back to text-only reply")
		return b.sendTextOnlyFallback(ctx, path, historyThreadRoot, responseText, logger)
	}

	// 2. Compact history markers + text.
	historyContent := buildAssistantHistoryContent(loaded, responseText)

	// 3. Fit the caption to the transport's media caption budget measured on
	// the rendered wire format; the rest is sent as follow-up text.
	var caption, followUp string
	if path.effectiveRichMode() == config.TelegramRichMessagesSend {
		if telegramRenderer, ok := b.renderer.(*TelegramRenderer); ok {
			caption, followUp = telegramRenderer.RenderSafeRichCaption(ctx, responseText)
		} else {
			// Rich send mode is Telegram-only. If that invariant is ever broken,
			// fail closed by keeping model content out of the media caption.
			followUp = responseText
		}
	} else {
		caption, followUp = b.renderer.RenderCaption(ctx, responseText)
	}

	items := make([]OutgoingMediaItem, 0, len(loaded))
	for _, la := range loaded {
		items = append(items, OutgoingMediaItem{
			Data:     la.data,
			Filename: la.artifact.OriginalName,
			MIME:     la.artifact.MimeType,
		})
	}
	if richMode == config.TelegramRichMessagesShadow {
		if _, _, ok, reason := b.planGeneratedRichMedia(ctx, path, responseText, items); ok {
			shadowOutcome = richMetricShadowNative
			shadowFallbackReason = richMetricFallbackNone
		} else {
			shadowFallbackReason = reason
		}
	}
	metricPath = richMetricPathLegacyMedia
	metricFallbackReason = richMetricFallbackMediaIneligible

	deliveryStart := time.Now()
	defer func() { result.duration = time.Since(deliveryStart) }()

	// Prefer one native persistent Rich Message for a single generated photo.
	// The trusted media reference is constructed by the transport from these
	// artifact bytes; model-authored image URLs remain suppressed by the Rich
	// HTML renderer. Larger images that the established Telegram policy sends
	// as documents, multiple outputs, and non-rich transports retain the legacy
	// media + caption/follow-up path below.
	nativeConfirmed := false
	if richTransport, richMedia, ok, ineligibleReason := b.prepareGeneratedRichMedia(ctx, path, responseText, items); ok {
		metricPath = richMetricPathNative
		metricFallbackReason = richMetricFallbackNone
		nativeAttachmentCount = len(richMedia.Items)
		for _, item := range richMedia.Items {
			nativeAttachmentBytes += len(item.Data)
		}
		result.attempts++
		mediaID, err := richTransport.SendRichMedia(ctx, richMedia)
		switch {
		case err == nil && strings.TrimSpace(mediaID) != "":
			result.primaryMessageID = mediaID
			nativeConfirmed = true
		case err == nil:
			result.outcome = richDeliveryUnknown
			result.err = fmt.Errorf("send generated rich media returned no stable message id")
			return result
		case errors.Is(err, ErrRichMessageRejected):
			// Telegram definitively rejected the rich representation, so no
			// persistent side effect occurred and the pre-rendered legacy media
			// path is safe. Ambiguous failures never reach this branch.
			logger.Warn("generated rich media rejected; using legacy media delivery", "error", err)
			metricPath = richMetricPathAPIFallback
			metricFallbackReason = richMetricFallbackFormatRejected
		default:
			logger.Error("failed to send generated rich media", "error", err)
			result.outcome = richOutcomeForError(err)
			result.err = fmt.Errorf("send generated rich media: %w", err)
			return result
		}
	} else {
		metricFallbackReason = ineligibleReason
	}

	if !nativeConfirmed {
		result.attempts++
		mediaID, err := b.transport.SendMedia(ctx, OutgoingMedia{
			ConversationID: path.convID,
			ThreadRoot:     path.threadRoot,
			ReplyTo:        path.replyTo,
			Caption:        caption,
			Items:          items,
		})
		if err != nil {
			logger.Error("failed to send generated media", "error", err)
			result.outcome = richOutcomeForError(err)
			result.err = fmt.Errorf("send generated media: %w", err)
			return result
		}
		if strings.TrimSpace(mediaID) == "" {
			result.outcome = richDeliveryUnknown
			result.err = fmt.Errorf("send generated media returned no stable message id")
			return result
		}
		result.primaryMessageID = mediaID

		// Send any remaining text as follow-up messages (no reply-to: the
		// media already anchored to the user's message).
		if strings.TrimSpace(followUp) != "" {
			followUpResult := b.sendGeneratedTextDelivery(ctx, path, "", followUp, logger)
			result.attempts += followUpResult.attempts
			if followUpResult.outcome != richDeliveryConfirmed {
				result.outcome = followUpResult.outcome
				result.err = fmt.Errorf("send generated media follow-up: %w", followUpResult.err)
				return result
			}
		}
	}

	result.outcome = richDeliveryConfirmed
	span := trace.SpanFromContext(ctx)
	if b.saveAssistantReply(userID, span, historyContent, path.convID, historyThreadRoot, logger) {
		result.persisted = true
		b.linkReplyTrace(userID, result.primaryMessageID, logger)
		b.linkGeneratedArtifactsToLatestAssistant(userID, loaded, logger)
	}
	return result
}

// prepareGeneratedRichMedia returns the narrow native-rich media MVP: one
// generated image that would otherwise be sent as a Telegram photo, plus one
// complete Rich HTML part. It is deliberately conservative so existing album,
// document-quality and split-message behavior remains byte-compatible.
func (b *Bot) prepareGeneratedRichMedia(
	ctx context.Context,
	path *responsePath,
	responseText string,
	items []OutgoingMediaItem,
) (RichMediaTransport, OutgoingRichMedia, bool, string) {
	if path == nil || path.effectiveRichMode() != config.TelegramRichMessagesSend {
		return nil, OutgoingRichMedia{}, false, richMetricFallbackMediaIneligible
	}
	return b.planGeneratedRichMedia(ctx, path, responseText, items)
}

// planGeneratedRichMedia performs the exact native-photo eligibility and
// bounded Rich HTML preflight without selecting a rollout mode or causing a
// network side effect. Send mode consumes the returned plan; shadow mode only
// records the same decision while retaining legacy delivery.
func (b *Bot) planGeneratedRichMedia(
	ctx context.Context,
	path *responsePath,
	responseText string,
	items []OutgoingMediaItem,
) (RichMediaTransport, OutgoingRichMedia, bool, string) {
	if path == nil || len(items) != 1 {
		return nil, OutgoingRichMedia{}, false, richMetricFallbackMediaIneligible
	}
	richTransport, ok := b.transport.(RichMediaTransport)
	if !ok {
		return nil, OutgoingRichMedia{}, false, richMetricFallbackMediaIneligible
	}
	item := items[0]
	if len(item.Data) == 0 || item.AsDocument || !strings.HasPrefix(strings.ToLower(item.MIME), "image/") {
		return nil, OutgoingRichMedia{}, false, richMetricFallbackMediaIneligible
	}
	threshold := b.cfg.Agents.ImageGenerator.DocumentThresholdBytes
	if threshold > 0 && len(item.Data) > threshold {
		return nil, OutgoingRichMedia{}, false, richMetricFallbackMediaIneligible
	}
	renderer, ok := b.renderer.(*TelegramRenderer)
	if !ok {
		return nil, OutgoingRichMedia{}, false, richMetricFallbackMediaIneligible
	}
	preflight, err := preflightRichDelivery(ctx, responseText, renderer)
	if err != nil {
		return nil, OutgoingRichMedia{}, false, richMetricFallbackHardPreflight
	}
	if preflight.localFallback {
		return nil, OutgoingRichMedia{}, false, preflight.fallbackReason
	}
	if len(preflight.parts) != 1 {
		return nil, OutgoingRichMedia{}, false, richMetricFallbackMediaIneligible
	}
	part := preflight.parts[0]
	if strings.TrimSpace(part.html) == "" {
		return nil, OutgoingRichMedia{}, false, richMetricFallbackMediaIneligible
	}
	if part.stats.Blocks+1 > richMessageSafeBlockLimit ||
		len(part.html)+len(generatedRichPhotoHTML) > richMessageMaxRenderedBytes {
		return nil, OutgoingRichMedia{}, false, richMetricFallbackRenderOrLimit
	}
	return richTransport, OutgoingRichMedia{
		ConversationID: path.convID,
		ThreadRoot:     path.threadRoot,
		ReplyTo:        path.replyTo,
		HTML:           part.html,
		Items:          []OutgoingMediaItem{item},
	}, true, richMetricFallbackNone
}

// deliverGeneratedOnError delivers generated images from a failed laplace turn
// with a short canned caption instead of the error text (no double apology: the
// caption already says the text reply failed). attempted=false means there was
// nothing to deliver and the caller may send its normal error reply. Once any
// delivery is attempted, even an unconfirmed result is handled: sending a
// second error payload could duplicate a request that reached the server.
func (b *Bot) deliverGeneratedOnError(
	ctx context.Context,
	path *responsePath,
	resp *laplace.Response,
	logger *slog.Logger,
) (attempted, confirmed bool) {
	if resp == nil || len(resp.GeneratedArtifactIDs) == 0 {
		return false, false
	}
	caption := b.translator.Get(b.cfg.Bot.Language, "bot.image_delivered_text_failed")
	logger.Info("delivering generated images despite laplace failure",
		"artifact_ids", resp.GeneratedArtifactIDs)
	path.flushSinkBeforeMedia(ctx, caption)
	result := b.sendResponseWithGeneratedImages(
		ctx, path, nil, caption, resp.GeneratedArtifactIDs, logger,
	)
	path.tgDuration += result.duration
	path.tgCalls += result.attempts
	if result.outcome != richDeliveryConfirmed {
		logger.Error("generated media error-response delivery was not confirmed", "error", result.err)
		return true, false
	}
	return true, true
}

// sendTextOnlyFallback is the emergency path when media cannot be sent.
// Local preflight failure is known not to have sent media, so using the normal
// text delivery path is safe. History still follows delivery confirmation.
func (b *Bot) sendTextOnlyFallback(
	ctx context.Context,
	path *responsePath,
	historyThreadRoot *string,
	responseText string,
	logger *slog.Logger,
) generatedDeliveryResult {
	start := time.Now()
	delivery := b.sendGeneratedTextDelivery(ctx, path, path.replyTo, responseText, logger)
	result := generatedDeliveryResult{
		outcome:          delivery.outcome,
		duration:         time.Since(start),
		attempts:         delivery.attempts,
		primaryMessageID: delivery.firstMsgID,
		err:              delivery.err,
	}
	if delivery.outcome != richDeliveryConfirmed {
		return result
	}
	span := trace.SpanFromContext(ctx)
	if b.saveAssistantReply(path.userID, span, responseText, path.convID, historyThreadRoot, logger) {
		result.persisted = true
		b.linkReplyTrace(path.userID, result.primaryMessageID, logger)
	}
	return result
}

func (b *Bot) sendGeneratedTextDelivery(ctx context.Context, path *responsePath, replyTo, text string, logger *slog.Logger) richDeliveryResult {
	if path.effectiveRichMode() == config.TelegramRichMessagesSend {
		return b.sendRichRendered(ctx, path.convID, path.threadRoot, replyTo, text, logger)
	}
	return b.sendRenderedDelivery(ctx, path.convID, path.threadRoot, replyTo, text, logger)
}

// linkGeneratedArtifactsToLatestAssistant runs only after the assistant
// history insert succeeded. A lookup failure leaves artifacts unlinked rather
// than risking attachment to an older row.
func (b *Bot) linkGeneratedArtifactsToLatestAssistant(userID storage.ScopeID, loaded []loadedArtifact, logger *slog.Logger) {
	lastMsgs, err := b.msgRepo.GetRecentHistory(userID, 1)
	if err != nil {
		logger.Warn("failed to resolve generated-media assistant history row", "error", err)
		return
	}
	if len(lastMsgs) == 0 {
		logger.Warn("generated-media assistant history row was not found")
		return
	}
	assistantMsgID := lastMsgs[0].ID
	for _, la := range loaded {
		if err := b.artifactRepo.UpdateMessageID(userID, la.artifact.ID, assistantMsgID); err != nil {
			logger.Warn("failed to link generated artifact to assistant message",
				"artifact_id", la.artifact.ID, "error", err)
		}
	}
}

// loadedArtifact pairs an Artifact row with its on-disk bytes.
type loadedArtifact struct {
	artifact *storage.Artifact
	data     []byte
}

// loadArtifactBytes resolves each artifact by ID (user-isolated) and reads
// its file bytes. Artifacts that fail to load are skipped with a warning —
// the caller decides what to do with an empty result.
func (b *Bot) loadArtifactBytes(ctx context.Context, userID storage.ScopeID, ids []int64, logger *slog.Logger) []loadedArtifact {
	out := make([]loadedArtifact, 0, len(ids))
	for _, id := range ids {
		art, err := b.artifactRepo.GetArtifact(userID, id)
		if err != nil {
			logger.Warn("failed to load generated artifact", "artifact_id", id, "error", err)
			continue
		}
		if art == nil {
			logger.Warn("generated artifact not found", "artifact_id", id, "user_id", userID)
			continue
		}
		// GetArtifact already enforces user_id = ?. Still, be defensive.
		if art.UserID != userID {
			logger.Error("user isolation violation: artifact user_id mismatch",
				"artifact_id", id, "expected", userID, "got", art.UserID)
			continue
		}
		data, err := b.fileStorage.ReadFile(ctx, art.FilePath)
		if err != nil {
			logger.Warn("failed to read generated artifact file",
				"artifact_id", id, "key", art.FilePath, "error", err)
			continue
		}
		out = append(out, loadedArtifact{artifact: art, data: data})
	}
	return out
}

// buildAssistantHistoryContent formats the assistant history content as
// compact markers for each generated image followed by the free-text reply.
// Matches the existing document marker convention: "📄 filename (artifact:N)".
func buildAssistantHistoryContent(loaded []loadedArtifact, text string) string {
	var sb strings.Builder
	for _, la := range loaded {
		fmt.Fprintf(&sb, "🎨 %s (artifact:%d)\n", la.artifact.OriginalName, la.artifact.ID)
	}
	if trimmed := strings.TrimSpace(text); trimmed != "" {
		sb.WriteString("\n")
		sb.WriteString(trimmed)
	}
	return sb.String()
}

// splitCaption splits a response into a caption suitable for the first media
// item (≤ limit chars) and the remaining text to send as follow-up. The split
// is on the last whitespace boundary before the limit so words stay intact.
func splitCaption(text string, limit int) (caption, followUp string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	// Caption length is counted in runes here as a close-enough proxy for the
	// transport's UTF-16/character budget.
	runes := []rune(text)
	if len(runes) <= limit {
		return text, ""
	}
	// Find last whitespace boundary before limit.
	cut := limit
	for cut > 0 && !isSpaceRune(runes[cut-1]) && cut > limit-120 {
		cut--
	}
	if cut <= 0 {
		cut = limit
	}
	caption = strings.TrimSpace(string(runes[:cut]))
	followUp = strings.TrimSpace(string(runes[cut:]))
	return caption, followUp
}

func isSpaceRune(r rune) bool {
	return r == ' ' || r == '\n' || r == '\t' || r == '\r'
}

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
	confirmedIDs     []string
	deliveryID       int64
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
	responseText = scrubModelArtifactReferences(responseText)
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
	deliveryStart := time.Now()
	defer func() { result.duration = time.Since(deliveryStart) }()
	// A local planner/validation rejection is known to have issued no
	// persistent request. Install this guard before artifact loading and every
	// text-only fallback so a missing artifact combined with an invalid text
	// plan cannot end the turn silently. Unknown or partial outcomes must never
	// enter this branch because another send could duplicate accepted content.
	defer func() {
		if result.outcome == richDeliveryRejected && result.attempts == 0 {
			result.attempts += b.sendGenericError(ctx, path.convID, path.threadRoot, logger)
		}
	}()
	userID := path.userID
	// MEDIA/SPLIT are turn-local layout protocol, never assistant prose. Resolve
	// their source views before repository access so even a missing artifact can
	// fall back to marker-free text without persisting protocol into history.
	deliveryText, historyText := responseText, responseText
	if protocolLayout, protocolErr := parseGeneratedMediaLayout(responseText, nil); protocolErr != nil {
		logger.Error("failed to resolve generated-media protocol", "error", protocolErr)
	} else {
		historyText = protocolLayout.MarkerFreeSource
		if cleanDeliveryText, cleanErr := generatedMediaDeliverySource(responseText, protocolLayout); cleanErr != nil {
			logger.Error("failed to clean generated-media delivery source", "error", cleanErr)
		} else {
			deliveryText = cleanDeliveryText
		}
	}
	if b.artifactRepo == nil {
		logger.Error("artifact repo not configured but generated artifacts present")
		return b.sendTextOnlyFallback(ctx, path, historyThreadRoot, deliveryText, historyText, logger)
	}

	if b.fileStorage == nil {
		logger.Error("file storage not configured — cannot send generated images")
		return b.sendTextOnlyFallback(ctx, path, historyThreadRoot, deliveryText, historyText, logger)
	}

	// 1. Load artifacts in the order produced and read their bytes.
	loaded := b.loadArtifactBytes(ctx, userID, artifactIDs, logger)
	if len(loaded) == 0 {
		logger.Error("no generated artifacts loadable from disk — falling back to text-only reply")
		return b.sendTextOnlyFallback(ctx, path, historyThreadRoot, deliveryText, historyText, logger)
	}

	// 3. Fit the caption to the transport's media caption budget measured on
	// the rendered wire format; the rest is sent as follow-up text.
	var caption string
	var followUps []string
	if path.effectiveRichMode() == config.TelegramRichMessagesSend {
		if telegramRenderer, ok := b.renderer.(*TelegramRenderer); ok {
			var followUp string
			caption, followUp = telegramRenderer.RenderSafeRichCaption(ctx, deliveryText)
			if strings.TrimSpace(followUp) != "" {
				followUps = append(followUps, followUp)
			}
		} else {
			// Rich send mode is Telegram-only. If that invariant is ever broken,
			// fail closed by keeping model content out of the media caption.
			followUps = append(followUps, deliveryText)
		}
	} else {
		// Legacy captions do not understand the application SPLIT protocol.
		// Resolve its AST-safe physical-line boundaries first so a short response
		// cannot leak the marker literally inside the media caption.
		sources, splitErr := splitStandaloneRichSources(deliveryText)
		if splitErr != nil {
			if !errors.Is(splitErr, errRichSplitEmpty) {
				logger.Warn("could not preserve generated-media split boundaries in legacy caption; using marker-free text", "error", splitErr)
			}
			sources = nil
			if strings.TrimSpace(historyText) != "" {
				sources = []string{historyText}
			}
		}
		if len(sources) > 0 {
			var firstOverflow string
			caption, firstOverflow = b.renderer.RenderCaption(ctx, sources[0])
			if strings.TrimSpace(firstOverflow) != "" {
				followUps = append(followUps, firstOverflow)
			}
			followUps = append(followUps, sources[1:]...)
		}
	}

	items := make([]OutgoingMediaItem, 0, len(loaded))
	for _, la := range loaded {
		items = append(items, OutgoingMediaItem{
			Data:          la.data,
			Filename:      la.artifact.OriginalName,
			MIME:          la.artifact.MimeType,
			SourceOrdinal: la.ordinal,
		})
	}
	if richMode == config.TelegramRichMessagesShadow {
		if shadowPlan, ok, reason := b.planGeneratedRichDelivery(ctx, path, responseText, items, len(artifactIDs)); ok {
			shadowOutcome = richMetricShadowNative
			shadowFallbackReason = richMetricFallbackNone
			if shadowPlan.cleanedTextValid {
				historyText = shadowPlan.cleanedText
			}
		} else {
			shadowFallbackReason = reason
			if shadowPlan.cleanedTextValid {
				historyText = shadowPlan.cleanedText
			}
		}
	}
	metricPath = richMetricPathLegacyMedia
	metricFallbackReason = richMetricFallbackMediaIneligible

	// Prefer the fully preflighted V2 gallery plan. It may contain one rich
	// gallery, additional block-packed rich text parts, and high-resolution
	// Document sidecars. A confirmed format rejection can execute only the
	// immutable legacy suffix embedded in that plan.
	deliveryConfirmed := false
	if richMode == config.TelegramRichMessagesSend {
		planned, ok, ineligibleReason := b.planGeneratedRichDelivery(ctx, path, responseText, items, len(artifactIDs))
		if planned.cleanedTextValid {
			historyText = planned.cleanedText
		}
		if planned.layoutMode == generatedMediaLayoutInvalid {
			logger.Warn("ignored invalid generated-media layout", "reason", planned.layoutReason)
		}
		if ok {
			metricPath = richMetricPathNative
			metricFallbackReason = richMetricFallbackNone
			nativeAttachmentCount = planned.nativeAttachments
			nativeAttachmentBytes = planned.nativeBytes
			delivery := b.executeDeliveryPlan(ctx, planned.plan, path.ledgerContext()...)
			result.attempts += delivery.attempts
			result.confirmedIDs = append(result.confirmedIDs, delivery.confirmedIDs...)
			result.primaryMessageID = delivery.firstMsgID
			result.deliveryID = delivery.deliveryID
			metricPath = delivery.metricPath
			metricFallbackReason = delivery.fallbackReason
			if delivery.outcome != richDeliveryConfirmed {
				result.outcome = delivery.outcome
				result.err = delivery.err
				return result
			}
			deliveryConfirmed = true
		} else {
			metricFallbackReason = ineligibleReason
			if len(planned.legacyFallback) == 0 {
				result.outcome = richDeliveryRejected
				result.err = fmt.Errorf("rich generated-media planner produced no safe fallback")
				return result
			}
			fallbackDelivery := b.executeDeliveryPlan(ctx, deliveryPlan{Operations: planned.legacyFallback}, path.ledgerContext()...)
			result.attempts += fallbackDelivery.attempts
			result.confirmedIDs = append(result.confirmedIDs, fallbackDelivery.confirmedIDs...)
			result.primaryMessageID = fallbackDelivery.firstMsgID
			result.deliveryID = fallbackDelivery.deliveryID
			if fallbackDelivery.outcome != richDeliveryConfirmed {
				result.outcome = fallbackDelivery.outcome
				result.err = fallbackDelivery.err
				return result
			}
			deliveryConfirmed = true
		}
	}

	if !deliveryConfirmed {
		result.attempts++
		outgoing := OutgoingMedia{
			ConversationID: path.convID,
			ThreadRoot:     path.threadRoot,
			ReplyTo:        path.replyTo,
			Caption:        caption,
			Items:          items,
		}
		// Off/shadow must keep the established compatibility envelope: Telegram's
		// SendMedia may split mixed document/photo sets into multiple API calls and
		// chunk albums above ten. The strict one-call method is reserved for the
		// fully planned rich-send path, where each batch is already homogeneous and
		// represented by its own durable ledger operation.
		mediaID, err := b.transport.SendMedia(ctx, outgoing)
		sent := persistentSendResult{MessageIDs: []string{mediaID}}
		if err != nil {
			logger.Error("failed to send generated media", "error", err)
			ids, idErr := normalizeOptionalPersistentIDs(sent.MessageIDs, map[string]struct{}{})
			if idErr != nil {
				result.outcome = richDeliveryUnknown
				result.err = fmt.Errorf("send generated media returned invalid ids with an error: %w", idErr)
				return result
			}
			result.confirmedIDs = append(result.confirmedIDs, ids...)
			if len(ids) > 0 {
				result.primaryMessageID = ids[0]
			}
			result.outcome = outcomeAfterFailure(len(result.confirmedIDs), err)
			result.err = fmt.Errorf("send generated media: %w", err)
			return result
		}
		ids, idErr := normalizePersistentIDs(sent.MessageIDs, map[string]struct{}{})
		if idErr != nil {
			result.outcome = richDeliveryUnknown
			result.err = fmt.Errorf("send generated media: %w", idErr)
			return result
		}
		result.confirmedIDs = append(result.confirmedIDs, ids...)
		result.primaryMessageID = ids[0]

		// Send any remaining text as follow-up messages (no reply-to: the
		// media already anchored to the user's message).
		for followUpIndex, followUp := range followUps {
			if strings.TrimSpace(followUp) == "" {
				continue
			}
			followUpResult := b.sendGeneratedTextDelivery(ctx, path, "", followUp, logger)
			result.attempts += followUpResult.attempts
			result.confirmedIDs = append(result.confirmedIDs, followUpResult.confirmedIDs...)
			if followUpResult.outcome != richDeliveryConfirmed {
				switch followUpResult.outcome {
				case richDeliveryRejected, richDeliveryPartialRejected:
					result.outcome = richDeliveryPartialRejected
				default:
					result.outcome = richDeliveryPartialUnknown
				}
				result.err = fmt.Errorf("send generated media follow-up %d: %w", followUpIndex, followUpResult.err)
				return result
			}
		}
	}

	result.outcome = richDeliveryConfirmed
	span := trace.SpanFromContext(ctx)
	// 2. Compact history markers + marker-free assistant prose. Layout protocol
	// is deliberately turn-local and must never be fed into the next prompt.
	historyContent := buildAssistantHistoryContent(loaded, historyText)
	loadedArtifactIDs := make([]int64, 0, len(loaded))
	for _, artifact := range loaded {
		loadedArtifactIDs = append(loadedArtifactIDs, artifact.artifact.ID)
	}
	if b.persistConfirmedAssistantReply(userID, span, historyContent, path.convID, historyThreadRoot,
		result.deliveryID, result.confirmedIDs, loadedArtifactIDs, logger) {
		result.persisted = true
	}
	return result
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
	deliveryText string,
	historyText string,
	logger *slog.Logger,
) generatedDeliveryResult {
	start := time.Now()
	delivery := b.sendGeneratedTextDelivery(ctx, path, path.replyTo, deliveryText, logger)
	result := generatedDeliveryResult{
		outcome:          delivery.outcome,
		duration:         time.Since(start),
		attempts:         delivery.attempts,
		primaryMessageID: delivery.firstMsgID,
		confirmedIDs:     append([]string(nil), delivery.confirmedIDs...),
		deliveryID:       delivery.deliveryID,
		err:              delivery.err,
	}
	if delivery.outcome != richDeliveryConfirmed {
		return result
	}
	span := trace.SpanFromContext(ctx)
	if b.persistConfirmedAssistantReply(path.userID, span, historyText, path.convID, historyThreadRoot,
		result.deliveryID, result.confirmedIDs, nil, logger) {
		result.persisted = true
	}
	return result
}

func (b *Bot) sendGeneratedTextDelivery(ctx context.Context, path *responsePath, replyTo, text string, logger *slog.Logger) richDeliveryResult {
	if path.effectiveRichMode() == config.TelegramRichMessagesSend {
		return b.sendRichRendered(ctx, path.convID, path.threadRoot, replyTo, text, logger, path.ledgerContext()...)
	}
	return b.sendRenderedDelivery(ctx, path.convID, path.threadRoot, replyTo, text, logger)
}

// loadedArtifact pairs an Artifact row with its on-disk bytes.
type loadedArtifact struct {
	artifact *storage.Artifact
	data     []byte
	ordinal  int
}

// loadArtifactBytes resolves each artifact by ID (user-isolated) and reads
// its file bytes. Artifacts that fail to load are skipped with a warning —
// the caller decides what to do with an empty result.
func (b *Bot) loadArtifactBytes(ctx context.Context, userID storage.ScopeID, ids []int64, logger *slog.Logger) []loadedArtifact {
	out := make([]loadedArtifact, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for slot, id := range ids {
		if id <= 0 {
			logger.Warn("ignoring invalid generated artifact id", "artifact_id", id)
			continue
		}
		if _, exists := seen[id]; exists {
			logger.Warn("ignoring duplicate generated artifact id", "artifact_id", id)
			continue
		}
		seen[id] = struct{}{}
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
		out = append(out, loadedArtifact{artifact: art, data: data, ordinal: slot + 1})
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

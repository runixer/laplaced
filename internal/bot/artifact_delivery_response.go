package bot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/artifactdelivery"
	"github.com/runixer/laplaced/internal/storage"
)

func normalizeGeneratedArtifacts(generated []artifactdelivery.Generated, legacyIDs []int64) ([]artifactdelivery.Generated, error) {
	if len(generated) == 0 {
		generated = make([]artifactdelivery.Generated, 0, len(legacyIDs))
		for _, id := range legacyIDs {
			generated = append(generated, artifactdelivery.Generated{ArtifactID: id, Mode: artifactdelivery.ModePreview})
		}
	}
	for i := range generated {
		if generated[i].ArtifactID <= 0 {
			return nil, fmt.Errorf("generated artifact %d has invalid id", i)
		}
		mode, err := artifactdelivery.ParseGeneratedMode(string(generated[i].Mode))
		if err != nil {
			return nil, fmt.Errorf("generated artifact %d: %w", i, err)
		}
		generated[i].Mode = mode
	}
	return append([]artifactdelivery.Generated(nil), generated...), nil
}

func normalizeSelectedArtifacts(selected []artifactdelivery.Selected) ([]artifactdelivery.Selected, error) {
	seen := make(map[int64]struct{}, len(selected))
	for i := range selected {
		if selected[i].ArtifactID <= 0 {
			return nil, fmt.Errorf("selected artifact %d has invalid id", i)
		}
		mode, err := artifactdelivery.ParseStoredMode(string(selected[i].Mode))
		if err != nil {
			return nil, fmt.Errorf("selected artifact %d: %w", i, err)
		}
		selected[i].Mode = mode
		if _, duplicate := seen[selected[i].ArtifactID]; duplicate {
			return nil, fmt.Errorf("selected artifact id %d is duplicated", selected[i].ArtifactID)
		}
		seen[selected[i].ArtifactID] = struct{}{}
	}
	return append([]artifactdelivery.Selected(nil), selected...), nil
}

func (b *Bot) loadArtifactDeliveryStrict(
	ctx context.Context,
	userID storage.ScopeID,
	artifactID int64,
	mode artifactdelivery.Mode,
	source string,
	ordinal int,
) (loadedArtifactDelivery, error) {
	if b.artifactRepo == nil || b.fileStorage == nil {
		return loadedArtifactDelivery{}, fmt.Errorf("artifact repository or blob storage is not configured")
	}
	artifact, err := b.artifactRepo.GetArtifact(userID, artifactID)
	if err != nil {
		return loadedArtifactDelivery{}, fmt.Errorf("load artifact %d metadata: %w", artifactID, err)
	}
	if artifact == nil || artifact.UserID != userID {
		return loadedArtifactDelivery{}, fmt.Errorf("artifact %d is unavailable for this user", artifactID)
	}
	// Neither Telegram Photo nor hosted-Bot-API Document upload can carry a
	// blob above this bound. Reject from trusted metadata before allocating or
	// reading it; an unknown/legacy zero size is verified after the read.
	if artifact.FileSize < 0 {
		return loadedArtifactDelivery{}, fmt.Errorf("artifact %d has invalid negative size %d", artifactID, artifact.FileSize)
	}
	if artifact.FileSize > telegramDocumentMaxBytes {
		return loadedArtifactDelivery{}, fmt.Errorf("artifact %d has %d bytes, Telegram upload limit is %d",
			artifactID, artifact.FileSize, telegramDocumentMaxBytes)
	}
	data, err := b.fileStorage.ReadFile(ctx, artifact.FilePath)
	if err != nil {
		return loadedArtifactDelivery{}, fmt.Errorf("read artifact %d: %w", artifactID, err)
	}
	if int64(len(data)) != artifact.FileSize && artifact.FileSize > 0 {
		return loadedArtifactDelivery{}, fmt.Errorf("artifact %d size mismatch", artifactID)
	}
	return loadedArtifactDelivery{
		loadedArtifact: loadedArtifact{artifact: artifact, data: data, ordinal: ordinal},
		mode:           mode,
		source:         source,
	}, nil
}

func (b *Bot) loadArtifactDeliveryInputs(
	ctx context.Context,
	userID storage.ScopeID,
	generated []artifactdelivery.Generated,
	selected []artifactdelivery.Selected,
) ([]loadedArtifactDelivery, []loadedArtifactDelivery, error) {
	loadedGenerated := make([]loadedArtifactDelivery, 0, len(generated))
	loadedSelected := make([]loadedArtifactDelivery, 0, len(selected))
	seen := make(map[int64]string, len(generated)+len(selected))
	for i, item := range generated {
		seen[item.ArtifactID] = "generated"
		loaded, err := b.loadArtifactDeliveryStrict(ctx, userID, item.ArtifactID, item.Mode, artifactReferenceSourceGenerated, i+1)
		if err != nil {
			return nil, nil, err
		}
		loadedGenerated = append(loadedGenerated, loaded)
	}
	for i, item := range selected {
		if prior, duplicate := seen[item.ArtifactID]; duplicate {
			return nil, nil, fmt.Errorf("artifact %d selected more than once (%s and stored)", item.ArtifactID, prior)
		}
		seen[item.ArtifactID] = "stored"
		loaded, err := b.loadArtifactDeliveryStrict(ctx, userID, item.ArtifactID, item.Mode, artifactReferenceSourceStored, i+1)
		if err != nil {
			return nil, nil, err
		}
		loadedSelected = append(loadedSelected, loaded)
	}
	return loadedGenerated, loadedSelected, nil
}

func storageArtifactReferences(refs []plannedArtifactReference) ([]storage.OutboundArtifactReference, error) {
	result := make([]storage.OutboundArtifactReference, 0, len(refs))
	for i, ref := range refs {
		var source storage.ArtifactReferenceSource
		switch ref.source {
		case artifactReferenceSourceGenerated:
			source = storage.ArtifactReferenceSourceGenerated
		case artifactReferenceSourceStored:
			source = storage.ArtifactReferenceSourceStored
		default:
			return nil, fmt.Errorf("artifact reference %d has unsupported source %q", i, ref.source)
		}
		result = append(result, storage.OutboundArtifactReference{
			ArtifactID: ref.artifactID,
			Ordinal:    i,
			Mode:       ref.mode,
			Source:     source,
		})
	}
	return result, nil
}

func (b *Bot) persistConfirmedArtifactReply(
	userID storage.ScopeID,
	span trace.Span,
	content, convID string,
	threadRoot *string,
	deliveryID int64,
	plan artifactResponsePlan,
	logger *slog.Logger,
) bool {
	if deliveryID <= 0 || b.artifactRefRepo == nil {
		logger.Error("confirmed artifact delivery has no provenance repository", "delivery_id", deliveryID)
		return false
	}
	references, err := storageArtifactReferences(plan.references)
	if err != nil {
		logger.Error("failed to prepare artifact references", "error", err)
		return false
	}
	message := b.assistantReplyMessage(userID, span, content, convID, threadRoot, logger)
	_, err = b.artifactRefRepo.PersistOutboundDeliveryReplyWithArtifacts(userID, deliveryID, message,
		storage.PersistOutboundArtifacts{
			OwnedArtifactIDs: append([]int64(nil), plan.ownedArtifactIDs...),
			References:       references,
		})
	if err != nil {
		logger.Error("failed to atomically persist confirmed artifact delivery", "delivery_id", deliveryID, "error", err)
		return false
	}
	return true
}

// sendResponseWithArtifacts is the private-chat V1 path shared by newly
// generated outputs and stored artifacts selected by send_artifacts. Every
// persistent operation is preplanned and recorded in one delivery ledger.
func (b *Bot) sendResponseWithArtifacts(
	ctx context.Context,
	path *responsePath,
	historyThreadRoot *string,
	responseText string,
	generated []artifactdelivery.Generated,
	selected []artifactdelivery.Selected,
	legacyGeneratedIDs []int64,
	logger *slog.Logger,
) (result generatedDeliveryResult) {
	started := time.Now()
	defer func() { result.duration = time.Since(started) }()
	metricPath := richMetricPathPreflightRejected
	metricFallbackReason := richMetricFallbackMediaUnavailable
	nativeAttachmentCount, nativeAttachmentBytes := 0, 0
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
	if !path.artifactDeliveryEligible() {
		result.outcome = richDeliveryRejected
		result.err = fmt.Errorf("explicit artifact delivery is available only in private rich-send turns")
		return result
	}
	// A deterministic local rejection is known to have sent no artifact
	// request, so it is safe (and much less confusing than a silent turn) to
	// emit one bounded generic-error notification. Once any persistent
	// delivery operation was attempted, its remote outcome owns the result and
	// must never be followed by an implicit retry or replacement.
	defer func() {
		if result.outcome == richDeliveryRejected && result.attempts == 0 {
			result.attempts += b.sendGenericError(ctx, path.convID, path.threadRoot, logger)
		}
	}()
	generated, err := normalizeGeneratedArtifacts(generated, legacyGeneratedIDs)
	if err != nil {
		metricFallbackReason = richMetricFallbackHardPreflight
		result.outcome, result.err = richDeliveryRejected, err
		return result
	}
	selected, err = normalizeSelectedArtifacts(selected)
	if err != nil {
		metricFallbackReason = richMetricFallbackHardPreflight
		result.outcome, result.err = richDeliveryRejected, err
		return result
	}
	loadedGenerated, loadedSelected, err := b.loadArtifactDeliveryInputs(ctx, path.userID, generated, selected)
	if err != nil {
		result.outcome = richDeliveryRejected
		result.err = err
		return result
	}
	planned, err := b.buildArtifactResponsePlan(ctx, path, responseText, loadedGenerated, loadedSelected)
	if err != nil {
		metricFallbackReason = richMetricFallbackRenderOrLimit
		result.outcome = richDeliveryRejected
		result.err = err
		return result
	}
	nativeAttachmentCount = planned.nativeAttachments
	nativeAttachmentBytes = planned.nativeBytes
	delivery := b.executeDeliveryPlan(ctx, planned.plan, path.ledgerContext()...)
	if delivery.metricPath != "" {
		metricPath = delivery.metricPath
	}
	if delivery.fallbackReason != "" {
		metricFallbackReason = delivery.fallbackReason
	}
	result.attempts = delivery.attempts
	result.confirmedIDs = append(result.confirmedIDs, delivery.confirmedIDs...)
	result.primaryMessageID = delivery.firstMsgID
	result.deliveryID = delivery.deliveryID
	result.outcome = delivery.outcome
	result.err = delivery.err
	if delivery.outcome != richDeliveryConfirmed {
		return result
	}
	historyContent := buildAssistantHistoryContent(planned.loaded, planned.historyText)
	if b.persistConfirmedArtifactReply(path.userID, trace.SpanFromContext(ctx), historyContent,
		path.convID, historyThreadRoot, delivery.deliveryID, planned, logger) {
		result.persisted = true
	}
	return result
}

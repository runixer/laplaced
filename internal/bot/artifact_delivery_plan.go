package bot

import (
	"context"
	"fmt"
	pathpkg "path"
	"strings"

	"github.com/runixer/laplaced/internal/artifactdelivery"
)

const (
	artifactReferenceSourceGenerated = "generated"
	artifactReferenceSourceStored    = "stored"
)

type loadedArtifactDelivery struct {
	loadedArtifact
	mode   artifactdelivery.Mode
	source string
}

type plannedArtifactReference struct {
	artifactID int64
	mode       artifactdelivery.Mode
	source     string
}

type artifactResponsePlan struct {
	plan               deliveryPlan
	deliveryText       string
	historyText        string
	loaded             []loadedArtifact
	ownedArtifactIDs   []int64
	references         []plannedArtifactReference
	nativeAttachments  int
	nativeBytes        int
	generatedPreviews  int
	generatedOriginals int
}

func outgoingItemForLoaded(value loadedArtifact) OutgoingMediaItem {
	return OutgoingMediaItem{
		Data:          value.data,
		Filename:      safeArtifactFilename(value.artifact.OriginalName, value.ordinal, value.artifact.MimeType),
		MIME:          value.artifact.MimeType,
		SourceOrdinal: value.ordinal,
	}
}

func safeArtifactFilename(original string, fallbackOrdinal int, mediaType string) string {
	name := pathpkg.Base(strings.ReplaceAll(strings.TrimSpace(original), `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		extension := ".bin"
		switch strings.ToLower(strings.TrimSpace(strings.SplitN(mediaType, ";", 2)[0])) {
		case "image/png":
			extension = ".png"
		case "image/jpeg":
			extension = ".jpg"
		case "application/pdf":
			extension = ".pdf"
		case "text/plain":
			extension = ".txt"
		}
		return fmt.Sprintf("attachment-%d%s", max(fallbackOrdinal, 1), extension)
	}
	const maxFilenameRunes = 180
	if runes := []rune(name); len(runes) > maxFilenameRunes {
		extension := pathpkg.Ext(name)
		extRunes := []rune(extension)
		keep := maxFilenameRunes - len(extRunes)
		if keep < 1 {
			keep = maxFilenameRunes
			extension = ""
		}
		name = string(runes[:keep]) + extension
	}
	return name
}

func asPhotoItem(item OutgoingMediaItem) (OutgoingMediaItem, bool) {
	item = normalizedGeneratedPhotoItem(item, max(item.SourceOrdinal-1, 0))
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.MIME)), "image/") || !generatedPhotoCanBePreviewed(item) {
		return OutgoingMediaItem{}, false
	}
	item.WireKind = OutgoingMediaWireKindPhoto
	item.AsDocument = false
	return item, true
}

func asDocumentItem(item OutgoingMediaItem) OutgoingMediaItem {
	item.AsDocument = true
	item.WireKind = OutgoingMediaWireKindDocument
	if strings.TrimSpace(item.Filename) == "" {
		item.Filename = fmt.Sprintf("attachment-%d.bin", max(item.SourceOrdinal, 1))
	}
	if strings.TrimSpace(item.MIME) == "" {
		item.MIME = "application/octet-stream"
	}
	return item
}

func appendPlannedReference(refs []plannedArtifactReference, item loadedArtifactDelivery, mode artifactdelivery.Mode) []plannedArtifactReference {
	return append(refs, plannedArtifactReference{
		artifactID: item.artifact.ID,
		mode:       mode,
		source:     item.source,
	})
}

// materializeArtifactPresentations resolves requested presentation modes into
// exact Telegram wire items before the delivery ledger exists. Generated Photo
// previews are returned separately so only those can participate in the rich
// MEDIA compositor; all originals and stored selections form the ordered tail.
func materializeArtifactPresentations(
	generated []loadedArtifactDelivery,
	selected []loadedArtifactDelivery,
) (previews, tail []OutgoingMediaItem, refs []plannedArtifactReference, previewCount int, generatedOriginals int) {
	// References follow the actual persistent presentation order: every rich
	// preview first, then the explicit file/selection tail. Keeping separate
	// slices avoids interleaving an item's original before a later generated
	// preview merely because both intents came from the same tool result.
	var previewReferences, tailReferences []plannedArtifactReference
	for _, item := range generated {
		raw := outgoingItemForLoaded(item.loadedArtifact)
		extension := pathpkg.Ext(raw.Filename)
		if extension == "" {
			extension = ".bin"
		}
		raw.Filename = fmt.Sprintf("generated-image-%d%s", max(item.ordinal, 1), extension)
		wantsPreview := item.mode == artifactdelivery.ModePreview || item.mode == artifactdelivery.ModePreviewAndOriginal
		wantsOriginal := item.mode == artifactdelivery.ModeOriginal || item.mode == artifactdelivery.ModePreviewAndOriginal
		if wantsPreview {
			previewCount++
			raw.SourceOrdinal = previewCount
			if photo, ok := asPhotoItem(raw); ok {
				previews = append(previews, photo)
				previewReferences = appendPlannedReference(previewReferences, item, artifactdelivery.ModePreview)
			} else {
				// An explicit preview that cannot satisfy Telegram's Photo envelope
				// degrades before network to one original Document, never a duplicate.
				wantsOriginal = true
			}
		}
		if wantsOriginal {
			tail = append(tail, asDocumentItem(raw))
			tailReferences = appendPlannedReference(tailReferences, item, artifactdelivery.ModeOriginal)
			generatedOriginals++
		}
	}

	for _, item := range selected {
		raw := outgoingItemForLoaded(item.loadedArtifact)
		photo, previewable := asPhotoItem(raw)
		mode := item.mode
		if mode == artifactdelivery.ModeAuto {
			if previewable {
				mode = artifactdelivery.ModePreview
			} else {
				mode = artifactdelivery.ModeOriginal
			}
		}
		switch mode {
		case artifactdelivery.ModePreview:
			if previewable {
				tail = append(tail, photo)
				tailReferences = appendPlannedReference(tailReferences, item, artifactdelivery.ModePreview)
			} else {
				tail = append(tail, asDocumentItem(raw))
				tailReferences = appendPlannedReference(tailReferences, item, artifactdelivery.ModeOriginal)
			}
		case artifactdelivery.ModeOriginal:
			tail = append(tail, asDocumentItem(raw))
			tailReferences = appendPlannedReference(tailReferences, item, artifactdelivery.ModeOriginal)
		case artifactdelivery.ModePreviewAndOriginal:
			if previewable {
				tail = append(tail, photo)
				tailReferences = appendPlannedReference(tailReferences, item, artifactdelivery.ModePreview)
			}
			tail = append(tail, asDocumentItem(raw))
			tailReferences = appendPlannedReference(tailReferences, item, artifactdelivery.ModeOriginal)
		}
	}
	refs = make([]plannedArtifactReference, 0, len(previewReferences)+len(tailReferences))
	refs = append(refs, previewReferences...)
	refs = append(refs, tailReferences...)
	return previews, tail, refs, previewCount, generatedOriginals
}

func cloneArtifactTailOperations(operations []deliveryOperation) []deliveryOperation {
	clones := make([]deliveryOperation, 0, len(operations))
	for _, operation := range operations {
		clones = append(clones, withoutReply(operation))
	}
	return clones
}

func orderArtifactReferencesByPrimary(
	primary []deliveryOperation,
	previews []OutgoingMediaItem,
	refs []plannedArtifactReference,
) ([]plannedArtifactReference, error) {
	if len(previews) == 0 {
		return refs, nil
	}
	if len(refs) < len(previews) {
		return nil, fmt.Errorf("artifact reference count %d is smaller than preview count %d", len(refs), len(previews))
	}
	byOrdinal := make(map[int]plannedArtifactReference, len(previews))
	for i, preview := range previews {
		if preview.SourceOrdinal <= 0 {
			return nil, fmt.Errorf("preview %d has no source ordinal", i)
		}
		if _, duplicate := byOrdinal[preview.SourceOrdinal]; duplicate {
			return nil, fmt.Errorf("duplicate preview source ordinal %d", preview.SourceOrdinal)
		}
		byOrdinal[preview.SourceOrdinal] = refs[i]
	}
	ordered := make([]plannedArtifactReference, 0, len(refs))
	seen := make(map[int]struct{}, len(previews))
	appendItems := func(items []OutgoingMediaItem) error {
		for _, item := range items {
			ref, ok := byOrdinal[item.SourceOrdinal]
			if !ok {
				continue
			}
			if _, duplicate := seen[item.SourceOrdinal]; duplicate {
				return fmt.Errorf("preview source ordinal %d appears more than once in primary plan", item.SourceOrdinal)
			}
			seen[item.SourceOrdinal] = struct{}{}
			ordered = append(ordered, ref)
		}
		return nil
	}
	for _, operation := range primary {
		if operation.RichMedia != nil {
			if err := appendItems(operation.RichMedia.Items); err != nil {
				return nil, err
			}
		}
		if operation.Media != nil {
			if err := appendItems(operation.Media.Items); err != nil {
				return nil, err
			}
		}
	}
	if len(seen) != len(previews) {
		return nil, fmt.Errorf("primary plan contains %d of %d generated previews", len(seen), len(previews))
	}
	ordered = append(ordered, refs[len(previews):]...)
	return ordered, nil
}

// appendArtifactTail makes explicit files part of every safe format-rejection
// suffix. The executor skips the remaining primary suffix when it activates a
// rich fallback, so omitting this copy would silently lose originals.
func appendArtifactTail(primary, tail []deliveryOperation) []deliveryOperation {
	if len(tail) == 0 {
		return primary
	}
	for i := range primary {
		if primary[i].Kind == persistentOperationRichText || primary[i].Kind == persistentOperationRichMedia {
			primary[i].formatFallback = append(primary[i].formatFallback, cloneArtifactTailOperations(tail)...)
		}
	}
	return append(primary, tail...)
}

func cleanArtifactProtocolSource(source string, availableOrdinals []int) (delivery, history string, err error) {
	layout, err := parseGeneratedMediaLayout(source, availableOrdinals)
	if err != nil {
		return "", "", err
	}
	delivery, err = generatedMediaDeliverySource(source, layout)
	if err != nil {
		return "", "", err
	}
	return delivery, layout.MarkerFreeSource, nil
}

func (b *Bot) buildArtifactResponsePlan(
	ctx context.Context,
	path *responsePath,
	responseText string,
	generated []loadedArtifactDelivery,
	selected []loadedArtifactDelivery,
) (artifactResponsePlan, error) {
	if path == nil {
		return artifactResponsePlan{}, fmt.Errorf("artifact delivery path is nil")
	}
	previews, tailItems, refs, previewRefCount, generatedOriginals := materializeArtifactPresentations(generated, selected)
	availableOrdinals := make([]int, 0, len(previews))
	for _, item := range previews {
		availableOrdinals = append(availableOrdinals, item.SourceOrdinal)
	}
	deliveryText, historyText, err := cleanArtifactProtocolSource(responseText, availableOrdinals)
	if err != nil {
		return artifactResponsePlan{}, fmt.Errorf("resolve artifact delivery protocol: %w", err)
	}

	tail := generatedMediaOperationsStable(path, tailItems, 0)
	for i := range tail {
		tail[i] = withoutReply(tail[i])
	}
	var primary []deliveryOperation
	if len(previews) > 0 {
		planned, native, _ := b.planGeneratedRichDelivery(ctx, path, responseText, previews, previewRefCount)
		if planned.cleanedTextValid {
			historyText = planned.cleanedText
		}
		if native {
			primary = planned.plan.Operations
		} else {
			primary = planned.legacyFallback
		}
		if len(primary) == 0 {
			return artifactResponsePlan{}, fmt.Errorf("generated preview planner produced no safe operations")
		}
	} else if strings.TrimSpace(deliveryText) != "" {
		renderer, ok := b.renderer.(*TelegramRenderer)
		if !ok {
			return artifactResponsePlan{}, fmt.Errorf("artifact delivery requires Telegram rich renderer")
		}
		preflight, preflightErr := preflightRichDelivery(ctx, deliveryText, renderer)
		if preflightErr != nil {
			return artifactResponsePlan{}, fmt.Errorf("artifact text preflight: %w", preflightErr)
		}
		textPlan, planErr := richTextDeliveryPlan(path.convID, path.threadRoot, path.replyTo, preflight)
		if planErr != nil {
			return artifactResponsePlan{}, fmt.Errorf("artifact text plan: %w", planErr)
		}
		primary = textPlan.Operations
	}
	if len(primary) == 0 && len(tail) == 0 {
		return artifactResponsePlan{}, fmt.Errorf("artifact response has no persistent operations")
	}
	refs, err = orderArtifactReferencesByPrimary(primary, previews, refs)
	if err != nil {
		return artifactResponsePlan{}, fmt.Errorf("order artifact response references: %w", err)
	}
	operations := appendArtifactTail(primary, tail)
	if len(primary) == 0 {
		setGeneratedOperationReply(&operations[0], path.replyTo)
	}
	plan := deliveryPlan{Operations: operations}
	if err := plan.validate(); err != nil {
		return artifactResponsePlan{}, fmt.Errorf("validate artifact response plan: %w", err)
	}

	result := artifactResponsePlan{
		plan:               plan,
		deliveryText:       deliveryText,
		historyText:        historyText,
		references:         refs,
		generatedPreviews:  len(previews),
		generatedOriginals: generatedOriginals,
	}
	loadedByID := make(map[int64]loadedArtifact, len(generated)+len(selected))
	for _, item := range append(append([]loadedArtifactDelivery(nil), generated...), selected...) {
		loadedByID[item.artifact.ID] = item.loadedArtifact
	}
	seenLoaded := make(map[int64]struct{}, len(loadedByID))
	for _, reference := range result.references {
		if _, seen := seenLoaded[reference.artifactID]; seen {
			continue
		}
		loaded, ok := loadedByID[reference.artifactID]
		if !ok {
			return artifactResponsePlan{}, fmt.Errorf("artifact reference %d has no loaded source", reference.artifactID)
		}
		seenLoaded[reference.artifactID] = struct{}{}
		result.loaded = append(result.loaded, loaded)
	}
	seenOwned := make(map[int64]struct{}, len(generated))
	for _, item := range generated {
		if item.artifact.MessageID == 0 {
			if _, duplicate := seenOwned[item.artifact.ID]; duplicate {
				continue
			}
			seenOwned[item.artifact.ID] = struct{}{}
			result.ownedArtifactIDs = append(result.ownedArtifactIDs, item.artifact.ID)
		}
	}
	for _, item := range previews {
		result.nativeAttachments++
		result.nativeBytes += len(item.Data)
	}
	return result, nil
}

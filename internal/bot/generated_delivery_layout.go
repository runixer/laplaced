package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/runixer/laplaced/internal/markdown"
)

type generatedLayoutAtomKind uint8

const (
	generatedLayoutAtomText generatedLayoutAtomKind = iota
	generatedLayoutAtomMedia
)

// generatedLayoutAtom is an application-owned composition atom. Text has
// already passed the Rich HTML allowlist; media indexes point only into the
// user-isolated generated-artifact slice.
type generatedLayoutAtom struct {
	kind        generatedLayoutAtomKind
	source      string
	html        string
	stats       markdown.RichStats
	itemIndexes []int
}

type generatedPreparedMedia struct {
	preview        OutgoingMediaItem
	original       OutgoingMediaItem
	highResolution bool
}

type generatedNativeLayoutPart struct {
	atoms           []generatedLayoutAtom
	htmlParts       []string
	mediaGroupSizes []int
	previewItems    []OutgoingMediaItem
	stats           markdown.RichStats
}

func generatedMediaDeliverySource(source string, layout generatedMediaLayout) (string, error) {
	mediaRanges := make([]markdown.RichSourceRange, 0, len(layout.ProtocolLines))
	for _, line := range layout.ProtocolLines {
		if line.Kind == generatedMediaProtocolMedia {
			mediaRanges = append(mediaRanges, line.SourceRange)
		}
	}
	return stripRichSourceRanges(source, mediaRanges)
}

func generatedLayoutAtomGroups(source string, layout generatedMediaLayout) ([][]generatedLayoutAtom, error) {
	if layout.Mode != generatedMediaLayoutDirected {
		return nil, fmt.Errorf("generated media layout is %q, not directed", layout.Mode)
	}
	placements := make(map[int]generatedMediaPlacement, len(layout.Placements))
	for _, placement := range layout.Placements {
		placements[placement.SourceRange.Start] = placement
	}

	groups := make([][]generatedLayoutAtom, 1)
	cursor := 0
	appendText := func(value string) error {
		if strings.TrimSpace(value) == "" {
			return nil
		}
		parts, err := renderRichParts(value)
		if err != nil {
			return err
		}
		for _, part := range parts {
			groups[len(groups)-1] = append(groups[len(groups)-1], generatedLayoutAtom{
				kind:   generatedLayoutAtomText,
				source: part.source,
				html:   part.html,
				stats:  part.stats,
			})
		}
		return nil
	}

	for i, line := range layout.ProtocolLines {
		if line.SourceRange.Start < cursor || line.SourceRange.End < line.SourceRange.Start || line.SourceRange.End > len(source) {
			return nil, fmt.Errorf("generated media protocol line %d has invalid range [%d,%d)",
				i, line.SourceRange.Start, line.SourceRange.End)
		}
		if err := appendText(source[cursor:line.SourceRange.Start]); err != nil {
			return nil, fmt.Errorf("render generated layout text before protocol line %d: %w", i, err)
		}
		switch line.Kind {
		case generatedMediaProtocolSplit:
			if len(groups[len(groups)-1]) > 0 {
				groups = append(groups, nil)
			}
		case generatedMediaProtocolMedia:
			placement, ok := placements[line.SourceRange.Start]
			if !ok || placement.SourceRange != line.SourceRange || len(placement.ItemIndexes) == 0 {
				return nil, fmt.Errorf("directed generated layout has no placement for protocol line %d", i)
			}
			groups[len(groups)-1] = append(groups[len(groups)-1], generatedLayoutAtom{
				kind:        generatedLayoutAtomMedia,
				itemIndexes: append([]int(nil), placement.ItemIndexes...),
			})
		default:
			return nil, fmt.Errorf("unsupported generated media protocol line kind %q", line.Kind)
		}
		cursor = line.SourceRange.End
	}
	if err := appendText(source[cursor:]); err != nil {
		return nil, fmt.Errorf("render generated layout trailing text: %w", err)
	}
	if len(groups) > 0 && len(groups[len(groups)-1]) == 0 {
		groups = groups[:len(groups)-1]
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("directed generated layout produced no content")
	}
	return groups, nil
}

func prepareGeneratedNativeMedia(items []OutgoingMediaItem, threshold int) ([]generatedPreparedMedia, bool) {
	prepared := make([]generatedPreparedMedia, len(items))
	for i, raw := range items {
		if len(raw.Data) == 0 || raw.AsDocument || !strings.HasPrefix(strings.ToLower(raw.MIME), "image/") {
			return nil, false
		}
		preview := normalizedGeneratedPhotoItem(raw, i)
		if !generatedPhotoCanBePreviewed(preview) {
			return nil, false
		}
		original := preview
		highResolution := threshold > 0 && len(raw.Data) > threshold
		if highResolution {
			original.AsDocument = true
		}
		prepared[i] = generatedPreparedMedia{
			preview:        preview,
			original:       original,
			highResolution: highResolution,
		}
	}
	return prepared, true
}

func generatedMediaAtomStats(count int) markdown.RichStats {
	stats := markdown.RichStats{Blocks: count, MaxDepth: 1}
	if count > 1 {
		stats.Blocks++ // collage/slideshow container
		stats.MaxDepth = 2
	}
	return stats
}

func cloneGeneratedNativeLayoutPart(part generatedNativeLayoutPart) generatedNativeLayoutPart {
	part.atoms = append([]generatedLayoutAtom(nil), part.atoms...)
	part.htmlParts = append([]string(nil), part.htmlParts...)
	part.mediaGroupSizes = append([]int(nil), part.mediaGroupSizes...)
	part.previewItems = append([]OutgoingMediaItem(nil), part.previewItems...)
	return part
}

func appendGeneratedLayoutAtom(part generatedNativeLayoutPart, atom generatedLayoutAtom, prepared []generatedPreparedMedia) (generatedNativeLayoutPart, error) {
	part = cloneGeneratedNativeLayoutPart(part)
	if len(part.htmlParts) == 0 {
		part.htmlParts = []string{""}
	}
	part.atoms = append(part.atoms, atom)
	switch atom.kind {
	case generatedLayoutAtomText:
		last := len(part.htmlParts) - 1
		part.htmlParts[last] += atom.html
		mergeBotRichStats(&part.stats, atom.stats)
	case generatedLayoutAtomMedia:
		if len(atom.itemIndexes) == 0 || len(atom.itemIndexes) > generatedRichGalleryMax {
			return generatedNativeLayoutPart{}, fmt.Errorf("generated media atom has %d items", len(atom.itemIndexes))
		}
		for _, itemIndex := range atom.itemIndexes {
			if itemIndex < 0 || itemIndex >= len(prepared) {
				return generatedNativeLayoutPart{}, fmt.Errorf("generated media atom item index %d is out of range", itemIndex)
			}
			part.previewItems = append(part.previewItems, prepared[itemIndex].preview)
		}
		part.mediaGroupSizes = append(part.mediaGroupSizes, len(atom.itemIndexes))
		part.htmlParts = append(part.htmlParts, "")
		mergeBotRichStats(&part.stats, generatedMediaAtomStats(len(atom.itemIndexes)))
	default:
		return generatedNativeLayoutPart{}, fmt.Errorf("unsupported generated layout atom kind %d", atom.kind)
	}
	return part, nil
}

func generatedNativeLayoutPartWithinLimits(part generatedNativeLayoutPart) bool {
	if part.stats.Characters > richMessageSafeCharacterLimit ||
		part.stats.Blocks > richMessageSafeBlockLimit ||
		part.stats.MaxDepth > richMessageMaxDepth ||
		part.stats.MaxTableColumns > richMessageMaxTableColumns {
		return false
	}
	if len(part.mediaGroupSizes) == 0 {
		return len(part.htmlParts) == 1 && richPartWithinLimits(part.htmlParts[0], part.stats)
	}
	composition, err := composeOutgoingRichMedia(OutgoingRichMedia{
		HTMLParts:       part.htmlParts,
		MediaGroupSizes: part.mediaGroupSizes,
		Items:           part.previewItems,
	})
	return err == nil && strings.TrimSpace(composition.html) != "" && len(composition.html) <= richMessageMaxRenderedBytes
}

func packGeneratedNativeLayout(groups [][]generatedLayoutAtom, prepared []generatedPreparedMedia) ([]generatedNativeLayoutPart, error) {
	parts := make([]generatedNativeLayoutPart, 0, min(len(groups), richMessageMaxParts))
	flush := func(part *generatedNativeLayoutPart) error {
		if len(part.atoms) == 0 {
			return nil
		}
		if !generatedNativeLayoutPartWithinLimits(*part) {
			return fmt.Errorf("generated rich layout part exceeds structural or wire limits")
		}
		parts = append(parts, cloneGeneratedNativeLayoutPart(*part))
		if len(parts) > richMessageMaxParts {
			return fmt.Errorf("%w: more than %d generated layout parts", errRichPartFanout, richMessageMaxParts)
		}
		*part = generatedNativeLayoutPart{}
		return nil
	}

	var current generatedNativeLayoutPart
	for groupIndex, atoms := range groups {
		for atomIndex, atom := range atoms {
			candidate, err := appendGeneratedLayoutAtom(current, atom, prepared)
			if err != nil {
				return nil, fmt.Errorf("append generated layout atom %d/%d: %w", groupIndex, atomIndex, err)
			}
			if len(current.atoms) > 0 && !generatedNativeLayoutPartWithinLimits(candidate) {
				if err := flush(&current); err != nil {
					return nil, fmt.Errorf("pack generated layout before atom %d/%d: %w", groupIndex, atomIndex, err)
				}
				candidate, err = appendGeneratedLayoutAtom(current, atom, prepared)
				if err != nil {
					return nil, fmt.Errorf("append generated layout atom %d/%d after split: %w", groupIndex, atomIndex, err)
				}
			}
			if !generatedNativeLayoutPartWithinLimits(candidate) {
				return nil, fmt.Errorf("atomic generated layout atom %d/%d exceeds structural or wire limits", groupIndex, atomIndex)
			}
			current = candidate
		}
		if err := flush(&current); err != nil {
			return nil, fmt.Errorf("hard split after generated layout group %d: %w", groupIndex, err)
		}
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("generated rich layout produced no parts")
	}
	return parts, nil
}

func generatedMediaOperationsStable(path *responsePath, items []OutgoingMediaItem, threshold int) []deliveryOperation {
	var operations []deliveryOperation
	var batch []OutgoingMediaItem
	batchDocument := false
	flush := func() {
		if len(batch) == 0 {
			return
		}
		media := &OutgoingMedia{
			ConversationID: path.convID,
			ThreadRoot:     path.threadRoot,
			Items:          append([]OutgoingMediaItem(nil), batch...),
		}
		operations = append(operations, deliveryOperation{Kind: persistentOperationMedia, Media: media})
		batch = nil
	}
	for i, raw := range items {
		item := normalizedGeneratedPhotoItem(raw, i)
		asDocument := raw.AsDocument ||
			(threshold > 0 && len(item.Data) > threshold) ||
			!strings.HasPrefix(strings.ToLower(item.MIME), "image/") ||
			!generatedPhotoCanBePreviewed(item)
		item.AsDocument = asDocument
		if len(batch) > 0 && (asDocument != batchDocument || len(batch) == 10) {
			flush()
		}
		if len(batch) == 0 {
			batchDocument = asDocument
		}
		batch = append(batch, item)
	}
	flush()
	return operations
}

func generatedFallbackForAtoms(
	ctx context.Context,
	path *responsePath,
	renderer *TelegramRenderer,
	atoms []generatedLayoutAtom,
	items []OutgoingMediaItem,
	threshold int,
) ([]deliveryOperation, error) {
	var operations []deliveryOperation
	var textSource strings.Builder
	flushText := func() error {
		if strings.TrimSpace(textSource.String()) == "" {
			textSource.Reset()
			return nil
		}
		chunks, err := renderer.renderSafeRichFallbackPart(ctx, textSource.String())
		if err != nil {
			return err
		}
		operations = append(operations, generatedLegacyTextOperations(path.convID, path.threadRoot, chunks)...)
		textSource.Reset()
		return nil
	}
	for atomIndex, atom := range atoms {
		switch atom.kind {
		case generatedLayoutAtomText:
			textSource.WriteString(atom.source)
		case generatedLayoutAtomMedia:
			if err := flushText(); err != nil {
				return nil, fmt.Errorf("render directed fallback text before atom %d: %w", atomIndex, err)
			}
			mediaItems := make([]OutgoingMediaItem, 0, len(atom.itemIndexes))
			for _, itemIndex := range atom.itemIndexes {
				if itemIndex < 0 || itemIndex >= len(items) {
					return nil, fmt.Errorf("directed fallback item index %d is out of range", itemIndex)
				}
				mediaItems = append(mediaItems, items[itemIndex])
			}
			operations = append(operations, generatedMediaOperationsStable(path, mediaItems, threshold)...)
		default:
			return nil, fmt.Errorf("unsupported generated fallback atom kind %d", atom.kind)
		}
	}
	if err := flushText(); err != nil {
		return nil, fmt.Errorf("render directed fallback trailing text: %w", err)
	}
	return operations, nil
}

func setGeneratedOperationReply(op *deliveryOperation, replyTo string) {
	if op == nil {
		return
	}
	switch {
	case op.Text != nil:
		op.Text.ReplyTo = replyTo
	case op.Media != nil:
		op.Media.ReplyTo = replyTo
	case op.RichMedia != nil:
		op.RichMedia.ReplyTo = replyTo
	}
}

func cloneGeneratedFallbackOperation(op deliveryOperation) deliveryOperation {
	clone := withoutReply(op)
	switch {
	case op.Text != nil:
		clone.Text.ReplyTo = op.Text.ReplyTo
	case op.Media != nil:
		clone.Media.ReplyTo = op.Media.ReplyTo
	case op.RichMedia != nil:
		clone.RichMedia.ReplyTo = op.RichMedia.ReplyTo
	}
	// Directed fallbacks are validated legacy leaves, so retaining a nested
	// fallback would violate the delivery-plan union even if a future caller
	// accidentally supplied one.
	clone.formatFallback = nil
	return clone
}

func generatedDirectedFallbackByPart(
	ctx context.Context,
	path *responsePath,
	renderer *TelegramRenderer,
	parts []generatedNativeLayoutPart,
	items []OutgoingMediaItem,
	threshold int,
) ([][]deliveryOperation, error) {
	perPart := make([][]deliveryOperation, len(parts))
	for i, part := range parts {
		operations, err := generatedFallbackForAtoms(ctx, path, renderer, part.atoms, items, threshold)
		if err != nil {
			return nil, fmt.Errorf("build directed fallback part %d: %w", i, err)
		}
		perPart[i] = operations
	}
	suffixes := make([][]deliveryOperation, len(parts))
	for i := range parts {
		var suffix []deliveryOperation
		for j := i; j < len(perPart); j++ {
			for _, op := range perPart[j] {
				suffix = append(suffix, cloneGeneratedFallbackOperation(op))
			}
		}
		if len(suffix) == 0 || len(suffix) > richMessageMaxFallbackChunks {
			return nil, fmt.Errorf("directed fallback suffix %d has %d operations, limit is %d",
				i, len(suffix), richMessageMaxFallbackChunks)
		}
		setGeneratedOperationReply(&suffix[0], path.replyTo)
		suffixes[i] = suffix
	}
	return suffixes, nil
}

func generatedDirectedFullFallback(
	ctx context.Context,
	path *responsePath,
	renderer *TelegramRenderer,
	groups [][]generatedLayoutAtom,
	items []OutgoingMediaItem,
	threshold int,
) ([]deliveryOperation, error) {
	var operations []deliveryOperation
	for i, atoms := range groups {
		part, err := generatedFallbackForAtoms(ctx, path, renderer, atoms, items, threshold)
		if err != nil {
			return nil, fmt.Errorf("build directed full fallback group %d: %w", i, err)
		}
		operations = append(operations, part...)
	}
	if len(operations) == 0 || len(operations) > richMessageMaxFallbackChunks {
		return nil, fmt.Errorf("directed full fallback has %d operations, limit is %d",
			len(operations), richMessageMaxFallbackChunks)
	}
	setGeneratedOperationReply(&operations[0], path.replyTo)
	return operations, nil
}

func generatedPartSidecars(path *responsePath, part generatedNativeLayoutPart, prepared []generatedPreparedMedia) []deliveryOperation {
	var originals []OutgoingMediaItem
	for _, atom := range part.atoms {
		if atom.kind != generatedLayoutAtomMedia {
			continue
		}
		for _, itemIndex := range atom.itemIndexes {
			if prepared[itemIndex].highResolution {
				originals = append(originals, prepared[itemIndex].original)
			}
		}
	}
	return generatedMediaOperationsStable(path, originals, 0)
}

func buildGeneratedDirectedPlan(
	ctx context.Context,
	path *responsePath,
	renderer *TelegramRenderer,
	source string,
	layout generatedMediaLayout,
	items []OutgoingMediaItem,
	threshold int,
) (deliveryPlan, []deliveryOperation, int, int, error) {
	groups, err := generatedLayoutAtomGroups(source, layout)
	if err != nil {
		return deliveryPlan{}, nil, 0, 0, err
	}
	fullFallback, err := generatedDirectedFullFallback(ctx, path, renderer, groups, items, threshold)
	if err != nil {
		return deliveryPlan{}, nil, 0, 0, err
	}
	prepared, nativeEligible := prepareGeneratedNativeMedia(items, threshold)
	if !nativeEligible {
		return deliveryPlan{}, fullFallback, 0, 0, nil
	}
	parts, err := packGeneratedNativeLayout(groups, prepared)
	if err != nil {
		return deliveryPlan{}, fullFallback, 0, 0, err
	}
	fallbackSuffixes, err := generatedDirectedFallbackByPart(ctx, path, renderer, parts, items, threshold)
	if err != nil {
		return deliveryPlan{}, fullFallback, 0, 0, err
	}

	operations := make([]deliveryOperation, 0, len(parts)*2)
	nativeAttachments, nativeBytes := 0, 0
	for partIndex, part := range parts {
		var op deliveryOperation
		if len(part.mediaGroupSizes) == 0 {
			op = deliveryOperation{
				Kind: persistentOperationRichText,
				Text: &OutgoingResponse{
					ConversationID: path.convID,
					ThreadRoot:     path.threadRoot,
					Text:           part.htmlParts[0],
					Format:         ResponseFormatRichHTML,
				},
			}
		} else {
			op = deliveryOperation{
				Kind: persistentOperationRichMedia,
				RichMedia: &OutgoingRichMedia{
					ConversationID:  path.convID,
					ThreadRoot:      path.threadRoot,
					HTMLParts:       append([]string(nil), part.htmlParts...),
					MediaGroupSizes: append([]int(nil), part.mediaGroupSizes...),
					Items:           append([]OutgoingMediaItem(nil), part.previewItems...),
				},
			}
			for _, item := range part.previewItems {
				nativeAttachments++
				nativeBytes += len(item.Data)
			}
		}
		if len(operations) == 0 {
			setGeneratedOperationReply(&op, path.replyTo)
		}
		op.formatFallback = fallbackSuffixes[partIndex]
		operations = append(operations, op)
		sidecars := generatedPartSidecars(path, part, prepared)
		for i := range sidecars {
			sidecars[i] = withoutReply(sidecars[i])
		}
		operations = append(operations, sidecars...)
	}
	plan := deliveryPlan{Operations: operations}
	if err := plan.validate(); err != nil {
		return deliveryPlan{}, fullFallback, 0, 0, err
	}
	return plan, fullFallback, nativeAttachments, nativeBytes, nil
}

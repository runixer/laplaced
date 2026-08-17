package bot

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"github.com/runixer/laplaced/internal/telegram"
)

type generatedRichDeliveryPlan struct {
	plan              deliveryPlan
	legacyFallback    []deliveryOperation
	cleanedText       string
	cleanedTextValid  bool
	layoutMode        generatedMediaLayoutMode
	layoutReason      generatedMediaLayoutReason
	nativeAttachments int
	nativeBytes       int
}

func normalizedGeneratedPhotoItem(item OutgoingMediaItem, index int) OutgoingMediaItem {
	item.AsDocument = false
	if strings.TrimSpace(item.Filename) == "" {
		item.Filename = fmt.Sprintf("generated-%d.png", index+1)
	}
	return item
}

// generatedPhotoCanBePreviewed verifies the Bot API uploaded-photo envelope
// before any item is planned as a Photo. Invalid, oversized-dimension, or
// extreme-aspect images stay on the bounded Document compatibility path.
func generatedPhotoCanBePreviewed(item OutgoingMediaItem) bool {
	if len(item.Data) == 0 || len(item.Data) > telegramRichPhotoMaxBytes {
		return false
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(item.Data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return false
	}
	if int64(config.Width)+int64(config.Height) > 10_000 {
		return false
	}
	long, short := config.Width, config.Height
	if long < short {
		long, short = short, long
	}
	return int64(long) <= int64(short)*20
}

func generatedGalleryLayout(items []OutgoingMediaItem) (html string, blockOverhead int, err error) {
	uploads := make([]telegram.RichPhotoUpload, 0, len(items))
	for _, item := range items {
		uploads = append(uploads, telegram.RichPhotoUpload{
			Filename: item.Filename,
			MIME:     item.MIME,
			Data:     item.Data,
		})
	}
	media, _, err := telegram.BuildRichPhotoMedia(uploads)
	if err != nil {
		return "", 0, err
	}
	html, err = generatedRichGalleryHTML(media)
	if err != nil {
		return "", 0, err
	}
	blockOverhead = len(items)
	if len(items) > 1 {
		blockOverhead++ // collage/slideshow container
	}
	return html, blockOverhead, nil
}

func generatedLegacyTextOperations(convID, threadRoot string, chunks []string) []deliveryOperation {
	operations := make([]deliveryOperation, 0, len(chunks))
	for _, value := range chunks {
		chunk := value
		operations = append(operations, deliveryOperation{
			Kind: persistentOperationLegacyText,
			Text: &OutgoingResponse{
				ConversationID: convID,
				ThreadRoot:     threadRoot,
				Text:           chunk,
			},
		})
	}
	return operations
}

func generatedMediaOperations(path *responsePath, caption string, items []OutgoingMediaItem, threshold int) []deliveryOperation {
	operations := generatedMediaOperationsStable(path, items, threshold)
	if len(operations) > 0 {
		operations[0].Media.ReplyTo = path.replyTo
		operations[0].Media.Caption = caption
	}
	return operations
}

func generatedAutomaticFallbackOperations(
	ctx context.Context,
	path *responsePath,
	renderer *TelegramRenderer,
	responseText string,
	items []OutgoingMediaItem,
	threshold int,
) ([]deliveryOperation, error) {
	if strings.TrimSpace(responseText) == "" {
		operations := generatedMediaOperations(path, "", items, threshold)
		if len(operations) == 0 {
			return nil, fmt.Errorf("generated-media fallback has no media operations")
		}
		return operations, nil
	}
	return generatedFallbackOperations(ctx, path, renderer, responseText, items, threshold)
}

func generatedFallbackOperations(
	ctx context.Context,
	path *responsePath,
	renderer *TelegramRenderer,
	responseText string,
	items []OutgoingMediaItem,
	threshold int,
) ([]deliveryOperation, error) {
	if err := validateRichSourceBounds(responseText); err != nil {
		return nil, fmt.Errorf("generated-media fallback source: %w", err)
	}
	sources, err := splitStandaloneRichSources(responseText)
	if err != nil {
		return nil, fmt.Errorf("resolve generated-media fallback boundaries: %w", err)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("generated-media fallback has no text sources")
	}

	// Only the first resolved source may contribute a media caption. Explicit
	// standalone split boundaries must remain persistent-message boundaries even
	// when the complete response would otherwise fit into Telegram's caption.
	caption, firstOverflow := renderer.RenderSafeRichCaption(ctx, sources[0])
	operations := generatedMediaOperations(path, caption, items, threshold)
	if len(operations) == 0 {
		return nil, fmt.Errorf("generated-media fallback has no media operations")
	}

	followUpSources := make([]string, 0, len(sources))
	if strings.TrimSpace(firstOverflow) != "" {
		followUpSources = append(followUpSources, firstOverflow)
	}
	followUpSources = append(followUpSources, sources[1:]...)
	for i, source := range followUpSources {
		chunks, renderErr := renderer.renderSafeRichFallbackPart(ctx, source)
		if renderErr != nil {
			return nil, fmt.Errorf("render generated-media fallback follow-up %d: %w", i, renderErr)
		}
		if len(chunks) == 0 {
			return nil, fmt.Errorf("render generated-media fallback follow-up %d: no chunks", i)
		}
		operations = append(operations, generatedLegacyTextOperations(path.convID, path.threadRoot, chunks)...)
	}
	return operations, nil
}

func generatedRichSuffixFallback(path *responsePath, parts []richRenderedPart, start int) []deliveryOperation {
	var operations []deliveryOperation
	for i := start; i < len(parts); i++ {
		operations = append(operations, generatedLegacyTextOperations(path.convID, path.threadRoot, parts[i].legacyFallback)...)
	}
	return operations
}

// planGeneratedRichDelivery resolves the turn-local MEDIA protocol before any
// persistent request. The model chooses only ordinal placement/grouping; the
// application still owns artifact lookup, photo bytes, Telegram media ids and
// the final HTML/media graph. Invalid or absent directives retain the automatic
// top-gallery behavior.
func (b *Bot) planGeneratedRichDelivery(
	ctx context.Context,
	path *responsePath,
	responseText string,
	items []OutgoingMediaItem,
	totalGenerated int,
) (generatedRichDeliveryPlan, bool, string) {
	if totalGenerated < len(items) || totalGenerated < 1 {
		return generatedRichDeliveryPlan{}, false, richMetricFallbackHardPreflight
	}
	normalizedItems := append([]OutgoingMediaItem(nil), items...)
	availableOrdinals := make([]int, len(normalizedItems))
	for i := range normalizedItems {
		ordinal := normalizedItems[i].SourceOrdinal
		if ordinal <= 0 {
			ordinal = i + 1
			normalizedItems[i].SourceOrdinal = ordinal
		}
		if ordinal > totalGenerated {
			return generatedRichDeliveryPlan{}, false, richMetricFallbackHardPreflight
		}
		availableOrdinals[i] = ordinal
	}
	layout, err := parseGeneratedMediaLayout(responseText, availableOrdinals)
	if err != nil {
		return generatedRichDeliveryPlan{}, false, richMetricFallbackHardPreflight
	}
	if totalGenerated > generatedRichGalleryMax {
		for _, line := range layout.ProtocolLines {
			if line.Kind == generatedMediaProtocolMedia {
				layout = invalidGeneratedMediaLayout(layout, generatedMediaLayoutReasonTooMany)
				break
			}
		}
	}
	base := generatedRichDeliveryPlan{
		cleanedText:      layout.MarkerFreeSource,
		cleanedTextValid: true,
		layoutMode:       layout.Mode,
		layoutReason:     layout.Reason,
	}
	deliverySource, err := generatedMediaDeliverySource(responseText, layout)
	if err != nil {
		return base, false, richMetricFallbackHardPreflight
	}
	renderer, ok := b.renderer.(*TelegramRenderer)
	if !ok || path == nil {
		return base, false, richMetricFallbackMediaIneligible
	}

	if layout.Mode == generatedMediaLayoutDirected {
		plan, fallback, nativeAttachments, nativeBytes, buildErr := buildGeneratedDirectedPlan(
			ctx, path, renderer, responseText, layout, normalizedItems,
			b.cfg.Agents.ImageGenerator.DocumentThresholdBytes,
		)
		base.legacyFallback = fallback
		if buildErr != nil {
			return base, false, richMetricFallbackRenderOrLimit
		}
		if len(plan.Operations) == 0 {
			return base, false, richMetricFallbackMediaIneligible
		}
		if _, ok := b.transport.(RichMediaTransport); !ok {
			return base, false, richMetricFallbackMediaIneligible
		}
		base.plan = plan
		base.nativeAttachments = nativeAttachments
		base.nativeBytes = nativeBytes
		return base, true, richMetricFallbackNone
	}

	// Automatic placement (including atomic degradation of an invalid authored
	// layout) keeps the established caption/fallback representation.
	fallback, fallbackErr := generatedAutomaticFallbackOperations(
		ctx, path, renderer, deliverySource, normalizedItems,
		b.cfg.Agents.ImageGenerator.DocumentThresholdBytes,
	)
	if fallbackErr != nil {
		return base, false, richMetricFallbackRenderOrLimit
	}
	base.legacyFallback = fallback
	if totalGenerated > generatedRichGalleryMax {
		return base, false, richMetricFallbackMediaIneligible
	}
	automatic, native, reason := b.planAutomaticGeneratedRichDelivery(
		ctx, path, deliverySource, normalizedItems,
	)
	automatic.legacyFallback = fallback
	automatic.cleanedText = base.cleanedText
	automatic.cleanedTextValid = true
	automatic.layoutMode = base.layoutMode
	automatic.layoutReason = base.layoutReason
	return automatic, native, reason
}

// planAutomaticGeneratedRichDelivery is the compatibility composition: one
// gallery at the start of the first rich part, followed by the rendered body.
func (b *Bot) planAutomaticGeneratedRichDelivery(
	ctx context.Context,
	path *responsePath,
	responseText string,
	items []OutgoingMediaItem,
) (generatedRichDeliveryPlan, bool, string) {
	if path == nil || len(items) == 0 || len(items) > generatedRichGalleryMax {
		return generatedRichDeliveryPlan{}, false, richMetricFallbackMediaIneligible
	}
	if _, ok := b.transport.(RichMediaTransport); !ok {
		return generatedRichDeliveryPlan{}, false, richMetricFallbackMediaIneligible
	}
	renderer, ok := b.renderer.(*TelegramRenderer)
	if !ok {
		return generatedRichDeliveryPlan{}, false, richMetricFallbackMediaIneligible
	}
	var parts []richRenderedPart
	if strings.TrimSpace(responseText) == "" {
		parts = []richRenderedPart{{}}
	} else {
		preflight, err := preflightRichDelivery(ctx, responseText, renderer)
		if err != nil {
			return generatedRichDeliveryPlan{}, false, richMetricFallbackHardPreflight
		}
		if preflight.localFallback {
			return generatedRichDeliveryPlan{}, false, preflight.fallbackReason
		}
		if len(preflight.parts) == 0 {
			return generatedRichDeliveryPlan{}, false, richMetricFallbackMediaIneligible
		}
		parts = preflight.parts
	}

	threshold := b.cfg.Agents.ImageGenerator.DocumentThresholdBytes
	previewItems := make([]OutgoingMediaItem, 0, len(items))
	documentItems := make([]OutgoingMediaItem, 0, len(items))
	for i, raw := range items {
		if len(raw.Data) == 0 || raw.AsDocument || !strings.HasPrefix(strings.ToLower(raw.MIME), "image/") {
			return generatedRichDeliveryPlan{}, false, richMetricFallbackMediaIneligible
		}
		preview := normalizedGeneratedPhotoItem(raw, i)
		isHighResolution := threshold > 0 && len(raw.Data) > threshold
		if !generatedPhotoCanBePreviewed(preview) {
			return generatedRichDeliveryPlan{}, false, richMetricFallbackMediaIneligible
		}
		previewItems = append(previewItems, preview)
		if isHighResolution {
			original := preview
			original.AsDocument = true
			documentItems = append(documentItems, original)
		}
	}

	galleryHTML, galleryBlocks, err := generatedGalleryLayout(previewItems)
	if err != nil {
		return generatedRichDeliveryPlan{}, false, richMetricFallbackMediaIneligible
	}
	first := parts[0]
	if first.stats.Blocks+galleryBlocks > richMessageSafeBlockLimit ||
		len(first.html)+len(galleryHTML) > richMessageMaxRenderedBytes {
		return generatedRichDeliveryPlan{}, false, richMetricFallbackRenderOrLimit
	}

	fullFallback, err := generatedAutomaticFallbackOperations(ctx, path, renderer, responseText, items, threshold)
	if err != nil {
		return generatedRichDeliveryPlan{}, false, richMetricFallbackRenderOrLimit
	}

	var sidecars []deliveryOperation
	if len(documentItems) > 0 {
		sidecars = generatedMediaOperations(path, "", documentItems, threshold)
		for i := range sidecars {
			sidecars[i].Media.ReplyTo = ""
		}
	}

	operations := make([]deliveryOperation, 0, len(parts)+1)
	operations = append(operations, deliveryOperation{
		Kind: persistentOperationRichMedia,
		RichMedia: &OutgoingRichMedia{
			ConversationID:  path.convID,
			ThreadRoot:      path.threadRoot,
			ReplyTo:         path.replyTo,
			HTMLParts:       []string{"", first.html},
			MediaGroupSizes: []int{len(previewItems)},
			Items:           previewItems,
		},
		formatFallback: fullFallback,
	})
	// A high-resolution original belongs to the gallery that previews it, so its
	// Document sidecar is persisted immediately after that owning rich part.
	// If it confirms, a later text-format fallback must never resend it.
	operations = append(operations, sidecars...)
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		op := deliveryOperation{
			Kind: persistentOperationRichText,
			Text: &OutgoingResponse{
				ConversationID: path.convID,
				ThreadRoot:     path.threadRoot,
				Text:           part.html,
				Format:         ResponseFormatRichHTML,
			},
		}
		op.formatFallback = generatedRichSuffixFallback(path, parts, i)
		operations = append(operations, op)
	}
	plan := deliveryPlan{Operations: operations}
	if err := plan.validate(); err != nil {
		return generatedRichDeliveryPlan{}, false, richMetricFallbackRenderOrLimit
	}
	nativeBytes := 0
	for _, item := range previewItems {
		nativeBytes += len(item.Data)
	}
	return generatedRichDeliveryPlan{
		plan:              plan,
		nativeAttachments: len(previewItems),
		nativeBytes:       nativeBytes,
	}, true, richMetricFallbackNone
}

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
	var documents, photos []OutgoingMediaItem
	for i, raw := range items {
		item := normalizedGeneratedPhotoItem(raw, i)
		asDocument := raw.AsDocument ||
			(threshold > 0 && len(item.Data) > threshold) ||
			!strings.HasPrefix(strings.ToLower(item.MIME), "image/") ||
			!generatedPhotoCanBePreviewed(item)
		item.AsDocument = asDocument
		if asDocument {
			documents = append(documents, item)
		} else {
			photos = append(photos, item)
		}
	}

	orderedBatches := make([][]OutgoingMediaItem, 0, (len(items)+9)/10)
	for _, group := range [][]OutgoingMediaItem{documents, photos} {
		for len(group) > 0 {
			size := min(10, len(group))
			orderedBatches = append(orderedBatches, append([]OutgoingMediaItem(nil), group[:size]...))
			group = group[size:]
		}
	}
	operations := make([]deliveryOperation, 0, len(orderedBatches))
	for i, batch := range orderedBatches {
		media := &OutgoingMedia{
			ConversationID: path.convID,
			ThreadRoot:     path.threadRoot,
			Items:          batch,
		}
		if i == 0 {
			media.ReplyTo = path.replyTo
			media.Caption = caption
		}
		operations = append(operations, deliveryOperation{Kind: persistentOperationMedia, Media: media})
	}
	return operations
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

func generatedRichSuffixFallback(path *responsePath, parts []richRenderedPart, start int, sidecars []deliveryOperation) []deliveryOperation {
	var operations []deliveryOperation
	for i := start; i < len(parts); i++ {
		operations = append(operations, generatedLegacyTextOperations(path.convID, path.threadRoot, parts[i].legacyFallback)...)
	}
	operations = append(operations, sidecars...)
	return operations
}

// planGeneratedRichDelivery builds the complete native and legacy envelopes
// before any persistent request. Generated media placement is app-owned: one
// gallery is injected at the top of the first rich part; model-authored image
// destinations never participate.
func (b *Bot) planGeneratedRichDelivery(
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
	first := preflight.parts[0]
	if first.stats.Blocks+galleryBlocks > richMessageSafeBlockLimit ||
		len(first.html)+len(galleryHTML) > richMessageMaxRenderedBytes {
		return generatedRichDeliveryPlan{}, false, richMetricFallbackRenderOrLimit
	}

	fullFallback, err := generatedFallbackOperations(ctx, path, renderer, responseText, items, threshold)
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

	operations := make([]deliveryOperation, 0, len(preflight.parts)+1)
	operations = append(operations, deliveryOperation{
		Kind: persistentOperationRichMedia,
		RichMedia: &OutgoingRichMedia{
			ConversationID: path.convID,
			ThreadRoot:     path.threadRoot,
			ReplyTo:        path.replyTo,
			HTML:           first.html,
			Items:          previewItems,
		},
		formatFallback: fullFallback,
	})
	for i := 1; i < len(preflight.parts); i++ {
		part := preflight.parts[i]
		op := deliveryOperation{
			Kind: persistentOperationRichText,
			Text: &OutgoingResponse{
				ConversationID: path.convID,
				ThreadRoot:     path.threadRoot,
				Text:           part.html,
				Format:         ResponseFormatRichHTML,
			},
		}
		op.formatFallback = generatedRichSuffixFallback(path, preflight.parts, i, sidecars)
		operations = append(operations, op)
	}
	operations = append(operations, sidecars...)
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

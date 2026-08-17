package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/markdown"
	"github.com/runixer/laplaced/internal/telegram"
)

func generatedV2PhotoItems(count int) []OutgoingMediaItem {
	items := make([]OutgoingMediaItem, count)
	for i := range items {
		items[i] = OutgoingMediaItem{
			Data:     append([]byte(nil), generatedTestPNG...),
			Filename: fmt.Sprintf("generated-%02d.png", i+1),
			MIME:     "image/png",
		}
	}
	return items
}

func generatedV2PNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, png.Encode(&out, image.NewNRGBA(image.Rect(0, 0, width, height))))
	return out.Bytes()
}

func generatedV2Planner(t *testing.T) (*Bot, *responsePath) {
	t.Helper()
	transport := &recordingTransport{richMediaID: "rich-media"}
	bot, _, userID := newGeneratedDeliveryTestBot(t, transport)
	bot.cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
	bot.cfg.Agents.ImageGenerator.DocumentThresholdBytes = 0
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	return bot, path
}

func TestPlanGeneratedRichDeliveryV2_GalleryLayoutBoundaries(t *testing.T) {
	tests := []struct {
		count         int
		wantContainer string
		wantBlocks    int
	}{
		{count: 1, wantBlocks: 1},
		{count: 2, wantContainer: "tg-collage", wantBlocks: 3},
		{count: 4, wantContainer: "tg-collage", wantBlocks: 5},
		{count: 5, wantContainer: "tg-slideshow", wantBlocks: 6},
		{count: 10, wantContainer: "tg-slideshow", wantBlocks: 11},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("photos_%d", tt.count), func(t *testing.T) {
			bot, path := generatedV2Planner(t)
			items := generatedV2PhotoItems(tt.count)

			planned, ok, reason := bot.planGeneratedRichDelivery(
				context.Background(), path, "# Gallery", items, len(items),
			)

			require.True(t, ok, "fallback reason: %s", reason)
			assert.Equal(t, richMetricFallbackNone, reason)
			assert.Equal(t, tt.count, planned.nativeAttachments)
			assert.Equal(t, tt.count*len(generatedTestPNG), planned.nativeBytes)
			require.Len(t, planned.plan.Operations, 1)
			op := planned.plan.Operations[0]
			require.Equal(t, persistentOperationRichMedia, op.Kind)
			require.NotNil(t, op.RichMedia)
			assert.Len(t, op.RichMedia.Items, tt.count)
			assert.Equal(t, path.replyTo, op.RichMedia.ReplyTo)
			require.Len(t, op.formatFallback, 1)
			assert.Equal(t, persistentOperationMedia, op.formatFallback[0].Kind)
			assert.Len(t, op.formatFallback[0].Media.Items, tt.count)

			html, blocks, err := generatedGalleryLayout(op.RichMedia.Items)
			require.NoError(t, err)
			assert.Equal(t, tt.wantBlocks, blocks)
			assert.Equal(t, tt.count, strings.Count(html, `<img src="tg://photo?id=rich_photo_`))
			if tt.wantContainer == "" {
				assert.Equal(t, `<img src="tg://photo?id=rich_photo_0"/>`, html)
				assert.NotContains(t, html, "tg-collage")
				assert.NotContains(t, html, "tg-slideshow")
			} else {
				assert.True(t, strings.HasPrefix(html, "<"+tt.wantContainer+">"))
				assert.True(t, strings.HasSuffix(html, "</"+tt.wantContainer+">"))
			}
			require.NoError(t, planned.plan.validate())
		})
	}
}

func TestPlanGeneratedRichDeliveryV2_ReservesInjectedGalleryLimits(t *testing.T) {
	bot, path := generatedV2Planner(t)
	items := generatedV2PhotoItems(4)
	galleryHTML, galleryBlocks, err := generatedGalleryLayout(items)
	require.NoError(t, err)
	require.Equal(t, 5, galleryBlocks, "four photos plus one collage container")

	paragraphs := func(count int) string {
		return strings.TrimSuffix(strings.Repeat("x\n\n", count), "\n\n")
	}

	t.Run("block budget exact", func(t *testing.T) {
		source := paragraphs(richMessageSafeBlockLimit - galleryBlocks)
		preflight, err := preflightRichDelivery(context.Background(), source, bot.renderer.(*TelegramRenderer))
		require.NoError(t, err)
		require.Len(t, preflight.parts, 1)
		require.Equal(t, richMessageSafeBlockLimit-galleryBlocks, preflight.parts[0].stats.Blocks)

		planned, ok, reason := bot.planGeneratedRichDelivery(
			context.Background(), path, source, items, len(items),
		)
		require.True(t, ok, "fallback reason: %s", reason)
		assert.Equal(t, richMessageSafeBlockLimit,
			preflight.parts[0].stats.Blocks+galleryBlocks)
		assert.NotEmpty(t, planned.plan.Operations)
	})

	t.Run("block budget plus one", func(t *testing.T) {
		source := paragraphs(richMessageSafeBlockLimit - galleryBlocks + 1)
		preflight, err := preflightRichDelivery(context.Background(), source, bot.renderer.(*TelegramRenderer))
		require.NoError(t, err, "text-only body remains within its own limit")
		require.Len(t, preflight.parts, 1)

		planned, ok, reason := bot.planGeneratedRichDelivery(
			context.Background(), path, source, items, len(items),
		)
		assert.False(t, ok)
		assert.Equal(t, richMetricFallbackRenderOrLimit, reason)
		assert.Empty(t, planned.plan.Operations)
	})

	richParagraphAtRenderedBytes := func(target int) string {
		t.Helper()
		const paragraphMarkupBytes = len("<p></p>")
		require.GreaterOrEqual(t, target, paragraphMarkupBytes)
		payloadBytes := target - paragraphMarkupBytes
		ampersands := payloadBytes / len("&amp;")
		plainBytes := payloadBytes % len("&amp;")
		source := strings.Repeat("&", ampersands) + strings.Repeat("x", plainBytes)
		html, _, err := markdown.ToRichHTML(source)
		require.NoError(t, err)
		require.Len(t, html, target)
		return source
	}

	t.Run("rendered byte budget exact", func(t *testing.T) {
		source := richParagraphAtRenderedBytes(richMessageMaxRenderedBytes - len(galleryHTML))
		planned, ok, reason := bot.planGeneratedRichDelivery(
			context.Background(), path, source, items, len(items),
		)
		require.True(t, ok, "fallback reason: %s", reason)
		require.Len(t, planned.plan.Operations, 1)
		assert.Equal(t, richMessageMaxRenderedBytes,
			len(strings.Join(planned.plan.Operations[0].RichMedia.HTMLParts, ""))+len(galleryHTML))
	})

	t.Run("rendered byte budget plus one", func(t *testing.T) {
		source := richParagraphAtRenderedBytes(richMessageMaxRenderedBytes - len(galleryHTML) + 1)
		preflight, err := preflightRichDelivery(context.Background(), source, bot.renderer.(*TelegramRenderer))
		require.NoError(t, err, "text-only body remains within its own byte limit")
		require.Len(t, preflight.parts, 1)

		planned, ok, reason := bot.planGeneratedRichDelivery(
			context.Background(), path, source, items, len(items),
		)
		assert.False(t, ok)
		assert.Equal(t, richMetricFallbackRenderOrLimit, reason)
		assert.Empty(t, planned.plan.Operations)
	})
}

func TestPlanGeneratedRichDeliveryV2_ElevenPhotosUseBoundedLegacyBatches(t *testing.T) {
	bot, path := generatedV2Planner(t)
	items := generatedV2PhotoItems(11)

	planned, ok, reason := bot.planGeneratedRichDelivery(
		context.Background(), path, "# Eleven", items, len(items),
	)
	assert.False(t, ok)
	assert.Equal(t, richMetricFallbackMediaIneligible, reason)
	assert.Empty(t, planned.plan.Operations)

	fallback, err := generatedFallbackOperations(
		context.Background(), path, bot.renderer.(*TelegramRenderer), "caption", items, 0,
	)
	require.NoError(t, err)
	require.Len(t, fallback, 2)
	assertGeneratedV2HomogeneousBatches(t, fallback, []int{10, 1}, false)
	assert.Equal(t, "caption", fallback[0].Media.Caption)
	assert.Equal(t, path.replyTo, fallback[0].Media.ReplyTo)
	assert.Empty(t, fallback[1].Media.Caption)
	assert.Empty(t, fallback[1].Media.ReplyTo)
	require.NoError(t, (deliveryPlan{Operations: fallback}).validate())
}

func TestPlanGeneratedRichDeliveryV2_SizeAloneNeverAddsOriginalSidecar(t *testing.T) {
	bot, path := generatedV2Planner(t)
	bot.cfg.Agents.ImageGenerator.DocumentThresholdBytes = len(generatedTestPNG) - 1
	items := generatedV2PhotoItems(1)

	planned, ok, reason := bot.planGeneratedRichDelivery(
		context.Background(), path, "# High resolution", items, len(items),
	)

	require.True(t, ok, "fallback reason: %s", reason)
	require.Len(t, planned.plan.Operations, 1)
	preview := planned.plan.Operations[0]
	require.Equal(t, persistentOperationRichMedia, preview.Kind)
	require.Len(t, preview.RichMedia.Items, 1)
	assert.False(t, preview.RichMedia.Items[0].AsDocument)
	assert.Equal(t, OutgoingMediaWireKindPhoto, preview.RichMedia.Items[0].WireKind)
	assert.Equal(t, items[0].Data, preview.RichMedia.Items[0].Data)

	// A rich-format rejection keeps the same preview policy. File size alone
	// never changes user intent into an original Document request.
	require.Len(t, preview.formatFallback, 1)
	assertGeneratedV2HomogeneousBatches(t, preview.formatFallback, []int{1}, false)
	assert.Equal(t, OutgoingMediaWireKindPhoto, preview.formatFallback[0].Media.Items[0].WireKind)
	assert.Contains(t, preview.formatFallback[0].Media.Caption, "High resolution")
	require.NoError(t, planned.plan.validate())
}

func TestPlanGeneratedRichDeliveryV2_InvalidPhotoEnvelopeFallsBackToDocument(t *testing.T) {
	tests := []struct {
		name string
		data func(*testing.T) []byte
	}{
		{name: "corrupt bytes", data: func(*testing.T) []byte { return []byte("not-an-image") }},
		{name: "invalid aspect ratio", data: func(t *testing.T) []byte { return generatedV2PNG(t, 21, 1) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bot, path := generatedV2Planner(t)
			items := []OutgoingMediaItem{{
				Data: tt.data(t), Filename: "generated.png", MIME: "image/png",
			}}

			planned, ok, reason := bot.planGeneratedRichDelivery(
				context.Background(), path, "# Invalid preview", items, len(items),
			)
			assert.False(t, ok)
			assert.Equal(t, richMetricFallbackMediaIneligible, reason)
			assert.Empty(t, planned.plan.Operations)

			fallback, err := generatedFallbackOperations(
				context.Background(), path, bot.renderer.(*TelegramRenderer), "caption", items, 0,
			)
			require.NoError(t, err)
			require.Len(t, fallback, 1)
			assertGeneratedV2HomogeneousBatches(t, fallback, []int{1}, true)
			require.NoError(t, (deliveryPlan{Operations: fallback}).validate())
		})
	}
}

func TestGeneratedMediaOperationsV2_BatchesMixedMediaHomogeneously(t *testing.T) {
	bot, path := generatedV2Planner(t)
	documents := generatedV2PhotoItems(12)
	for i := range documents {
		documents[i].AsDocument = true
	}
	photos := generatedV2PhotoItems(11)
	items := append(documents, photos...)

	operations := generatedMediaOperations(path, "caption", items, 0)
	require.Len(t, operations, 4)
	assertGeneratedV2HomogeneousBatches(t, operations, []int{10, 2, 10, 1}, true, true, false, false)
	assert.Equal(t, "caption", operations[0].Media.Caption)
	assert.Equal(t, path.replyTo, operations[0].Media.ReplyTo)
	for _, op := range operations[1:] {
		assert.Empty(t, op.Media.Caption)
		assert.Empty(t, op.Media.ReplyTo)
	}
	require.NoError(t, (deliveryPlan{Operations: operations}).validate())
	require.NoError(t, bot.validateDeliveryExecution(deliveryPlan{Operations: operations}, nil))
}

func assertGeneratedV2HomogeneousBatches(
	t *testing.T,
	operations []deliveryOperation,
	wantSizes []int,
	wantDocuments ...bool,
) {
	t.Helper()
	require.Len(t, operations, len(wantSizes))
	for i, op := range operations {
		require.Equal(t, persistentOperationMedia, op.Kind, "operation %d", i)
		require.NotNil(t, op.Media, "operation %d", i)
		require.Len(t, op.Media.Items, wantSizes[i], "operation %d", i)
		require.LessOrEqual(t, len(op.Media.Items), 10, "operation %d", i)
		wantDocument := false
		if i < len(wantDocuments) {
			wantDocument = wantDocuments[i]
		}
		for j, item := range op.Media.Items {
			assert.Equal(t, wantDocument, item.AsDocument, "operation %d item %d", i, j)
		}
	}
}

func TestExecuteGeneratedRichDeliveryV2_FirstFormatRejectionUsesFullFallback(t *testing.T) {
	formatErr := errors.Join(ErrRichMessageRejected, &telegram.APIError{
		Code: 400, Description: "Bad Request: RICH_MESSAGE_MEDIA_INVALID",
	})
	transport := &recordingTransport{richMediaErr: formatErr, mediaID: "legacy-media"}
	bot, _, userID := newGeneratedDeliveryTestBot(t, transport)
	bot.cfg.Agents.ImageGenerator.DocumentThresholdBytes = 0
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	planned, ok, reason := bot.planGeneratedRichDelivery(
		context.Background(), path, "# First\n###SPLIT###\n## Second", generatedV2PhotoItems(1), 1,
	)
	require.True(t, ok, "fallback reason: %s", reason)
	require.Len(t, planned.plan.Operations, 2)

	result := bot.executeDeliveryPlan(context.Background(), planned.plan)

	require.NoError(t, result.err)
	assert.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Equal(t, richMetricPathAPIFallback, result.metricPath)
	assert.Equal(t, 3, result.attempts)
	require.Len(t, transport.richMedia, 1)
	require.Len(t, transport.media, 1)
	require.Len(t, transport.text, 1)
	assert.NotContains(t, transport.media[0].Caption, richSplitDelimiter)
	assert.Contains(t, transport.media[0].Caption, "First")
	assert.Equal(t, ResponseFormatDefault, transport.text[0].Format)
	assert.NotContains(t, transport.text[0].Text, richSplitDelimiter)
	assert.Contains(t, transport.text[0].Text, "Second")
	assert.Equal(t, []string{"legacy-media", "text-message-1"}, result.confirmedIDs)
}

func TestExecuteGeneratedRichDeliveryV2_LaterFormatRejectionUsesOnlySuffixFallback(t *testing.T) {
	formatErr := errors.Join(ErrRichMessageRejected, &telegram.APIError{
		Code: 400, Description: "Bad Request: RICH_MESSAGE_INVALID",
	})
	transport := &recordingTransport{
		richMediaID: "rich-media",
		textErrors:  map[int]error{0: formatErr},
	}
	bot, _, userID := newGeneratedDeliveryTestBot(t, transport)
	bot.cfg.Agents.ImageGenerator.DocumentThresholdBytes = 0
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	planned, ok, reason := bot.planGeneratedRichDelivery(
		context.Background(), path, "# First\n###SPLIT###\n## Second", generatedV2PhotoItems(1), 1,
	)
	require.True(t, ok, "fallback reason: %s", reason)
	require.Len(t, planned.plan.Operations, 2)

	result := bot.executeDeliveryPlan(context.Background(), planned.plan)

	require.NoError(t, result.err)
	assert.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Equal(t, richMetricPathAPIFallback, result.metricPath)
	assert.Equal(t, 3, result.attempts)
	require.Len(t, transport.richMedia, 1)
	assert.Empty(t, transport.media, "confirmed gallery must never be resent")
	require.Len(t, transport.text, 2)
	assert.Equal(t, ResponseFormatRichHTML, transport.text[0].Format)
	assert.Equal(t, ResponseFormatDefault, transport.text[1].Format)
	assert.Contains(t, transport.text[1].Text, "Second")
	assert.Equal(t, []string{"rich-media", "text-message-1"}, result.confirmedIDs)
}

func TestExecuteGeneratedRichDeliveryV2_UnknownStopsWithoutFallback(t *testing.T) {
	tests := []struct {
		name        string
		transport   *recordingTransport
		wantOutcome richDeliveryOutcome
		wantIDs     []string
		wantTexts   int
	}{
		{
			name: "first rich media",
			transport: &recordingTransport{
				richMediaErr: errors.New("connection reset after write"),
				mediaID:      "must-not-send",
			},
			wantOutcome: richDeliveryUnknown,
		},
		{
			name: "later rich text",
			transport: &recordingTransport{
				richMediaID: "rich-media",
				textErrors:  map[int]error{0: context.DeadlineExceeded},
				mediaID:     "must-not-send",
			},
			wantOutcome: richDeliveryPartialUnknown,
			wantIDs:     []string{"rich-media"},
			wantTexts:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bot, _, userID := newGeneratedDeliveryTestBot(t, tt.transport)
			bot.cfg.Agents.ImageGenerator.DocumentThresholdBytes = 0
			path := generatedPath(bot, userID)
			path.richMode = config.TelegramRichMessagesSend
			planned, ok, reason := bot.planGeneratedRichDelivery(
				context.Background(), path, "# First\n###SPLIT###\n## Second", generatedV2PhotoItems(1), 1,
			)
			require.True(t, ok, "fallback reason: %s", reason)

			result := bot.executeDeliveryPlan(context.Background(), planned.plan)

			require.Error(t, result.err)
			assert.Equal(t, tt.wantOutcome, result.outcome)
			assert.Equal(t, tt.wantIDs, result.confirmedIDs)
			assert.Empty(t, tt.transport.media, "unknown must not select a fallback representation")
			assert.Len(t, tt.transport.text, tt.wantTexts)
		})
	}
}

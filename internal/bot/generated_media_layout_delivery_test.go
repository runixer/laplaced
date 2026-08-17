package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestPlanGeneratedMediaLayout_MiddlePlacement(t *testing.T) {
	bot, path := generatedV2Planner(t)
	items := generatedV2PhotoItems(1)
	source := "# Before\n\nIntro.\n\n###MEDIA:1###\n\n## After\n\nTail."

	planned, ok, reason := bot.planGeneratedRichDelivery(
		context.Background(), path, source, items, len(items),
	)

	require.True(t, ok, "fallback reason: %s", reason)
	assert.Equal(t, generatedMediaLayoutDirected, planned.layoutMode)
	assert.Equal(t, generatedMediaLayoutReasonNone, planned.layoutReason)
	assert.Equal(t, "# Before Intro. ## After Tail.", strings.Join(strings.Fields(planned.cleanedText), " "))
	require.Len(t, planned.plan.Operations, 1)
	op := planned.plan.Operations[0]
	require.Equal(t, persistentOperationRichMedia, op.Kind)
	require.NotNil(t, op.RichMedia)
	assert.Equal(t, []int{1}, op.RichMedia.MediaGroupSizes)
	require.Len(t, op.RichMedia.HTMLParts, 2)
	assert.Contains(t, op.RichMedia.HTMLParts[0], "<h1>Before</h1>")
	assert.Contains(t, op.RichMedia.HTMLParts[0], "Intro.")
	assert.Contains(t, op.RichMedia.HTMLParts[1], "<h2>After</h2>")
	assert.Contains(t, op.RichMedia.HTMLParts[1], "Tail.")
	assert.NotContains(t, strings.Join(op.RichMedia.HTMLParts, ""), "###MEDIA")

	composition, err := composeOutgoingRichMedia(*op.RichMedia)
	require.NoError(t, err)
	before := strings.Index(composition.html, "Before")
	media := strings.Index(composition.html, `tg://photo?id=rich_photo_0`)
	after := strings.Index(composition.html, "After")
	require.NotEqual(t, -1, before)
	require.NotEqual(t, -1, media)
	require.NotEqual(t, -1, after)
	assert.Less(t, before, media)
	assert.Less(t, media, after)
	require.NoError(t, planned.plan.validate())
}

func TestPlanGeneratedMediaLayout_ReordersMultipleGroups(t *testing.T) {
	bot, path := generatedV2Planner(t)
	items := generatedV2PhotoItems(3)
	source := "Before.\n\n###MEDIA:3###\n\nMiddle.\n\n###MEDIA:2,1###\n\nAfter."

	planned, ok, reason := bot.planGeneratedRichDelivery(context.Background(), path, source, items, len(items))

	require.True(t, ok, "fallback reason: %s", reason)
	require.Len(t, planned.plan.Operations, 1)
	rich := planned.plan.Operations[0].RichMedia
	require.NotNil(t, rich)
	assert.Equal(t, []int{1, 2}, rich.MediaGroupSizes)
	require.Len(t, rich.HTMLParts, 3)
	assert.Contains(t, rich.HTMLParts[0], "Before.")
	assert.Contains(t, rich.HTMLParts[1], "Middle.")
	assert.Contains(t, rich.HTMLParts[2], "After.")
	require.Len(t, rich.Items, 3)
	assert.Equal(t, []string{
		items[2].Filename,
		items[1].Filename,
		items[0].Filename,
	}, generatedLayoutFilenames(rich.Items))

	composition, err := composeOutgoingRichMedia(*rich)
	require.NoError(t, err)
	assert.Equal(t, 3, strings.Count(composition.html, `tg://photo?id=rich_photo_`))
	assert.Contains(t, composition.html, "<tg-collage>")
	assert.NotContains(t, composition.html, "###MEDIA")
	require.NoError(t, planned.plan.validate())
}

func TestPlanGeneratedMediaLayout_PreservesPlacementAcrossSplit(t *testing.T) {
	bot, path := generatedV2Planner(t)
	items := generatedV2PhotoItems(2)
	source := "# First\n\n###MEDIA:2###\n\n###SPLIT###\n\n## Second\n\n###MEDIA:1###\n\nTail."

	planned, ok, reason := bot.planGeneratedRichDelivery(context.Background(), path, source, items, len(items))

	require.True(t, ok, "fallback reason: %s", reason)
	assert.Equal(t, "# First ## Second Tail.", strings.Join(strings.Fields(planned.cleanedText), " "))
	require.Len(t, planned.plan.Operations, 2)
	first := planned.plan.Operations[0]
	second := planned.plan.Operations[1]
	require.NotNil(t, first.RichMedia)
	require.NotNil(t, second.RichMedia)
	assert.Equal(t, []string{items[1].Filename}, generatedLayoutFilenames(first.RichMedia.Items))
	assert.Equal(t, []string{items[0].Filename}, generatedLayoutFilenames(second.RichMedia.Items))
	assert.Contains(t, first.RichMedia.HTMLParts[0], "First")
	assert.NotContains(t, strings.Join(first.RichMedia.HTMLParts, ""), "Second")
	assert.Contains(t, second.RichMedia.HTMLParts[0], "Second")
	assert.Contains(t, second.RichMedia.HTMLParts[1], "Tail.")
	assert.Equal(t, path.replyTo, first.RichMedia.ReplyTo)
	assert.Empty(t, second.RichMedia.ReplyTo)
	for _, op := range planned.plan.Operations {
		assert.NotContains(t, strings.Join(op.RichMedia.HTMLParts, ""), "###MEDIA")
		assert.NotContains(t, strings.Join(op.RichMedia.HTMLParts, ""), "###SPLIT")
	}
	require.NoError(t, planned.plan.validate())
}

func TestPlanGeneratedMediaLayout_InvalidLayoutDegradesAtomicallyToTopGallery(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		ordinals   []int
		total      int
		wantReason generatedMediaLayoutReason
	}{
		{
			name:       "duplicate",
			source:     "Before.\n###MEDIA:1###\nMiddle.\n###MEDIA:1,2###\nAfter.",
			ordinals:   []int{1, 2},
			total:      2,
			wantReason: generatedMediaLayoutReasonDuplicate,
		},
		{
			name:       "unreferenced loaded image",
			source:     "Before.\n###MEDIA:1###\nAfter.",
			ordinals:   []int{1, 2},
			total:      2,
			wantReason: generatedMediaLayoutReasonUnreferenced,
		},
		{
			name:       "unavailable ordinal",
			source:     "Before.\n###MEDIA:1,2,3###\nAfter.",
			ordinals:   []int{1, 3},
			total:      3,
			wantReason: generatedMediaLayoutReasonUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bot, path := generatedV2Planner(t)
			items := generatedV2PhotoItems(2)
			for i := range items {
				items[i].SourceOrdinal = tt.ordinals[i]
			}

			planned, ok, reason := bot.planGeneratedRichDelivery(context.Background(), path, tt.source, items, tt.total)

			require.True(t, ok, "fallback reason: %s", reason)
			assert.Equal(t, generatedMediaLayoutInvalid, planned.layoutMode)
			assert.Equal(t, tt.wantReason, planned.layoutReason)
			assert.NotContains(t, planned.cleanedText, "###MEDIA")
			require.Len(t, planned.plan.Operations, 1)
			rich := planned.plan.Operations[0].RichMedia
			require.NotNil(t, rich)
			assert.Equal(t, []string{items[0].Filename, items[1].Filename}, generatedLayoutFilenames(rich.Items),
				"an invalid directed layout must not partially reorder the gallery")
			composition, err := composeOutgoingRichMedia(*rich)
			require.NoError(t, err)
			firstMedia := strings.Index(composition.html, `tg://photo?id=rich_photo_0`)
			body := strings.Index(composition.html, "Before.")
			require.NotEqual(t, -1, firstMedia)
			require.NotEqual(t, -1, body)
			assert.Less(t, firstMedia, body, "invalid layout must use the complete automatic top gallery")
			assert.NotContains(t, composition.html, "###MEDIA")
			require.NoError(t, planned.plan.validate())
		})
	}
}

func TestPlanGeneratedMediaLayout_MissingOrdinalDoesNotRenumberLaterArtifact(t *testing.T) {
	bot, path := generatedV2Planner(t)
	items := generatedV2PhotoItems(2)
	items[0].SourceOrdinal = 1
	items[1].SourceOrdinal = 3
	source := "Before.\n\n###MEDIA:3###\n\nMiddle. (artifact:999)\n\n###MEDIA:1###\n\nAfter."

	planned, ok, reason := bot.planGeneratedRichDelivery(context.Background(), path, source, items, 3)

	require.True(t, ok, "fallback reason: %s", reason)
	assert.Equal(t, generatedMediaLayoutDirected, planned.layoutMode)
	require.Len(t, planned.plan.Operations, 1)
	rich := planned.plan.Operations[0].RichMedia
	require.NotNil(t, rich)
	assert.Equal(t, []int{1, 1}, rich.MediaGroupSizes)
	assert.Equal(t, []string{items[1].Filename, items[0].Filename}, generatedLayoutFilenames(rich.Items))
	assert.Equal(t, []int{3, 1}, generatedLayoutOrdinals(rich.Items))
	require.NoError(t, planned.plan.validate())
}

func TestPlanGeneratedMediaLayout_TotalGeneratedLimitSurvivesLoadGaps(t *testing.T) {
	bot, path := generatedV2Planner(t)
	items := generatedV2PhotoItems(1)
	items[0].SourceOrdinal = 11
	source := "Before.\n\n###MEDIA:11###\n\nAfter."

	planned, ok, reason := bot.planGeneratedRichDelivery(
		context.Background(), path, source, items, 11,
	)

	assert.False(t, ok, "eleven generated slots must not regain directed/native eligibility when ten fail to load")
	assert.Equal(t, richMetricFallbackMediaIneligible, reason)
	assert.Equal(t, generatedMediaLayoutInvalid, planned.layoutMode)
	assert.Equal(t, generatedMediaLayoutReasonTooMany, planned.layoutReason)
	assert.Empty(t, planned.plan.Operations)
	assert.Equal(t, "Before. After.", strings.Join(strings.Fields(planned.cleanedText), " "))
	require.Len(t, planned.legacyFallback, 1)
	fallback := planned.legacyFallback[0]
	require.Equal(t, persistentOperationMedia, fallback.Kind)
	require.NotNil(t, fallback.Media)
	require.Len(t, fallback.Media.Items, 1)
	assert.Equal(t, 11, fallback.Media.Items[0].SourceOrdinal)
	assert.Equal(t, items[0].Filename, fallback.Media.Items[0].Filename)
	assert.Contains(t, fallback.Media.Caption, "Before.")
	assert.Contains(t, fallback.Media.Caption, "After.")
	assert.NotContains(t, fallback.Media.Caption, "###MEDIA")
	assert.Equal(t, path.replyTo, fallback.Media.ReplyTo)
	require.NoError(t, (deliveryPlan{Operations: planned.legacyFallback}).validate())
}

func TestPlanGeneratedMediaLayout_InvalidOnlySourceKeepsMediaOnlyNativeAndFallback(t *testing.T) {
	bot, path := generatedV2Planner(t)
	items := generatedV2PhotoItems(1)

	planned, ok, reason := bot.planGeneratedRichDelivery(
		context.Background(), path, "###MEDIA:2###", items, len(items),
	)

	require.True(t, ok, "fallback reason: %s", reason)
	assert.Equal(t, richMetricFallbackNone, reason)
	assert.Equal(t, generatedMediaLayoutInvalid, planned.layoutMode)
	assert.Equal(t, generatedMediaLayoutReasonUnavailable, planned.layoutReason)
	assert.Empty(t, strings.TrimSpace(planned.cleanedText))
	require.Len(t, planned.plan.Operations, 1)
	richOperation := planned.plan.Operations[0]
	require.Equal(t, persistentOperationRichMedia, richOperation.Kind)
	require.NotNil(t, richOperation.RichMedia)
	assert.Equal(t, []string{"", ""}, richOperation.RichMedia.HTMLParts)
	assert.Equal(t, []int{1}, richOperation.RichMedia.MediaGroupSizes)
	assert.Equal(t, generatedLayoutFilenames(items), generatedLayoutFilenames(richOperation.RichMedia.Items))
	composition, err := composeOutgoingRichMedia(*richOperation.RichMedia)
	require.NoError(t, err)
	assert.Equal(t, `<img src="tg://photo?id=rich_photo_0"/>`, composition.html)

	require.NotEmpty(t, planned.legacyFallback)
	require.Len(t, planned.legacyFallback, 1)
	fallback := planned.legacyFallback[0]
	require.Equal(t, persistentOperationMedia, fallback.Kind)
	require.NotNil(t, fallback.Media)
	assert.Equal(t, generatedLayoutFilenames(items), generatedLayoutFilenames(fallback.Media.Items))
	assert.Empty(t, fallback.Media.Caption)
	assert.Equal(t, path.replyTo, fallback.Media.ReplyTo)
	require.NoError(t, planned.plan.validate())
	require.NoError(t, (deliveryPlan{Operations: planned.legacyFallback}).validate())
}

func TestResponsePath_NoArtifactProtocolKeepsSplitDeliveryAndMarkerFreeHistory(t *testing.T) {
	transport := &recordingRichTransport{ids: map[int]string{0: "part-1", 1: "part-2"}}
	bot := newRichDeliveryTestBot(t, transport)
	store := new(testutil.MockStorage)
	bot.msgRepo = store
	path := &responsePath{
		bot: bot, logger: bot.logger, userID: storage.ScopeID("user"),
		convID: "123", replyTo: "42", richMode: config.TelegramRichMessagesSend,
	}
	var persisted storage.Message
	store.On("AddMessageToHistory", storage.ScopeID("user"), mock.Anything).Run(func(args mock.Arguments) {
		persisted = args.Get(1).(storage.Message)
	}).Return(nil).Once()
	store.On("SetReplyTransportID", storage.ScopeID("user"), "part-1").Return(nil).Once()
	content := "# First\n\n###SPLIT###\n\n###MEDIA:99###\n\n## Second"
	ctx, span := otel.Tracer("test").Start(context.Background(), "final")

	ok := path.sendFinalAndPersist(ctx, span, content, nil)
	span.End()

	require.True(t, ok)
	require.Len(t, transport.responses, 2, "SPLIT remains a real persistent-message boundary")
	assert.Equal(t, []string{"part-1", "part-2"}, path.deliveredMessageIDs)
	assert.Contains(t, transport.responses[0].Text, "<h1>First</h1>")
	assert.NotContains(t, transport.responses[0].Text, "Second")
	assert.Contains(t, transport.responses[1].Text, "<h2>Second</h2>")
	for _, response := range transport.responses {
		assert.Equal(t, ResponseFormatRichHTML, response.Format)
		assert.NotContains(t, response.Text, "###SPLIT###")
		assert.NotContains(t, response.Text, "###MEDIA")
	}
	assert.Equal(t, "# First ## Second", strings.Join(strings.Fields(persisted.Content), " "))
	assert.NotContains(t, persisted.Content, "###SPLIT###")
	assert.NotContains(t, persisted.Content, "###MEDIA")
	store.AssertExpectations(t)
}

type generatedLayoutTransportCall struct {
	kind  string
	text  OutgoingResponse
	media OutgoingMedia
	rich  OutgoingRichMedia
}

type generatedLayoutOrderedTransport struct {
	stubTransport
	calls   []generatedLayoutTransportCall
	richErr error
	nextID  int
}

func newGeneratedLayoutOrderedTransport() *generatedLayoutOrderedTransport {
	return &generatedLayoutOrderedTransport{stubTransport: stubTransport{kind: transportTelegram}}
}

func (t *generatedLayoutOrderedTransport) nextMessageID(kind string) string {
	t.nextID++
	return fmt.Sprintf("%s-%d", kind, t.nextID)
}

func (t *generatedLayoutOrderedTransport) SendRichMedia(_ context.Context, media OutgoingRichMedia) (string, error) {
	t.calls = append(t.calls, generatedLayoutTransportCall{kind: "rich_media", rich: media})
	if t.richErr != nil {
		return "", t.richErr
	}
	return t.nextMessageID("rich-media"), nil
}

func (t *generatedLayoutOrderedTransport) SendText(_ context.Context, response OutgoingResponse) (string, error) {
	t.calls = append(t.calls, generatedLayoutTransportCall{kind: "text", text: response})
	return t.nextMessageID("text"), nil
}

func (t *generatedLayoutOrderedTransport) SendTextPersistent(ctx context.Context, response OutgoingResponse) (string, error) {
	return t.SendText(ctx, response)
}

func (t *generatedLayoutOrderedTransport) SendMedia(_ context.Context, media OutgoingMedia) (string, error) {
	t.calls = append(t.calls, generatedLayoutTransportCall{kind: "media", media: media})
	return t.nextMessageID("media"), nil
}

func (t *generatedLayoutOrderedTransport) SendMediaPersistent(_ context.Context, media OutgoingMedia) (persistentSendResult, error) {
	t.calls = append(t.calls, generatedLayoutTransportCall{kind: "media", media: media})
	ids := make([]string, len(media.Items))
	for i := range ids {
		ids[i] = t.nextMessageID("media")
	}
	return persistentSendResult{MessageIDs: ids}, nil
}

func TestExecuteGeneratedMediaLayout_FormatFallbackKeepsTextMediaTextOrder(t *testing.T) {
	transport := newGeneratedLayoutOrderedTransport()
	transport.richErr = errors.Join(ErrRichMessageRejected, &telegram.APIError{
		Code: 400, Description: "Bad Request: RICH_MESSAGE_MEDIA_INVALID",
	})
	backing := &recordingTransport{}
	bot, _, userID := newGeneratedDeliveryTestBot(t, backing)
	bot.transport = transport
	bot.cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
	bot.cfg.Agents.ImageGenerator.DocumentThresholdBytes = 0
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	items := generatedV2PhotoItems(1)
	source := "Before text.\n\n###MEDIA:1###\n\nAfter text."

	planned, ok, reason := bot.planGeneratedRichDelivery(
		context.Background(), path, source, items, len(items),
	)
	require.True(t, ok, "fallback reason: %s", reason)
	require.Len(t, planned.plan.Operations, 1)
	require.Len(t, planned.plan.Operations[0].formatFallback, 3)

	result := bot.executeDeliveryPlan(context.Background(), planned.plan)

	require.NoError(t, result.err)
	assert.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Equal(t, richMetricPathAPIFallback, result.metricPath)
	require.Len(t, transport.calls, 4)
	assert.Equal(t, []string{"rich_media", "text", "media", "text"}, []string{
		transport.calls[0].kind,
		transport.calls[1].kind,
		transport.calls[2].kind,
		transport.calls[3].kind,
	})
	assert.Contains(t, transport.calls[1].text.Text, "Before text.")
	assert.Equal(t, path.replyTo, transport.calls[1].text.ReplyTo)
	require.Len(t, transport.calls[2].media.Items, 1)
	assert.Equal(t, items[0].Filename, transport.calls[2].media.Items[0].Filename)
	assert.Empty(t, transport.calls[2].media.ReplyTo)
	assert.Contains(t, transport.calls[3].text.Text, "After text.")
	assert.Empty(t, transport.calls[3].text.ReplyTo)
	for _, call := range transport.calls[1:] {
		assert.NotContains(t, call.text.Text, "###MEDIA")
		assert.NotContains(t, call.media.Caption, "###MEDIA")
	}
}

func TestPlanGeneratedMediaLayout_SizeAloneKeepsOnlyDirectedPreviews(t *testing.T) {
	bot, path := generatedV2Planner(t)
	bot.cfg.Agents.ImageGenerator.DocumentThresholdBytes = len(generatedTestPNG) - 1
	items := generatedV2PhotoItems(2)
	source := "# First\n\n###MEDIA:2###\n\n###SPLIT###\n\n# Second\n\n###MEDIA:1###"

	planned, ok, reason := bot.planGeneratedRichDelivery(context.Background(), path, source, items, len(items))

	require.True(t, ok, "fallback reason: %s", reason)
	require.Len(t, planned.plan.Operations, 2)
	wantKinds := []persistentOperationKind{
		persistentOperationRichMedia,
		persistentOperationRichMedia,
	}
	for i, want := range wantKinds {
		assert.Equal(t, want, planned.plan.Operations[i].Kind, "operation %d", i)
	}
	firstRich := planned.plan.Operations[0].RichMedia
	secondRich := planned.plan.Operations[1].RichMedia
	require.NotNil(t, firstRich)
	require.NotNil(t, secondRich)
	assert.Equal(t, []string{items[1].Filename}, generatedLayoutFilenames(firstRich.Items))
	assert.Equal(t, []string{items[0].Filename}, generatedLayoutFilenames(secondRich.Items))
	assert.Equal(t, OutgoingMediaWireKindPhoto, firstRich.Items[0].WireKind)
	assert.Equal(t, OutgoingMediaWireKindPhoto, secondRich.Items[0].WireKind)
	assert.Equal(t, path.replyTo, firstRich.ReplyTo)
	assert.Empty(t, secondRich.ReplyTo)
	require.NoError(t, planned.plan.validate())
}

func TestGeneratedMediaLayout_MissingArtifactSlotKeepsOrdinalAndHistoryIsMarkerFree(t *testing.T) {
	transport := &recordingTransport{richMediaID: "rich-directed"}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	bot.cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	bot.fileStorage.(*fakeFileStorage).blobs["gen/third.png"] = append([]byte(nil), generatedTestPNG...)
	store.On("GetArtifact", userID, int64(42)).Return(&storage.Artifact{
		ID: 42, UserID: userID, FilePath: "gen/cat.png", OriginalName: "cat.png", MimeType: "image/png",
	}, nil).Once()
	store.On("GetArtifact", userID, int64(43)).Return(nil, nil).Once()
	store.On("GetArtifact", userID, int64(44)).Return(&storage.Artifact{
		ID: 44, UserID: userID, FilePath: "gen/third.png", OriginalName: "third.png", MimeType: "image/png",
	}, nil).Once()
	var persisted storage.Message
	store.On("AddMessageToHistory", userID, mock.Anything).Run(func(args mock.Arguments) {
		persisted = args.Get(1).(storage.Message)
	}).Return(nil).Once()
	store.On("SetReplyTransportID", userID, "rich-directed").Return(nil).Once()
	store.On("GetRecentHistory", userID, 1).Return([]storage.Message{{ID: 9}}, nil).Once()
	store.On("UpdateMessageID", userID, int64(42), int64(9)).Return(nil).Once()
	store.On("UpdateMessageID", userID, int64(44), int64(9)).Return(nil).Once()
	source := "Before.\n\n###MEDIA:3###\n\nMiddle.\n\n###MEDIA:1###\n\nAfter."

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, source, []int64{42, 43, 44}, bot.logger,
	)

	require.Equal(t, richDeliveryConfirmed, result.outcome, "delivery error: %v", result.err)
	require.True(t, result.persisted)
	require.Len(t, transport.richMedia, 1)
	assert.Equal(t, []string{"third.png", "cat.png"}, generatedLayoutFilenames(transport.richMedia[0].Items))
	assert.Equal(t, []int{3, 1}, generatedLayoutOrdinals(transport.richMedia[0].Items))
	assert.NotContains(t, strings.Join(transport.richMedia[0].HTMLParts, ""), "999")
	assert.NotContains(t, strings.ToLower(strings.Join(transport.richMedia[0].HTMLParts, "")), "artifact")
	assert.Contains(t, persisted.Content, "🎨 cat.png (artifact:42)")
	assert.Contains(t, persisted.Content, "🎨 third.png (artifact:44)")
	assert.Contains(t, persisted.Content, "Before.")
	assert.Contains(t, persisted.Content, "Middle.")
	assert.NotContains(t, persisted.Content, "artifact:999")
	assert.Contains(t, persisted.Content, "After.")
	assert.NotContains(t, persisted.Content, "###MEDIA")
	assert.NotContains(t, persisted.Content, "###SPLIT")
	store.AssertNotCalled(t, "UpdateMessageID", userID, int64(43), mock.Anything)
	store.AssertExpectations(t)
}

func TestGeneratedMediaLayout_AllArtifactsMissingSendsAndPersistsMarkerFreeText(t *testing.T) {
	transport := newGeneratedLayoutOrderedTransport()
	backing := &recordingTransport{}
	bot, store, userID := newGeneratedDeliveryTestBot(t, backing)
	bot.transport = transport
	bot.cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	store.On("GetArtifact", userID, int64(42)).Return(nil, nil).Once()
	var persisted storage.Message
	store.On("AddMessageToHistory", userID, mock.Anything).Run(func(args mock.Arguments) {
		persisted = args.Get(1).(storage.Message)
	}).Return(nil).Once()
	source := "# Before\n\n###MEDIA:1###\n\n###SPLIT###\n\n## After"

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, source, []int64{42}, bot.logger,
	)

	require.Equal(t, richDeliveryConfirmed, result.outcome, "delivery error: %v", result.err)
	assert.True(t, result.persisted)
	assert.Equal(t, 2, result.attempts, "the explicit split remains a delivery boundary")
	require.Len(t, transport.calls, 2)
	for _, call := range transport.calls {
		assert.Equal(t, "text", call.kind)
		assert.Equal(t, ResponseFormatRichHTML, call.text.Format)
		assert.NotContains(t, call.text.Text, "###MEDIA")
		assert.NotContains(t, call.text.Text, "###SPLIT")
	}
	assert.Contains(t, transport.calls[0].text.Text, "Before")
	assert.Contains(t, transport.calls[1].text.Text, "After")
	assert.Empty(t, transport.calls[0].media.Items)
	assert.Equal(t, "# Before ## After", strings.Join(strings.Fields(persisted.Content), " "))
	assert.NotContains(t, persisted.Content, "###MEDIA")
	assert.NotContains(t, persisted.Content, "###SPLIT")
	store.AssertNotCalled(t, "GetRecentHistory", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "UpdateMessageID", mock.Anything, mock.Anything, mock.Anything)
	store.AssertExpectations(t)
}

func generatedLayoutFilenames(items []OutgoingMediaItem) []string {
	values := make([]string, len(items))
	for i := range items {
		values[i] = items[i].Filename
	}
	return values
}

func generatedLayoutOrdinals(items []OutgoingMediaItem) []int {
	values := make([]int, len(items))
	for i := range items {
		values[i] = items[i].SourceOrdinal
	}
	return values
}

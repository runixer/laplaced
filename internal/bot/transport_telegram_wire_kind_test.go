package bot

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"
)

type wireKindRecordingTransport struct {
	recordingRichTransport
	media []OutgoingMedia
}

func (t *wireKindRecordingTransport) SendMediaPersistent(_ context.Context, media OutgoingMedia) (persistentSendResult, error) {
	t.media = append(t.media, media)
	return persistentSendResult{MessageIDs: []string{"sent"}}, nil
}

func TestTelegramTransport_SendMediaPersistentExplicitPhotoIgnoresLegacyThreshold(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.Agents.ImageGenerator.DocumentThresholdBytes = 1
	api := new(testutil.MockBotAPI)
	api.On("SendPhoto", mock.Anything, mock.MatchedBy(func(req telegram.SendPhotoRequest) bool {
		return req.PhotoFilename == "large.png" && bytes.Equal(req.PhotoData, generatedTestPNG)
	})).Return(&telegram.Message{MessageID: 101}, nil).Once()
	transport := NewTelegramTransport(api, cfg, testutil.TestTranslator(t), testutil.TestLogger())

	result, err := transport.SendMediaPersistent(context.Background(), OutgoingMedia{
		ConversationID: "123",
		Items: []OutgoingMediaItem{{
			Data: generatedTestPNG, Filename: "large.png", MIME: "image/png",
			WireKind: OutgoingMediaWireKindPhoto,
			// A stale legacy hint must not override the exact V2 presentation.
			AsDocument: true,
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"101"}, result.MessageIDs)
	api.AssertNotCalled(t, "SendDocument", mock.Anything, mock.Anything)
	api.AssertExpectations(t)
}

func TestTelegramTransport_SendMediaPersistentExplicitDocumentPreservesBytes(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.Agents.ImageGenerator.DocumentThresholdBytes = 1 << 20
	original := []byte("%PDF-1.7\nexact-original\x00bytes")
	api := new(testutil.MockBotAPI)
	api.On("SendDocument", mock.Anything, mock.MatchedBy(func(req telegram.SendDocumentRequest) bool {
		return req.Filename == "report.pdf" && bytes.Equal(req.Data, original)
	})).Return(&telegram.Message{MessageID: 202}, nil).Once()
	transport := NewTelegramTransport(api, cfg, testutil.TestTranslator(t), testutil.TestLogger())

	result, err := transport.SendMediaPersistent(context.Background(), OutgoingMedia{
		ConversationID: "123",
		Items: []OutgoingMediaItem{{
			Data: original, Filename: "report.pdf", MIME: "application/pdf",
			WireKind: OutgoingMediaWireKindDocument,
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"202"}, result.MessageIDs)
	api.AssertNotCalled(t, "SendPhoto", mock.Anything, mock.Anything)
	api.AssertExpectations(t)
}

func TestTelegramTransport_SendMediaPersistentExplicitDocumentAlbumReturnsEveryID(t *testing.T) {
	api := new(testutil.MockBotAPI)
	api.On("SendMediaGroupDocuments", mock.Anything, mock.MatchedBy(func(req telegram.SendMediaGroupDocumentsRequest) bool {
		return len(req.Media) == 2 &&
			req.Media[0].Filename == "one.pdf" && bytes.Equal(req.Media[0].Data, []byte("one")) &&
			req.Media[1].Filename == "two.pdf" && bytes.Equal(req.Media[1].Data, []byte("two"))
	})).Return([]telegram.Message{{MessageID: 211}, {MessageID: 212}}, nil).Once()
	transport := NewTelegramTransport(api, testutil.TestConfig(), testutil.TestTranslator(t), testutil.TestLogger())

	result, err := transport.SendMediaPersistent(context.Background(), OutgoingMedia{
		ConversationID: "123",
		Items: []OutgoingMediaItem{
			{Data: []byte("one"), Filename: "one.pdf", MIME: "application/pdf", WireKind: OutgoingMediaWireKindDocument},
			{Data: []byte("two"), Filename: "two.pdf", MIME: "application/pdf", WireKind: OutgoingMediaWireKindDocument},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"211", "212"}, result.MessageIDs)
	api.AssertNotCalled(t, "SendDocument", mock.Anything, mock.Anything)
	api.AssertExpectations(t)
}

func TestTelegramTransport_SendMediaPersistentZeroWireKindKeepsLegacyThreshold(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.Agents.ImageGenerator.DocumentThresholdBytes = 1
	api := new(testutil.MockBotAPI)
	api.On("SendDocument", mock.Anything, mock.MatchedBy(func(req telegram.SendDocumentRequest) bool {
		return req.Filename == "legacy.png" && bytes.Equal(req.Data, generatedTestPNG)
	})).Return(&telegram.Message{MessageID: 303}, nil).Once()
	transport := NewTelegramTransport(api, cfg, testutil.TestTranslator(t), testutil.TestLogger())

	result, err := transport.SendMediaPersistent(context.Background(), OutgoingMedia{
		ConversationID: "123",
		Items: []OutgoingMediaItem{{
			Data: generatedTestPNG, Filename: "legacy.png", MIME: "image/png",
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"303"}, result.MessageIDs)
	api.AssertNotCalled(t, "SendPhoto", mock.Anything, mock.Anything)
	api.AssertExpectations(t)
}

func TestTelegramTransport_SendMediaPersistentRejectsMixedExplicitWireKindsBeforeNetwork(t *testing.T) {
	api := new(testutil.MockBotAPI)
	transport := NewTelegramTransport(api, testutil.TestConfig(), testutil.TestTranslator(t), testutil.TestLogger())

	result, err := transport.SendMediaPersistent(context.Background(), OutgoingMedia{
		ConversationID: "123",
		Items: []OutgoingMediaItem{
			{Data: generatedTestPNG, Filename: "preview.png", MIME: "image/png", WireKind: OutgoingMediaWireKindPhoto},
			{Data: generatedTestPNG, Filename: "original.png", MIME: "image/png", WireKind: OutgoingMediaWireKindDocument},
		},
	})

	require.ErrorContains(t, err, "mixes photo and document")
	assert.Empty(t, result.MessageIDs)
	api.AssertNotCalled(t, "SendPhoto", mock.Anything, mock.Anything)
	api.AssertNotCalled(t, "SendDocument", mock.Anything, mock.Anything)
	api.AssertNotCalled(t, "SendMediaGroup", mock.Anything, mock.Anything)
	api.AssertNotCalled(t, "SendMediaGroupDocuments", mock.Anything, mock.Anything)
}

func TestExecuteDeliveryPlanRejectsMixedExplicitWireKindsBeforeLedgerSend(t *testing.T) {
	transport := &wireKindRecordingTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	result := bot.executeDeliveryPlan(context.Background(), deliveryPlan{Operations: []deliveryOperation{{
		Kind: persistentOperationMedia,
		Media: &OutgoingMedia{
			ConversationID: "123",
			Items: []OutgoingMediaItem{
				{Data: generatedTestPNG, Filename: "preview.png", MIME: "image/png", WireKind: OutgoingMediaWireKindPhoto},
				{Data: generatedTestPNG, Filename: "original.png", MIME: "image/png", WireKind: OutgoingMediaWireKindDocument},
			},
		},
	}}})

	require.ErrorContains(t, result.err, "mixes photo and document")
	assert.Equal(t, richDeliveryRejected, result.outcome)
	assert.Zero(t, result.attempts)
	assert.Empty(t, transport.media)
}

func TestValidateTelegramDocumentSizeBoundary(t *testing.T) {
	require.NoError(t, validateTelegramDocumentSize(telegramDocumentMaxBytes))
	require.ErrorContains(t, validateTelegramDocumentSize(telegramDocumentMaxBytes+1), "limit is")
}

func TestValidatePersistentMediaItemRejectsUnsafeMetadata(t *testing.T) {
	base := OutgoingMediaItem{
		Data: []byte("data"), Filename: "safe.bin", MIME: "application/octet-stream",
		WireKind: OutgoingMediaWireKindDocument,
	}
	tests := []struct {
		name string
		item OutgoingMediaItem
		want string
	}{
		{name: "empty data", item: func() OutgoingMediaItem { v := base; v.Data = nil; return v }(), want: "data is empty"},
		{name: "empty filename", item: func() OutgoingMediaItem { v := base; v.Filename = " "; return v }(), want: "required"},
		{name: "filename newline", item: func() OutgoingMediaItem { v := base; v.Filename = "bad\r\nname.bin"; return v }(), want: "control characters"},
		{name: "MIME newline", item: func() OutgoingMediaItem { v := base; v.MIME = "application/pdf\r\nx: y"; return v }(), want: "control characters"},
		{name: "malformed MIME", item: func() OutgoingMediaItem { v := base; v.MIME = "image/[png"; return v }(), want: "invalid MIME"},
		{name: "unknown wire kind", item: func() OutgoingMediaItem { v := base; v.WireKind = "video"; return v }(), want: "unsupported media wire kind"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorContains(t, validatePersistentMediaItem(tt.item), tt.want)
		})
	}
}

func TestComposeOutgoingRichMediaRequiresExplicitPhotoWireKind(t *testing.T) {
	media := func(kind OutgoingMediaWireKind) OutgoingRichMedia {
		return OutgoingRichMedia{
			ConversationID: "123", HTMLParts: []string{"", ""}, MediaGroupSizes: []int{1},
			Items: []OutgoingMediaItem{{
				Data: generatedTestPNG, Filename: "photo.png", MIME: "image/png", WireKind: kind,
			}},
		}
	}

	_, err := composeOutgoingRichMedia(media(OutgoingMediaWireKindLegacy))
	require.ErrorContains(t, err, "must explicitly select photo")
	_, err = composeOutgoingRichMedia(media(OutgoingMediaWireKindDocument))
	require.ErrorContains(t, err, "must explicitly select photo")
	_, err = composeOutgoingRichMedia(media(OutgoingMediaWireKindPhoto))
	require.NoError(t, err)
}

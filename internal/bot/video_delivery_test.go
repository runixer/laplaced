package bot

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/artifactdelivery"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"
)

var testMP4 = []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom-clip")

func TestTelegramTransport_SendMediaPersistentVideoUsesSendVideo(t *testing.T) {
	api := new(testutil.MockBotAPI)
	api.On("SendVideo", mock.Anything, mock.MatchedBy(func(req telegram.SendVideoRequest) bool {
		return req.Filename == "clip.mp4" && req.MIME == "video/mp4" && bytes.Equal(req.Data, testMP4) && req.ReplyToMessageID == 5
	})).Return(&telegram.Message{MessageID: 303}, nil).Once()
	transport := NewTelegramTransport(api, testutil.TestConfig(), testutil.TestTranslator(t), testutil.TestLogger())

	result, err := transport.SendMediaPersistent(context.Background(), OutgoingMedia{
		ConversationID: "123",
		ReplyTo:        "5",
		Items:          []OutgoingMediaItem{{Data: testMP4, Filename: "clip.mp4", MIME: "video/mp4", WireKind: OutgoingMediaWireKindVideo}},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"303"}, result.MessageIDs)
	api.AssertNotCalled(t, "SendPhoto", mock.Anything, mock.Anything)
	api.AssertNotCalled(t, "SendDocument", mock.Anything, mock.Anything)
	api.AssertExpectations(t)
}

func TestTelegramTransport_SendMediaCompatibilityRoutesLegacyVideo(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.Agents.ImageGenerator.DocumentThresholdBytes = 1 << 20 // the clip is far below it
	api := new(testutil.MockBotAPI)
	api.On("SendVideo", mock.Anything, mock.Anything).Return(&telegram.Message{MessageID: 404}, nil).Once()
	transport := NewTelegramTransport(api, cfg, testutil.TestTranslator(t), testutil.TestLogger())

	_, err := transport.SendMedia(context.Background(), OutgoingMedia{
		ConversationID: "123",
		Items:          []OutgoingMediaItem{{Data: testMP4, Filename: "clip.mp4", MIME: "video/mp4"}},
	})
	require.NoError(t, err)
	api.AssertNotCalled(t, "SendPhoto", mock.Anything, mock.Anything)
	api.AssertExpectations(t)
}

func TestResolvedTelegramMediaWireKind_Video(t *testing.T) {
	video := OutgoingMediaItem{Data: testMP4, MIME: "video/mp4"}
	assert.Equal(t, OutgoingMediaWireKindVideo, resolvedTelegramMediaWireKind(video, 1))
	video.AsDocument = true
	assert.Equal(t, OutgoingMediaWireKindDocument, resolvedTelegramMediaWireKind(video, 1))
}

func TestValidateTelegramPersistentMediaBatch_Video(t *testing.T) {
	clip := OutgoingMediaItem{Data: testMP4, Filename: "a.mp4", MIME: "video/mp4", WireKind: OutgoingMediaWireKindVideo}
	kind, err := validateTelegramPersistentMediaBatch([]OutgoingMediaItem{clip}, 0)
	require.NoError(t, err)
	assert.Equal(t, OutgoingMediaWireKindVideo, kind)

	_, err = validateTelegramPersistentMediaBatch([]OutgoingMediaItem{clip, clip}, 0)
	assert.ErrorContains(t, err, "exactly one item")

	notVideo := clip
	notVideo.MIME = "image/png"
	_, err = validateTelegramPersistentMediaBatch([]OutgoingMediaItem{notVideo}, 0)
	assert.ErrorContains(t, err, "non-video MIME")
}

func loadedVideo(id int64, ordinal int, mode artifactdelivery.Mode) loadedArtifactDelivery {
	return loadedArtifactDelivery{
		loadedArtifact: loadedArtifact{
			artifact: &storage.Artifact{ID: id, MimeType: "video/mp4", OriginalName: "generated_x_1.mp4"},
			data:     testMP4,
			ordinal:  ordinal,
		},
		mode: mode,
	}
}

func loadedPNG(id int64, ordinal int) loadedArtifactDelivery {
	return loadedArtifactDelivery{
		loadedArtifact: loadedArtifact{
			artifact: &storage.Artifact{ID: id, MimeType: "image/png", OriginalName: "generated_x_1.png"},
			data:     generatedTestPNG,
			ordinal:  ordinal,
		},
		mode: artifactdelivery.ModePreview,
	}
}

func TestMaterializeArtifactPresentations_GeneratedVideoGoesToTailWithoutMediaOrdinal(t *testing.T) {
	previews, tail, refs, previewCount, originals := materializeArtifactPresentations(
		[]loadedArtifactDelivery{loadedVideo(1, 1, artifactdelivery.ModePreview), loadedPNG(2, 2)},
		nil,
	)
	require.Len(t, previews, 1, "only the image is a rich photo preview")
	assert.Equal(t, 1, previews[0].SourceOrdinal, "the video must not consume MEDIA:1")
	assert.Equal(t, 1, previewCount)
	assert.Equal(t, 0, originals)
	require.Len(t, tail, 1)
	assert.Equal(t, OutgoingMediaWireKindVideo, tail[0].WireKind)
	assert.Equal(t, "generated-video-1.mp4", tail[0].Filename)
	require.Len(t, refs, 2)
	assert.Equal(t, int64(2), refs[0].artifactID, "preview references come first")
	assert.Equal(t, int64(1), refs[1].artifactID)
	assert.Equal(t, artifactdelivery.ModePreview, refs[1].mode)
}

func TestMaterializeArtifactPresentations_VideoOriginalAndStoredAuto(t *testing.T) {
	_, tail, _, _, originals := materializeArtifactPresentations(
		[]loadedArtifactDelivery{loadedVideo(1, 1, artifactdelivery.ModeOriginal)},
		[]loadedArtifactDelivery{loadedVideo(3, 1, artifactdelivery.ModeAuto)},
	)
	require.Len(t, tail, 2)
	assert.Equal(t, OutgoingMediaWireKindDocument, tail[0].WireKind)
	assert.Equal(t, 1, originals)
	assert.Equal(t, OutgoingMediaWireKindVideo, tail[1].WireKind, "a stored clip in auto mode plays inline")
}

func TestGeneratedMediaOperationsStable_IsolatesVideos(t *testing.T) {
	path := &responsePath{convID: "123"}
	photo := OutgoingMediaItem{Data: generatedTestPNG, Filename: "a.png", MIME: "image/png", WireKind: OutgoingMediaWireKindPhoto}
	clip := OutgoingMediaItem{Data: testMP4, Filename: "a.mp4", MIME: "video/mp4", WireKind: OutgoingMediaWireKindVideo}

	ops := generatedMediaOperationsStable(path, []OutgoingMediaItem{photo, clip, photo, photo}, 0)
	require.Len(t, ops, 3)
	assert.Len(t, ops[0].Media.Items, 1)
	require.Len(t, ops[1].Media.Items, 1)
	assert.Equal(t, OutgoingMediaWireKindVideo, ops[1].Media.Items[0].WireKind)
	assert.Len(t, ops[2].Media.Items, 2)
}

package bot

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"
)

var generatedTestPNG, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
)

// fakeFileStorage is a minimal in-memory files.Storage for delivery tests.
type fakeFileStorage struct {
	blobs map[string][]byte
}

func (f *fakeFileStorage) SaveFile(context.Context, storage.ScopeID, io.Reader, string) (*files.SavedFile, error) {
	return nil, nil
}
func (f *fakeFileStorage) ReadFile(_ context.Context, key string) ([]byte, error) {
	return f.blobs[key], nil
}
func (f *fakeFileStorage) DeleteFile(context.Context, string) error { return nil }

// recordingTransport captures media and follow-up text sends and lets tests
// inject terminal outcomes without standing up a Telegram client.
type recordingTransport struct {
	stubTransport
	media                []OutgoingMedia
	mediaID              string
	mediaErr             error
	richMedia            []OutgoingRichMedia
	richMediaID          string
	richMediaErr         error
	beforeRichMedia      func()
	text                 []OutgoingResponse
	textErrors           map[int]error
	beforeMediaSend      func()
	persistentMediaCalls int
}

func (*recordingTransport) Kind() string { return transportTelegram }

func (r *recordingTransport) SendRichMedia(_ context.Context, m OutgoingRichMedia) (string, error) {
	if r.beforeRichMedia != nil {
		r.beforeRichMedia()
	}
	r.richMedia = append(r.richMedia, m)
	return r.richMediaID, r.richMediaErr
}

func (r *recordingTransport) SendMedia(_ context.Context, m OutgoingMedia) (string, error) {
	if r.beforeMediaSend != nil {
		r.beforeMediaSend()
	}
	r.media = append(r.media, m)
	return r.mediaID, r.mediaErr
}

func (r *recordingTransport) SendText(_ context.Context, response OutgoingResponse) (string, error) {
	call := len(r.text)
	r.text = append(r.text, response)
	if err := r.textErrors[call]; err != nil {
		return "", err
	}
	return "text-message-1", nil
}

func (r *recordingTransport) SendTextPersistent(ctx context.Context, response OutgoingResponse) (string, error) {
	return r.SendText(ctx, response)
}

func (r *recordingTransport) SendMediaPersistent(ctx context.Context, media OutgoingMedia) (persistentSendResult, error) {
	r.persistentMediaCalls++
	id, err := r.SendMedia(ctx, media)
	if err != nil {
		return persistentSendResult{MessageIDs: []string{id}}, err
	}
	ids := make([]string, len(media.Items))
	for i := range ids {
		ids[i] = id
		if i > 0 {
			ids[i] = fmt.Sprintf("%s-%d", id, i+1)
		}
	}
	return persistentSendResult{MessageIDs: ids}, nil
}

func newGeneratedDeliveryTestBot(t *testing.T, transport *recordingTransport) (*Bot, *testutil.MockStorage, storage.ScopeID) {
	t.Helper()
	userID := storage.ScopeID("123")
	store := new(testutil.MockStorage)
	store.On("GetPrivacyMode", mock.Anything).Return(false, nil).Maybe()
	bot := &Bot{
		cfg:          testutil.TestConfig(),
		logger:       testutil.TestLogger(),
		translator:   testutil.TestTranslator(t),
		msgRepo:      store,
		artifactRepo: store,
		fileStorage:  &fakeFileStorage{blobs: map[string][]byte{"gen/cat.png": append([]byte(nil), generatedTestPNG...)}},
		transport:    transport,
		renderer:     NewTelegramRenderer(testutil.TestLogger()),
	}
	return bot, store, userID
}

func generatedPath(bot *Bot, userID storage.ScopeID) *responsePath {
	return &responsePath{
		bot: bot, logger: bot.logger, userID: userID,
		convID: "123", replyTo: "1", richMode: config.TelegramRichMessagesOff,
	}
}

func expectGeneratedArtifact(store *testutil.MockStorage, userID storage.ScopeID) {
	store.On("GetArtifact", userID, int64(42)).Return(&storage.Artifact{
		ID: 42, UserID: userID, FilePath: "gen/cat.png",
		OriginalName: "cat.png", MimeType: "image/png",
	}, nil).Once()
}

type streamingRecordingTransport struct {
	*recordingTransport
}

func (*streamingRecordingTransport) Kind() string { return transportTelegram }
func (*streamingRecordingTransport) Capabilities() Capabilities {
	return Capabilities{SupportsRichMessages: true, SupportsStreaming: true}
}

func TestGeneratedMedia_PersistsAndLinksOnlyAfterConfirmedDelivery(t *testing.T) {
	historyInserted := false
	transport := &recordingTransport{mediaID: "media-7"}
	transport.beforeMediaSend = func() {
		assert.False(t, historyInserted, "history must not precede the persistent media send")
	}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	expectGeneratedArtifact(store, userID)
	store.On("AddMessageToHistory", userID, mock.MatchedBy(func(message storage.Message) bool {
		return message.Role == "assistant" &&
			strings.Contains(message.Content, "🎨 cat.png (artifact:42)") &&
			strings.Contains(message.Content, "Here is the image")
	})).Run(func(mock.Arguments) { historyInserted = true }).Return(nil).Once()
	store.On("SetReplyTransportID", userID, "media-7").Return(nil).Once()
	store.On("GetRecentHistory", userID, 1).Return([]storage.Message{{ID: 9}}, nil).Once()
	store.On("UpdateMessageID", userID, int64(42), int64(9)).Return(nil).Once()

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), generatedPath(bot, userID), nil,
		"Here is the image", []int64{42}, bot.logger,
	)

	assert.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.True(t, result.persisted)
	assert.Equal(t, "media-7", result.primaryMessageID)
	assert.Equal(t, 1, result.attempts)
	require.Len(t, transport.media, 1)
	require.Len(t, transport.media[0].Items, 1)
	assert.Equal(t, generatedTestPNG, transport.media[0].Items[0].Data)
	store.AssertExpectations(t)
}

func TestGeneratedMedia_OffModeKeepsCompatibilityMediaEnvelope(t *testing.T) {
	transport := &recordingTransport{mediaID: "legacy-batch-primary"}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesOff

	artifactIDs := make([]int64, 11)
	for i := range artifactIDs {
		id := int64(i + 1)
		artifactIDs[i] = id
		mimeType := "image/png"
		if i == 0 {
			mimeType = "application/octet-stream"
		}
		store.On("GetArtifact", userID, id).Return(&storage.Artifact{
			ID: id, UserID: userID, FilePath: "gen/cat.png",
			OriginalName: fmt.Sprintf("generated-%02d.png", i+1), MimeType: mimeType,
		}, nil).Once()
	}
	store.On("AddMessageToHistory", userID, mock.Anything).Return(nil).Once()
	store.On("SetReplyTransportID", userID, "legacy-batch-primary").Return(nil).Once()
	store.On("GetRecentHistory", userID, 1).Return([]storage.Message{{ID: 9}}, nil).Once()
	for _, id := range artifactIDs {
		store.On("UpdateMessageID", userID, id, int64(9)).Return(nil).Once()
	}

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, "Compatibility envelope", artifactIDs, bot.logger,
	)

	require.Equal(t, richDeliveryConfirmed, result.outcome, "delivery error: %v", result.err)
	require.Len(t, transport.media, 1)
	assert.Len(t, transport.media[0].Items, 11)
	assert.Zero(t, transport.persistentMediaCalls,
		"off/shadow path must leave mixed and >10 batching to compatibility SendMedia")
	store.AssertExpectations(t)
}

func TestGeneratedMedia_OffModeKeepsSplitOutOfCaptionAndHistory(t *testing.T) {
	transport := &recordingTransport{mediaID: "legacy-media"}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesOff
	expectGeneratedArtifact(store, userID)
	var persisted storage.Message
	store.On("AddMessageToHistory", userID, mock.Anything).Run(func(args mock.Arguments) {
		persisted = args.Get(1).(storage.Message)
	}).Return(nil).Once()
	store.On("SetReplyTransportID", userID, "legacy-media").Return(nil).Once()
	store.On("GetRecentHistory", userID, 1).Return([]storage.Message{{ID: 9}}, nil).Once()
	store.On("UpdateMessageID", userID, int64(42), int64(9)).Return(nil).Once()

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil,
		"# First\n\n###SPLIT###\n\n## Second", []int64{42}, bot.logger,
	)

	require.Equal(t, richDeliveryConfirmed, result.outcome, "delivery error: %v", result.err)
	assert.Equal(t, 2, result.attempts)
	require.Len(t, transport.media, 1)
	assert.Contains(t, transport.media[0].Caption, "First")
	assert.NotContains(t, transport.media[0].Caption, "Second")
	assert.NotContains(t, transport.media[0].Caption, richSplitDelimiter)
	require.Len(t, transport.text, 1)
	assert.Contains(t, transport.text[0].Text, "Second")
	assert.NotContains(t, transport.text[0].Text, richSplitDelimiter)
	assert.NotContains(t, persisted.Content, richSplitDelimiter)
	store.AssertExpectations(t)
}

func TestGeneratedMedia_LocalV2RejectionNotifiesWithBoundedGenericError(t *testing.T) {
	transport := &recordingTransport{richMediaID: "must-not-send"}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	expectGeneratedArtifact(store, userID)

	parts := make([]string, richMessageMaxParts+1)
	for i := range parts {
		parts[i] = fmt.Sprintf("# Part %d", i+1)
	}
	response := strings.Join(parts, "\n"+richSplitDelimiter+"\n")

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, response, []int64{42}, bot.logger,
	)

	require.Equal(t, richDeliveryRejected, result.outcome)
	require.Error(t, result.err)
	assert.Equal(t, 1, result.attempts, "only the bounded notification is sent")
	assert.Empty(t, transport.richMedia)
	assert.Empty(t, transport.media)
	require.Len(t, transport.text, 1)
	assert.NotContains(t, transport.text[0].Text, "Part 1")
	store.AssertExpectations(t)
}

func TestGeneratedMedia_UnloadableArtifactAndRejectedTextFallbackNotifies(t *testing.T) {
	transport := &recordingTransport{richMediaID: "must-not-send"}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	store.On("GetArtifact", userID, int64(42)).Return(nil, nil).Once()

	parts := make([]string, richMessageMaxParts+1)
	for i := range parts {
		parts[i] = fmt.Sprintf("# Missing artifact part %d", i+1)
	}
	response := strings.Join(parts, "\n"+richSplitDelimiter+"\n")

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, response, []int64{42}, bot.logger,
	)

	require.Equal(t, richDeliveryRejected, result.outcome)
	require.Error(t, result.err)
	assert.Equal(t, 1, result.attempts, "only the bounded notification is sent")
	assert.Empty(t, transport.richMedia)
	assert.Empty(t, transport.media)
	require.Len(t, transport.text, 1)
	assert.NotContains(t, transport.text[0].Text, "Missing artifact part 1")
	store.AssertExpectations(t)
}

func TestGeneratedMedia_RichModeSendsOneNativeMessageWithTrustedPhoto(t *testing.T) {
	transport := &recordingTransport{richMediaID: "rich-media-7"}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	expectGeneratedArtifact(store, userID)
	store.On("AddMessageToHistory", userID, mock.Anything).Return(nil).Once()
	store.On("SetReplyTransportID", userID, "rich-media-7").Return(nil).Once()
	store.On("GetRecentHistory", userID, 1).Return([]storage.Message{{ID: 9}}, nil).Once()
	store.On("UpdateMessageID", userID, int64(42), int64(9)).Return(nil).Once()
	source := "# Result\n\nFormula $x^2$.\n\n" +
		"[@victim](tg://user?id=123) bare @alice " +
		"![cat photo](https://evil.example/cat.jpg) [unsafe](javascript:alert(1))"

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, source, []int64{42}, bot.logger,
	)

	require.Equal(t, richDeliveryConfirmed, result.outcome, "delivery error: %v", result.err)
	assert.Equal(t, "rich-media-7", result.primaryMessageID)
	assert.Equal(t, 1, result.attempts)
	assert.Empty(t, transport.media)
	require.Len(t, transport.richMedia, 1)
	rich := transport.richMedia[0]
	require.Len(t, rich.Items, 1)
	assert.Equal(t, generatedTestPNG, rich.Items[0].Data)
	richBody := strings.Join(rich.HTMLParts, "")
	assert.Contains(t, richBody, "<h1>Result</h1>")
	assert.Contains(t, richBody, "<tg-math>x^2</tg-math>")
	assert.Contains(t, richBody, "cat photo")
	assert.Contains(t, richBody, "unsafe")
	assert.Contains(t, richBody, "@alice")
	assert.NotContains(t, strings.ToLower(richBody), "tg://")
	assert.NotContains(t, strings.ToLower(richBody), "javascript:")
	assert.NotContains(t, strings.ToLower(richBody), "<img")
	store.AssertExpectations(t)
}

func TestGeneratedMedia_RichDraftClosesBeforeNativeFinalAndCannotRevive(t *testing.T) {
	recorded := &recordingTransport{richMediaID: "rich-media-final"}
	transport := &streamingRecordingTransport{recorded}
	bot, store, userID := newGeneratedDeliveryTestBot(t, recorded)
	bot.transport = transport
	bot.cfg.Telegram.RichMessages.DraftStreamingEnabled = true
	bot.cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
	bot.cfg.Telegram.RichMessages.AllowedUserIDs = []int64{123}

	api := new(testutil.MockBotAPI)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return req.ChatID == 123 && req.DraftID == 42 &&
			req.RichMessage.SkipEntityDetection && strings.Contains(req.RichMessage.HTML, "<tg-thinking>")
	})).Return(nil).Once()
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, "partial")
	})).Return(nil).Once()
	tailCaughtUp := false
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, "partial") &&
			strings.Contains(req.RichMessage.HTML, "TERMINAL_MEDIA_TAIL")
	})).Run(func(mock.Arguments) {
		tailCaughtUp = true
	}).Return(nil).Once()
	bot.api = api

	expectGeneratedArtifact(store, userID)
	store.On("AddMessageToHistory", userID, mock.Anything).Return(nil).Once()
	store.On("SetReplyTransportID", userID, "rich-media-final").Return(nil).Once()
	store.On("GetRecentHistory", userID, 1).Return([]storage.Message{{ID: 9}}, nil).Once()
	store.On("UpdateMessageID", userID, int64(42), int64(9)).Return(nil).Once()

	path := bot.newResponsePath(
		context.Background(), userID, "123", true,
		"123", "", "42", bot.logger,
	)
	require.True(t, path.usesRichDraft())
	clock := newFakeClock()
	setRichDraftClock(path.richDraft, clock)
	path.streamDelta("partial")
	path.streamDelta(strings.Repeat("x", 80) + " TERMINAL_MEDIA_TAIL")

	recorded.beforeRichMedia = func() {
		assert.True(t, tailCaughtUp, "terminal catch-up must precede persistent multipart send")
		assert.False(t, path.usesRichDraft(), "draft must be terminal before persistent multipart send")
		assert.True(t, path.richDraftClosed)
		// A late SSE/tool callback racing with final delivery must not recreate
		// the preview or issue another draft update.
		path.streamDelta(" late-delta")
		path.streamStatus("generate_image", `{"prompt":"late-status"}`)
		path.streamRAG("late-rag")
		api.AssertNumberOfCalls(t, "SendRichMessageDraft", 3)
	}

	path.flushSinkBeforeMedia(context.Background(), "# Final")
	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, "# Final", []int64{42}, bot.logger,
	)

	require.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Equal(t, 1, result.attempts)
	require.Len(t, recorded.richMedia, 1)
	assert.Empty(t, recorded.media)
	assert.Equal(t, richDraftCatchupSent, path.richDraft.stats.terminalCatchup)
	path.streamDelta(" post-final")
	path.recordTelegramMetrics()
	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 3)
	api.AssertExpectations(t)
	store.AssertExpectations(t)
}

func TestGeneratedMedia_RichFormatRejectionFallsBackOnceToLegacyMedia(t *testing.T) {
	transport := &recordingTransport{
		richMediaErr: fmt.Errorf("%w: %w", ErrRichMessageRejected, &telegram.APIError{
			Code: 400, Description: "Bad Request: RICH_MESSAGE_MEDIA_INVALID",
		}),
		mediaID: "media-7",
	}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	expectGeneratedArtifact(store, userID)
	store.On("AddMessageToHistory", userID, mock.Anything).Return(nil).Once()
	store.On("SetReplyTransportID", userID, "media-7").Return(nil).Once()
	store.On("GetRecentHistory", userID, 1).Return([]storage.Message{{ID: 9}}, nil).Once()
	store.On("UpdateMessageID", userID, int64(42), int64(9)).Return(nil).Once()

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, "# Result\n\nFormula $x^2$.", []int64{42}, bot.logger,
	)

	require.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Equal(t, 2, result.attempts)
	require.Len(t, transport.richMedia, 1)
	require.Len(t, transport.media, 1)
	assert.Contains(t, transport.media[0].Caption, "<b>Result</b>")
	store.AssertExpectations(t)
}

func TestGeneratedMedia_RichUnknownFailureNeverResendsLegacy(t *testing.T) {
	transport := &recordingTransport{
		richMediaErr: errors.New("connection reset after request write"),
		mediaID:      "must-not-send",
	}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	expectGeneratedArtifact(store, userID)

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, "# Result", []int64{42}, bot.logger,
	)

	require.Equal(t, richDeliveryUnknown, result.outcome)
	assert.Equal(t, 1, result.attempts)
	require.Len(t, transport.richMedia, 1)
	assert.Empty(t, transport.media)
	store.AssertNotCalled(t, "AddMessageToHistory", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "SetReplyTransportID", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "GetRecentHistory", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "UpdateMessageID", mock.Anything, mock.Anything, mock.Anything)
	store.AssertExpectations(t)
}

func TestGeneratedMedia_RichEmptySuccessNeverResendsLegacy(t *testing.T) {
	transport := &recordingTransport{
		// A nil error without a stable id is an ambiguous success: Telegram may
		// have persisted the multipart request, so retrying as legacy can duplicate it.
		richMediaID: "",
		mediaID:     "must-not-send",
	}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	expectGeneratedArtifact(store, userID)

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, "# Result", []int64{42}, bot.logger,
	)

	require.Equal(t, richDeliveryUnknown, result.outcome)
	assert.Equal(t, 1, result.attempts)
	require.Len(t, transport.richMedia, 1)
	assert.Empty(t, transport.media)
	store.AssertNotCalled(t, "AddMessageToHistory", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "SetReplyTransportID", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "GetRecentHistory", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "UpdateMessageID", mock.Anything, mock.Anything, mock.Anything)
	store.AssertExpectations(t)
}

func TestGeneratedMedia_RichModeUsesNativePhotoAtDocumentThreshold(t *testing.T) {
	transport := &recordingTransport{richMediaID: "rich-media-boundary"}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	bot.cfg.Agents.ImageGenerator.DocumentThresholdBytes = len(generatedTestPNG)
	path := generatedPath(bot, userID)
	path.richMode = config.TelegramRichMessagesSend
	expectGeneratedArtifact(store, userID)
	store.On("AddMessageToHistory", userID, mock.Anything).Return(nil).Once()
	store.On("SetReplyTransportID", userID, "rich-media-boundary").Return(nil).Once()
	store.On("GetRecentHistory", userID, 1).Return([]storage.Message{{ID: 9}}, nil).Once()
	store.On("UpdateMessageID", userID, int64(42), int64(9)).Return(nil).Once()

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, "# Result", []int64{42}, bot.logger,
	)

	require.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Equal(t, 1, result.attempts)
	require.Len(t, transport.richMedia, 1)
	assert.Empty(t, transport.media)
	store.AssertExpectations(t)
}

func TestGeneratedMedia_V2GallerySplitAndHighResolutionSidecar(t *testing.T) {
	tests := []struct {
		name         string
		response     string
		artifactIDs  []int64
		threshold    int
		wantAttempts int
		wantGallery  int
		wantText     int
		wantSidecar  int
	}{
		{
			name:         "two generated images",
			response:     "# Album",
			artifactIDs:  []int64{42, 43},
			wantAttempts: 1,
			wantGallery:  2,
		},
		{
			name:         "explicit split",
			response:     "# First\n\n###SPLIT###\n\n## Second",
			artifactIDs:  []int64{42},
			wantAttempts: 2,
			wantGallery:  1,
			wantText:     1,
		},
		{
			name:         "document quality image",
			response:     "# Document",
			artifactIDs:  []int64{42},
			threshold:    4,
			wantAttempts: 2,
			wantGallery:  1,
			wantSidecar:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &recordingTransport{richMediaID: "rich-media-7", mediaID: "sidecar-8"}
			bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
			path := generatedPath(bot, userID)
			path.richMode = config.TelegramRichMessagesSend
			if tt.threshold > 0 {
				bot.cfg.Agents.ImageGenerator.DocumentThresholdBytes = tt.threshold
			}

			expectGeneratedArtifact(store, userID)
			if len(tt.artifactIDs) == 2 {
				bot.fileStorage.(*fakeFileStorage).blobs["gen/dog.png"] = append([]byte(nil), generatedTestPNG...)
				store.On("GetArtifact", userID, int64(43)).Return(&storage.Artifact{
					ID: 43, UserID: userID, FilePath: "gen/dog.png",
					OriginalName: "dog.png", MimeType: "image/png",
				}, nil).Once()
			}
			store.On("AddMessageToHistory", userID, mock.Anything).Return(nil).Once()
			store.On("SetReplyTransportID", userID, "rich-media-7").Return(nil).Once()
			store.On("GetRecentHistory", userID, 1).Return([]storage.Message{{ID: 9}}, nil).Once()
			for _, artifactID := range tt.artifactIDs {
				store.On("UpdateMessageID", userID, artifactID, int64(9)).Return(nil).Once()
			}

			result := bot.sendResponseWithGeneratedImages(
				context.Background(), path, nil, tt.response, tt.artifactIDs, bot.logger,
			)

			require.Equal(t, richDeliveryConfirmed, result.outcome, "delivery error: %v", result.err)
			assert.Equal(t, tt.wantAttempts, result.attempts)
			require.Len(t, transport.richMedia, 1)
			assert.Len(t, transport.richMedia[0].Items, tt.wantGallery)
			assert.Len(t, transport.text, tt.wantText)
			assert.Len(t, transport.media, tt.wantSidecar)
			if tt.wantSidecar > 0 {
				require.Len(t, transport.media[0].Items, 1)
				assert.True(t, transport.media[0].Items[0].AsDocument)
			}
			store.AssertExpectations(t)
		})
	}
}

func TestGeneratedMedia_UnconfirmedMediaDoesNotPersistOrLink(t *testing.T) {
	tests := []struct {
		name     string
		mediaID  string
		mediaErr error
		outcome  richDeliveryOutcome
	}{
		{name: "network error", mediaErr: errors.New("connection reset after write"), outcome: richDeliveryUnknown},
		{name: "telegram 500", mediaErr: &telegram.APIError{Code: 500, Description: "Internal Server Error"}, outcome: richDeliveryUnknown},
		{name: "empty success", outcome: richDeliveryUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &recordingTransport{mediaID: tt.mediaID, mediaErr: tt.mediaErr}
			bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
			expectGeneratedArtifact(store, userID)

			result := bot.sendResponseWithGeneratedImages(
				context.Background(), generatedPath(bot, userID), nil,
				"Here is the image", []int64{42}, bot.logger,
			)

			assert.Equal(t, tt.outcome, result.outcome)
			assert.False(t, result.persisted)
			assert.Equal(t, 1, result.attempts)
			store.AssertNotCalled(t, "AddMessageToHistory", mock.Anything, mock.Anything)
			store.AssertNotCalled(t, "SetReplyTransportID", mock.Anything, mock.Anything)
			store.AssertNotCalled(t, "GetRecentHistory", mock.Anything, mock.Anything)
			store.AssertNotCalled(t, "UpdateMessageID", mock.Anything, mock.Anything, mock.Anything)
			store.AssertExpectations(t)
		})
	}
}

func TestGeneratedMedia_HistoryInsertFailureNeverLinksOlderRow(t *testing.T) {
	transport := &recordingTransport{mediaID: "media-7"}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	expectGeneratedArtifact(store, userID)
	store.On("AddMessageToHistory", userID, mock.Anything).Return(errors.New("database unavailable")).Once()

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), generatedPath(bot, userID), nil,
		"Here is the image", []int64{42}, bot.logger,
	)

	assert.Equal(t, richDeliveryConfirmed, result.outcome, "delivery remains confirmed even when best-effort persistence fails")
	assert.False(t, result.persisted)
	store.AssertNotCalled(t, "SetReplyTransportID", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "GetRecentHistory", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "UpdateMessageID", mock.Anything, mock.Anything, mock.Anything)
	store.AssertExpectations(t)
}

func TestGeneratedMedia_FollowUp500DoesNotPersistCompleteReply(t *testing.T) {
	transport := &recordingTransport{
		mediaID: "media-7",
		textErrors: map[int]error{
			0: &telegram.APIError{Code: 500, Description: "Internal Server Error"},
		},
	}
	bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
	expectGeneratedArtifact(store, userID)
	longReply := strings.Repeat("длинный текст для переполнения подписи ", 80)

	result := bot.sendResponseWithGeneratedImages(
		context.Background(), generatedPath(bot, userID), nil,
		longReply, []int64{42}, bot.logger,
	)

	assert.Equal(t, richDeliveryPartialUnknown, result.outcome)
	assert.False(t, result.persisted)
	assert.Equal(t, 2, result.attempts)
	require.Len(t, transport.media, 1)
	require.Len(t, transport.text, 1)
	store.AssertNotCalled(t, "AddMessageToHistory", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "SetReplyTransportID", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "GetRecentHistory", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "UpdateMessageID", mock.Anything, mock.Anything, mock.Anything)
	store.AssertExpectations(t)
}

func TestDeliverGeneratedOnError_AttemptAndConfirmationAreDistinct(t *testing.T) {
	t.Run("no artifacts", func(t *testing.T) {
		transport := &recordingTransport{mediaID: "media-7"}
		bot, _, userID := newGeneratedDeliveryTestBot(t, transport)
		attempted, confirmed := bot.deliverGeneratedOnError(
			context.Background(), generatedPath(bot, userID), &laplace.Response{}, bot.logger,
		)
		assert.False(t, attempted)
		assert.False(t, confirmed)
		assert.Empty(t, transport.media)
	})

	t.Run("confirmed media", func(t *testing.T) {
		transport := &recordingTransport{mediaID: "media-7"}
		bot, store, userID := newGeneratedDeliveryTestBot(t, transport)
		expectGeneratedArtifact(store, userID)
		store.On("AddMessageToHistory", userID, mock.Anything).Return(nil).Once()
		store.On("SetReplyTransportID", userID, "media-7").Return(nil).Once()
		store.On("GetRecentHistory", userID, 1).Return([]storage.Message{{ID: 9}}, nil).Once()
		store.On("UpdateMessageID", userID, int64(42), int64(9)).Return(nil).Once()

		attempted, confirmed := bot.deliverGeneratedOnError(
			context.Background(), generatedPath(bot, userID),
			&laplace.Response{GeneratedArtifactIDs: []int64{42}}, bot.logger,
		)
		assert.True(t, attempted)
		assert.True(t, confirmed)
		require.Len(t, transport.media, 1)
		assert.Equal(t, bot.translator.Get(bot.cfg.Bot.Language, "bot.image_delivered_text_failed"), transport.media[0].Caption)
		store.AssertExpectations(t)
	})
}

func TestTelegramTransport_SendMediaRejectsMalformedSuccess(t *testing.T) {
	for _, tt := range []struct {
		name string
		msg  *telegram.Message
	}{
		{name: "nil result", msg: nil},
		{name: "zero id", msg: &telegram.Message{}},
		{name: "negative id", msg: &telegram.Message{MessageID: -1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			api := new(testutil.MockBotAPI)
			api.On("SendPhoto", mock.Anything, mock.Anything).Return(tt.msg, nil).Once()
			transport := NewTelegramTransport(api, testutil.TestConfig(), testutil.TestTranslator(t), testutil.TestLogger())

			messageID, err := transport.SendMedia(context.Background(), OutgoingMedia{
				ConversationID: "123",
				Items:          []OutgoingMediaItem{{Data: []byte("png"), Filename: "cat.png", MIME: "image/png"}},
			})

			require.Error(t, err)
			assert.Empty(t, messageID)
			api.AssertExpectations(t)
		})
	}
}

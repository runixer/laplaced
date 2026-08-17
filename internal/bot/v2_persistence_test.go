package bot

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

func newV2PersistenceStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.NewSQLiteStore(testutil.TestLogger(), ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	require.NoError(t, store.Init())
	return store
}

func wireV2PersistenceStore(bot *Bot, store *storage.Store) {
	bot.msgRepo = store
	bot.exactMsgRepo = store
	bot.deliveryRepo = store
	bot.artifactRepo = store
	bot.flagRepo = store
}

func TestV2SplitRichDelivery_PersistsEveryIDAndResolvesSecondaryReaction(t *testing.T) {
	const conversationID = "555"
	userID := storage.PassthroughScopeID(transportTelegram, conversationID)
	store := newV2PersistenceStore(t)
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	wireV2PersistenceStore(bot, store)

	content := "# First section\n\nFirst body.\n\n###SPLIT###\n\n# Second section\n\nSecond body."
	result := bot.sendRichRendered(
		context.Background(), conversationID, "", "42", content, bot.logger,
		deliveryLedgerContext{
			UserID: userID, Transport: transportTelegram, ConversationID: conversationID,
		},
	)

	require.NoError(t, result.err)
	require.Equal(t, richDeliveryConfirmed, result.outcome)
	require.Positive(t, result.deliveryID)
	require.Equal(t, []string{"message-1", "message-2"}, result.confirmedIDs)

	delivery, operations, err := store.GetOutboundDelivery(result.deliveryID)
	require.NoError(t, err)
	require.NotNil(t, delivery)
	assert.Equal(t, storage.DeliveryStatusConfirmed, delivery.Status)
	require.Len(t, operations, 2)
	assert.Equal(t, []string{"message-1"}, operations[0].TransportMessageIDs)
	assert.Equal(t, []string{"message-2"}, operations[1].TransportMessageIDs)

	require.True(t, bot.persistConfirmedAssistantReply(
		userID, trace.SpanFromContext(context.Background()), content, conversationID, nil,
		result.deliveryID, result.confirmedIDs, nil, bot.logger,
	))

	var historyID int64
	for _, messageID := range result.confirmedIDs {
		reply, lookupErr := store.GetReplyByTransportMessage(userID, transportTelegram, conversationID, messageID)
		require.NoError(t, lookupErr)
		require.NotNil(t, reply)
		assert.Equal(t, content, reply.Content)
		if historyID == 0 {
			historyID = reply.ID
		} else {
			assert.Equal(t, historyID, reply.ID)
		}
	}

	// The secondary rich-message id is not mirrored in history.message_id; a
	// successful flag therefore proves HandleReaction used the exact composite
	// mapping rather than the pre-V2 primary-id lookup.
	bot.HandleReaction(IncomingReaction{
		ConversationID: conversationID,
		SenderID:       conversationID,
		MessageID:      "message-2",
		NewEmojis:      []string{"👎"},
		IsDirect:       true,
	})
	flags, err := store.GetFlags(userID, 10)
	require.NoError(t, err)
	require.Len(t, flags, 1)
	assert.Equal(t, "message-2", flags[0].MessageID)
	assert.Equal(t, "👎", flags[0].Emoji)
	require.NotNil(t, flags[0].HistoryID)
	assert.Equal(t, historyID, *flags[0].HistoryID)
}

func TestV2GeneratedGalleryHighResolutionSidecars_PersistEveryIDAndArtifact(t *testing.T) {
	const conversationID = "123"
	userID := storage.PassthroughScopeID(transportTelegram, conversationID)
	store := newV2PersistenceStore(t)
	transport := &recordingTransport{
		richMediaID: "rich-gallery",
		mediaID:     "original",
	}
	cfg := testutil.TestConfig()
	cfg.Transport = transportTelegram
	cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
	// The fixture remains a valid Telegram Photo, but this threshold also asks
	// V2 to preserve each byte-identical original as a Document sidecar.
	cfg.Agents.ImageGenerator.DocumentThresholdBytes = len(generatedTestPNG) - 1
	bot := &Bot{
		cfg:        cfg,
		logger:     testutil.TestLogger(),
		translator: testutil.TestTranslator(t),
		transport:  transport,
		renderer:   NewTelegramRenderer(testutil.TestLogger()),
		fileStorage: &fakeFileStorage{blobs: map[string][]byte{
			"generated/one.png": append([]byte(nil), generatedTestPNG...),
			"generated/two.png": append([]byte(nil), generatedTestPNG...),
		}},
	}
	wireV2PersistenceStore(bot, store)

	artifactIDs := make([]int64, 0, 2)
	for i, name := range []string{"one.png", "two.png"} {
		id, err := store.AddArtifact(storage.Artifact{
			UserID: userID, MessageID: 0, FileType: "image",
			FilePath: fmt.Sprintf("generated/%s", name), FileSize: int64(len(generatedTestPNG)),
			MimeType: "image/png", OriginalName: name,
			ContentHash: fmt.Sprintf("v2-gallery-%d", i), State: "ready",
		})
		require.NoError(t, err)
		artifactIDs = append(artifactIDs, id)
	}

	path := &responsePath{
		bot: bot, logger: bot.logger, userID: userID,
		convID: conversationID, replyTo: "42", richMode: config.TelegramRichMessagesSend,
	}
	content := "# Generated gallery\n\nTwo generated images with **structured** context."
	result := bot.sendResponseWithGeneratedImages(
		context.Background(), path, nil, content, artifactIDs, bot.logger,
	)

	require.NoError(t, result.err)
	require.Equal(t, richDeliveryConfirmed, result.outcome)
	require.True(t, result.persisted)
	require.Positive(t, result.deliveryID)
	require.Equal(t, []string{"rich-gallery", "original", "original-2"}, result.confirmedIDs)
	require.Len(t, transport.richMedia, 1)
	require.Len(t, transport.richMedia[0].Items, 2)
	assert.Contains(t, transport.richMedia[0].HTML, "<h1>Generated gallery</h1>")
	assert.NotContains(t, transport.richMedia[0].HTML, "<img", "trusted gallery layout is injected at the Telegram wire boundary")
	require.Len(t, transport.media, 1)
	require.Len(t, transport.media[0].Items, 2)
	assert.True(t, transport.media[0].Items[0].AsDocument)
	assert.True(t, transport.media[0].Items[1].AsDocument)

	delivery, operations, err := store.GetOutboundDelivery(result.deliveryID)
	require.NoError(t, err)
	require.NotNil(t, delivery)
	require.NotNil(t, delivery.HistoryID)
	assert.Equal(t, storage.DeliveryStatusConfirmed, delivery.Status)
	require.Len(t, operations, 2)
	assert.Equal(t, storage.DeliveryOperationRichMedia, operations[0].Kind)
	assert.Equal(t, []string{"rich-gallery"}, operations[0].TransportMessageIDs)
	assert.Equal(t, storage.DeliveryOperationMedia, operations[1].Kind)
	assert.Equal(t, []string{"original", "original-2"}, operations[1].TransportMessageIDs)

	historyID := *delivery.HistoryID
	for _, messageID := range result.confirmedIDs {
		reply, lookupErr := store.GetReplyByTransportMessage(userID, transportTelegram, conversationID, messageID)
		require.NoError(t, lookupErr)
		require.NotNil(t, reply)
		assert.Equal(t, historyID, reply.ID)
	}
	for _, artifactID := range artifactIDs {
		artifact, artifactErr := store.GetArtifact(userID, artifactID)
		require.NoError(t, artifactErr)
		require.NotNil(t, artifact)
		assert.Equal(t, historyID, artifact.MessageID)
	}
}

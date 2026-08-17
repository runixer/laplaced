package bot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestTelegramTransport_SendMediaPersistentReturnsEveryAlbumID(t *testing.T) {
	api := new(testutil.MockBotAPI)
	api.On("SendMediaGroup", mock.Anything, mock.Anything).
		Return([]telegram.Message{{MessageID: 101}, {MessageID: 102}}, nil).Once()
	transport := NewTelegramTransport(api, testutil.TestConfig(), testutil.TestTranslator(t), testutil.TestLogger())

	result, err := transport.SendMediaPersistent(context.Background(), OutgoingMedia{
		ConversationID: "123",
		Items: []OutgoingMediaItem{
			{Data: append([]byte(nil), generatedTestPNG...), Filename: "one.png", MIME: "image/png", WireKind: OutgoingMediaWireKindPhoto},
			{Data: append([]byte(nil), generatedTestPNG...), Filename: "two.png", MIME: "image/png", WireKind: OutgoingMediaWireKindPhoto},
		},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"101", "102"}, result.MessageIDs)
	api.AssertExpectations(t)
}

func TestTelegramTransport_SendMediaPersistentRejectsDuplicateAlbumID(t *testing.T) {
	api := new(testutil.MockBotAPI)
	api.On("SendMediaGroup", mock.Anything, mock.Anything).
		Return([]telegram.Message{{MessageID: 101}, {MessageID: 101}}, nil).Once()
	transport := NewTelegramTransport(api, testutil.TestConfig(), testutil.TestTranslator(t), testutil.TestLogger())

	result, err := transport.SendMediaPersistent(context.Background(), OutgoingMedia{
		ConversationID: "123",
		Items: []OutgoingMediaItem{
			{Data: append([]byte(nil), generatedTestPNG...), Filename: "one.png", MIME: "image/png", WireKind: OutgoingMediaWireKindPhoto},
			{Data: append([]byte(nil), generatedTestPNG...), Filename: "two.png", MIME: "image/png", WireKind: OutgoingMediaWireKindPhoto},
		},
	})

	require.Error(t, err)
	assert.Empty(t, result.MessageIDs)
	api.AssertExpectations(t)
}

func TestTelegramTransport_SendMediaCompatibilityChunksElevenPhotos(t *testing.T) {
	api := new(testutil.MockBotAPI)
	api.On("SendMediaGroup", mock.Anything, mock.MatchedBy(func(req telegram.SendMediaGroupRequest) bool {
		return len(req.Media) == 10 && req.Media[0].Caption == "caption" && req.ReplyToMessageID == 42
	})).Return([]telegram.Message{
		{MessageID: 101}, {MessageID: 102}, {MessageID: 103}, {MessageID: 104}, {MessageID: 105},
		{MessageID: 106}, {MessageID: 107}, {MessageID: 108}, {MessageID: 109}, {MessageID: 110},
	}, nil).Once()
	api.On("SendPhoto", mock.Anything, mock.MatchedBy(func(req telegram.SendPhotoRequest) bool {
		return req.Caption == "" && req.ReplyToMessageID == 0
	})).Return(&telegram.Message{MessageID: 111}, nil).Once()
	transport := NewTelegramTransport(api, testutil.TestConfig(), testutil.TestTranslator(t), testutil.TestLogger())

	items := generatedV2PhotoItems(11)
	primaryID, err := transport.SendMedia(context.Background(), OutgoingMedia{
		ConversationID: "123",
		ReplyTo:        "42",
		Caption:        "caption",
		Items:          items,
	})

	require.NoError(t, err)
	assert.Equal(t, "101", primaryID)
	api.AssertExpectations(t)
}

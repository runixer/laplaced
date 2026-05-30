package bot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestShouldReply(t *testing.T) {
	tests := []struct {
		name          string
		im            IncomingMessage
		senderAllowed bool
		want          bool
	}{
		{"DM always replies (allowed)", IncomingMessage{IsDirect: true}, true, true},
		{"DM always replies even if allow=false (gated earlier)", IncomingMessage{IsDirect: true}, false, true},
		{"channel mention by allowed sender", IncomingMessage{IsDirect: false, Mention: true}, true, true},
		{"channel mention by non-allowed sender", IncomingMessage{IsDirect: false, Mention: true}, false, false},
		{"channel no mention", IncomingMessage{IsDirect: false, Mention: false}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldReply(tt.im, tt.senderAllowed))
		})
	}
}

func TestStorePassiveChannelMessage_AttributesAuthor(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	b := &Bot{logger: testutil.TestLogger(), msgRepo: mockStore}

	im := IncomingMessage{
		ConversationID: "chan1",
		SenderID:       "u1",
		MessageID:      "post1",
		Text:           "hello team",
		Prefix:         "[John (@jdoe) (2026-05-30 12:00:00)]",
		SenderDisplay:  "John (@jdoe)",
		IsDirect:       false,
	}

	mockStore.On("AddMessageToHistory", int64(42), mock.MatchedBy(func(m storage.Message) bool {
		return m.Role == "user" &&
			m.Content == "[John (@jdoe) (2026-05-30 12:00:00)]: hello team" &&
			m.Author != nil && *m.Author == "John (@jdoe)" &&
			m.ConversationID != nil && *m.ConversationID == "chan1" &&
			m.MessageID != nil && *m.MessageID == "post1"
	})).Return(nil)

	b.storePassiveChannelMessage(42, im)
	mockStore.AssertExpectations(t)
}

func TestStorePassiveChannelMessage_EmptyContentSkipped(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	b := &Bot{logger: testutil.TestLogger(), msgRepo: mockStore}

	// No text and no files → incomingContent returns "" → nothing stored.
	im := IncomingMessage{ConversationID: "chan1", SenderID: "u1", IsDirect: false}
	b.storePassiveChannelMessage(42, im)

	mockStore.AssertNotCalled(t, "AddMessageToHistory")
	mockStore.AssertExpectations(t)
}

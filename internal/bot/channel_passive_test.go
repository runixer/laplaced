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

func TestBotParticipatesInThread(t *testing.T) {
	t.Run("DM never consults the repo", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		b := &Bot{logger: testutil.TestLogger(), msgRepo: mockStore}
		got := b.botParticipatesInThread(1, IncomingMessage{IsDirect: true, ThreadRoot: "r"})
		assert.False(t, got)
		mockStore.AssertNotCalled(t, "BotParticipatedInThread")
	})

	t.Run("threadless channel post is not a thread reply", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		b := &Bot{logger: testutil.TestLogger(), msgRepo: mockStore}
		got := b.botParticipatesInThread(2, IncomingMessage{IsDirect: false, ThreadRoot: ""})
		assert.False(t, got)
		mockStore.AssertNotCalled(t, "BotParticipatedInThread")
	})

	t.Run("delegates to repo for a channel thread", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		b := &Bot{logger: testutil.TestLogger(), msgRepo: mockStore}
		im := IncomingMessage{IsDirect: false, ConversationID: "chanA", ThreadRoot: "rootX"}
		mockStore.On("BotParticipatedInThread", int64(2), "chanA", "rootX").Return(true, nil)
		assert.True(t, b.botParticipatesInThread(2, im))
		mockStore.AssertExpectations(t)
	})

	t.Run("repo error fails safe to false", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		b := &Bot{logger: testutil.TestLogger(), msgRepo: mockStore}
		im := IncomingMessage{IsDirect: false, ConversationID: "chanA", ThreadRoot: "rootX"}
		mockStore.On("BotParticipatedInThread", int64(2), "chanA", "rootX").Return(false, assert.AnError)
		assert.False(t, b.botParticipatesInThread(2, im))
		mockStore.AssertExpectations(t)
	})
}

func TestUpsertChannelParticipant(t *testing.T) {
	t.Run("DM is a no-op", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		b := &Bot{logger: testutil.TestLogger(), peopleRepo: mockStore, transport: &stubTransport{kind: "time"}}
		b.upsertChannelParticipant(1, IncomingMessage{IsDirect: true, SenderID: "u1"})
		mockStore.AssertNotCalled(t, "FindPersonByExternalID")
	})

	t.Run("creates a new participant with external id", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		b := &Bot{logger: testutil.TestLogger(), peopleRepo: mockStore, transport: &stubTransport{kind: "time"}}
		im := IncomingMessage{IsDirect: false, SenderID: "u1", ConversationID: "chanA", SenderDisplay: "Alice (@alice)"}
		mockStore.On("FindPersonByExternalID", int64(2), "time", "u1").Return(nil, nil)
		mockStore.On("AddPerson", mock.MatchedBy(func(p storage.Person) bool {
			return p.UserID == 2 && p.DisplayName == "Alice (@alice)" && p.Circle == "Other" &&
				p.ExternalTransport != nil && *p.ExternalTransport == "time" &&
				p.ExternalID != nil && *p.ExternalID == "u1"
		})).Return(int64(5), nil)
		b.upsertChannelParticipant(2, im)
		mockStore.AssertExpectations(t)
	})

	t.Run("touches an existing participant", func(t *testing.T) {
		mockStore := new(testutil.MockStorage)
		b := &Bot{logger: testutil.TestLogger(), peopleRepo: mockStore, transport: &stubTransport{kind: "time"}}
		im := IncomingMessage{IsDirect: false, SenderID: "u1", ConversationID: "chanA", SenderDisplay: "Alice"}
		existing := &storage.Person{ID: 5, UserID: 2, DisplayName: "Alice", MentionCount: 3}
		mockStore.On("FindPersonByExternalID", int64(2), "time", "u1").Return(existing, nil)
		mockStore.On("UpdatePerson", mock.MatchedBy(func(p storage.Person) bool {
			return p.ID == 5 && p.MentionCount == 4
		})).Return(nil)
		b.upsertChannelParticipant(2, im)
		mockStore.AssertNotCalled(t, "AddPerson")
		mockStore.AssertExpectations(t)
	})
}

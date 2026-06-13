package bot

import (
	"testing"

	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestAddedReactions(t *testing.T) {
	tests := []struct {
		name     string
		old, new []string
		want     []string
	}{
		{"first reaction", nil, []string{"👎"}, []string{"👎"}},
		{"pure removal", []string{"👎"}, nil, nil},
		{"swap adds the new one", []string{"👍"}, []string{"👎"}, []string{"👎"}},
		{"unchanged adds nothing", []string{"👎"}, []string{"👎"}, nil},
		{"multiple new", nil, []string{"👎", "💩"}, []string{"👎", "💩"}},
		{"keep one, add one", []string{"👍"}, []string{"👍", "👎"}, []string{"👎"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, addedReactions(tt.old, tt.new))
		})
	}
}

func newReactionTestBot(t *testing.T, allowedID int64) (*Bot, *testutil.MockStorage) {
	t.Helper()
	mockStore := new(testutil.MockStorage)
	cfg := testutil.TestConfig()
	cfg.Bot.AllowedUserIDs = []int64{allowedID}
	logger := testutil.TestLogger()
	b := &Bot{
		cfg:       cfg,
		msgRepo:   mockStore,
		flagRepo:  mockStore,
		logger:    logger,
		transport: NewTelegramTransport(nil, cfg, testutil.TestTranslator(t), logger),
	}
	return b, mockStore
}

func TestHandleReaction_FlagsBotReply(t *testing.T) {
	b, mockStore := newReactionTestBot(t, 555)
	scope := storage.PassthroughScopeID("telegram", "555")
	trace := "0102030405060708090a0b0c0d0e0f10"

	mockStore.On("GetReplyByTransportID", scope, "4473").
		Return(&storage.Message{ID: 42, Content: "the bad reply", TraceID: &trace}, nil)
	mockStore.On("AddFlag", mock.MatchedBy(func(f storage.Flag) bool {
		return f.UserID == scope && f.MessageID == "4473" && f.Emoji == "👎" &&
			f.TraceID != nil && *f.TraceID == trace && f.ReplyPreview == "the bad reply"
	})).Return(nil)

	b.HandleReaction(IncomingReaction{
		ConversationID: "555", SenderID: "555", MessageID: "4473",
		NewEmojis: []string{"👎"}, IsDirect: true,
	})

	mockStore.AssertExpectations(t)
}

func TestHandleReaction_IgnoresRemoval(t *testing.T) {
	b, mockStore := newReactionTestBot(t, 555)

	// old has 👎, new is empty → pure removal → no lookup, no flag.
	b.HandleReaction(IncomingReaction{
		SenderID: "555", MessageID: "4473",
		OldEmojis: []string{"👎"}, NewEmojis: nil, IsDirect: true,
	})

	mockStore.AssertNotCalled(t, "GetReplyByTransportID", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "AddFlag", mock.Anything)
}

func TestHandleReaction_IgnoresUnindexedMessage(t *testing.T) {
	b, mockStore := newReactionTestBot(t, 555)
	scope := storage.PassthroughScopeID("telegram", "555")

	// Reaction on the user's own / a pre-feature message: lookup misses.
	mockStore.On("GetReplyByTransportID", scope, "9999").Return(nil, nil)

	b.HandleReaction(IncomingReaction{
		SenderID: "555", MessageID: "9999",
		NewEmojis: []string{"👍"}, IsDirect: true,
	})

	mockStore.AssertExpectations(t)
	mockStore.AssertNotCalled(t, "AddFlag", mock.Anything)
}

func TestHandleReaction_IgnoresUnauthorizedSender(t *testing.T) {
	b, mockStore := newReactionTestBot(t, 555)

	// Sender 999 is not in the allowlist.
	b.HandleReaction(IncomingReaction{
		SenderID: "999", MessageID: "4473",
		NewEmojis: []string{"👎"}, IsDirect: true,
	})

	mockStore.AssertNotCalled(t, "GetReplyByTransportID", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "AddFlag", mock.Anything)
}

func TestHandleReaction_MultipleEmojisFlagEach(t *testing.T) {
	b, mockStore := newReactionTestBot(t, 555)
	scope := storage.PassthroughScopeID("telegram", "555")

	mockStore.On("GetReplyByTransportID", scope, "4473").
		Return(&storage.Message{ID: 42, Content: "reply"}, nil)
	mockStore.On("AddFlag", mock.Anything).Return(nil).Times(2)

	b.HandleReaction(IncomingReaction{
		SenderID: "555", MessageID: "4473",
		NewEmojis: []string{"👎", "💩"}, IsDirect: true,
	})

	mockStore.AssertExpectations(t)
}

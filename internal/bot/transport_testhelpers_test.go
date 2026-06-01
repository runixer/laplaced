package bot

import (
	"context"
	"strconv"
	"time"

	"github.com/runixer/laplaced/internal/storage"

	"github.com/runixer/laplaced/internal/telegram"
)

// stubTransport is a minimal Transport for tests that need a non-Telegram kind
// or a controllable allowlist without standing up a real Mattermost client.
type stubTransport struct {
	kind    string
	allowed map[string]bool
}

func (s *stubTransport) SendText(context.Context, OutgoingResponse) (string, error) { return "", nil }
func (s *stubTransport) SendMedia(context.Context, OutgoingMedia) (string, error)   { return "", nil }
func (s *stubTransport) SendTyping(context.Context, string) error                   { return nil }
func (s *stubTransport) SetReaction(context.Context, string, string) error          { return nil }
func (s *stubTransport) Kind() string                                               { return s.kind }
func (s *stubTransport) Capabilities() Capabilities                                 { return Capabilities{} }
func (s *stubTransport) IsAllowed(id string) bool                                   { return s.allowed[id] }

// grpIncoming maps a Telegram message to (scopeID, IncomingMessage) for the
// bot-free message-grouper tests, carrying only the fields the grouper keys on.
// Designed for grouper.AddMessage(grpIncoming(msg)) via multi-return spread.
func grpIncoming(msg *telegram.Message) (storage.ScopeID, IncomingMessage) {
	return storage.ScopeID(strconv.FormatInt(msg.From.ID, 10)), IncomingMessage{
		SenderID:       strconv.FormatInt(msg.From.ID, 10),
		ConversationID: strconv.FormatInt(msg.From.ID, 10),
		MessageID:      strconv.Itoa(msg.MessageID),
		IsDirect:       true,
		SentAt:         time.Now(),
	}
}

// tgGroup builds a MessageGroup from Telegram messages via the neutral envelope.
// Used by tests that exercise processMessageGroup directly (post transport-seam,
// the group holds IncomingMessage, not *telegram.Message).
func tgGroup(b *Bot, userID storage.ScopeID, msgs ...*telegram.Message) *MessageGroup {
	return &MessageGroup{
		Messages:  tgIncomings(b, msgs...),
		UserID:    userID,
		StartedAt: time.Now(),
	}
}

// tgIncomings converts Telegram messages to neutral envelopes for tests.
func tgIncomings(b *Bot, msgs ...*telegram.Message) []IncomingMessage {
	ims := make([]IncomingMessage, len(msgs))
	for i, m := range msgs {
		ims[i] = b.incomingFromTelegram(m)
	}
	return ims
}

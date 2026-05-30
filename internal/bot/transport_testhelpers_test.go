package bot

import (
	"strconv"
	"time"

	"github.com/runixer/laplaced/internal/telegram"
)

// grpIncoming maps a Telegram message to (scopeID, IncomingMessage) for the
// bot-free message-grouper tests, carrying only the fields the grouper keys on.
// Designed for grouper.AddMessage(grpIncoming(msg)) via multi-return spread.
func grpIncoming(msg *telegram.Message) (int64, IncomingMessage) {
	return msg.From.ID, IncomingMessage{
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
func tgGroup(b *Bot, userID int64, msgs ...*telegram.Message) *MessageGroup {
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

package bot

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/runixer/laplaced/internal/telegram"
)

// MessageGroup represents a collection of messages from a single user that are processed together.
type MessageGroup struct {
	Messages   []*telegram.Message
	Timer      *time.Timer
	CancelFunc context.CancelFunc
	UserID     int64
}

// MessageGrouper handles the grouping of incoming messages from users.
type MessageGrouper struct {
	mu           sync.Mutex
	groups       map[int64]*MessageGroup
	bot          *Bot
	logger       *slog.Logger
	turnWait     time.Duration
	onGroupReady func(ctx context.Context, group *MessageGroup)
}

// NewMessageGrouper creates a new MessageGrouper.
func NewMessageGrouper(b *Bot, logger *slog.Logger, turnWait time.Duration, onGroupReady func(ctx context.Context, group *MessageGroup)) *MessageGrouper {
	return &MessageGrouper{
		groups:       make(map[int64]*MessageGroup),
		bot:          b,
		logger:       logger.With(slog.String("component", "message_grouper")),
		turnWait:     turnWait,
		onGroupReady: onGroupReady,
	}
}

// AddMessage adds a new message to a user's group.
func (mg *MessageGrouper) AddMessage(msg *telegram.Message) {
	mg.mu.Lock()
	defer mg.mu.Unlock()

	userID := msg.From.ID

	group, ok := mg.groups[userID]
	if !ok {
		// Create a new group if one doesn't exist
		group = &MessageGroup{
			Messages: []*telegram.Message{},
			UserID:   userID,
		}
		mg.groups[userID] = group
		mg.logger.Debug("created new message group", slog.Int64("user_id", userID))
	} else {
		// If a group exists, cancel the previous timer and any ongoing processing.
		if group.Timer != nil {
			group.Timer.Stop()
		}
		if group.CancelFunc != nil {
			group.CancelFunc()
			mg.logger.Debug("cancelled previous processing", slog.Int64("user_id", userID))
		}
	}

	group.Messages = append(group.Messages, msg)
	mg.logger.Debug("added message to group", slog.Int64("user_id", userID), slog.Int("message_id", msg.MessageID))

	// Create a new context for this processing cycle.
	ctx, cancel := context.WithCancel(context.Background())
	group.CancelFunc = cancel

	// Reset the timer.
	group.Timer = time.AfterFunc(mg.turnWait, func() {
		mg.processGroup(ctx, userID)
	})
}

func (mg *MessageGrouper) processGroup(ctx context.Context, userID int64) {
	mg.mu.Lock()
	group, ok := mg.groups[userID]
	if !ok {
		mg.mu.Unlock()
		return
	}

	// Take a snapshot of the messages to process.
	messagesToProcess := make([]*telegram.Message, len(group.Messages))
	copy(messagesToProcess, group.Messages)

	// Clear the group's message list for the next batch.
	group.Messages = []*telegram.Message{}
	group.Timer = nil
	// The CancelFunc is for the context we are currently in. A new one will be made
	// when a new message arrives.

	mg.mu.Unlock()

	if len(messagesToProcess) == 0 {
		return // Nothing to process
	}

	// Create a new group object for the handler to own, so the handler can't
	// accidentally modify the original group's state.
	processingGroup := &MessageGroup{
		Messages: messagesToProcess,
		UserID:   userID,
	}

	mg.logger.Info("processing message group", slog.Int64("user_id", processingGroup.UserID), slog.Int("message_count", len(processingGroup.Messages)))

	// Pass the context down to the handler.
	mg.onGroupReady(ctx, processingGroup)
}

package bot

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/runixer/laplaced/internal/telegram"
)

// SessionInfo represents information about an active message grouping session.
type SessionInfo struct {
	UserID       int64
	MessageCount int
	StartedAt    time.Time
	LastMessage  time.Time
}

// MessageGroup represents a collection of messages from a single user that are processed together.
type MessageGroup struct {
	Messages   []*telegram.Message
	Timer      *time.Timer
	CancelFunc context.CancelFunc
	UserID     int64
	StartedAt  time.Time // When the first message in this group was received
}

// MessageGrouper handles the grouping of incoming messages from users.
type MessageGrouper struct {
	mu           sync.Mutex
	wg           sync.WaitGroup // tracks active processGroup operations
	groups       map[int64]*MessageGroup
	bot          *Bot
	logger       *slog.Logger
	turnWait     time.Duration
	onGroupReady func(ctx context.Context, group *MessageGroup)
	parentCtx    context.Context
	parentCancel context.CancelFunc
}

// NewMessageGrouper creates a new MessageGrouper.
func NewMessageGrouper(b *Bot, logger *slog.Logger, turnWait time.Duration, onGroupReady func(ctx context.Context, group *MessageGroup)) *MessageGrouper {
	ctx, cancel := context.WithCancel(context.Background())
	return &MessageGrouper{
		groups:       make(map[int64]*MessageGroup),
		bot:          b,
		logger:       logger.With(slog.String("component", "message_grouper")),
		turnWait:     turnWait,
		onGroupReady: onGroupReady,
		parentCtx:    ctx,
		parentCancel: cancel,
	}
}

// Stop processes all pending message groups and waits for completion.
// Should be called during graceful shutdown.
func (mg *MessageGrouper) Stop() {
	mg.logger.Info("Stopping message grouper...")

	// Don't cancel parent context yet - we want pending groups to be processed

	mg.mu.Lock()
	// Collect groups that need immediate processing
	var pendingGroups []*MessageGroup
	for userID, group := range mg.groups {
		if group.Timer != nil {
			// Timer.Stop returns true if the timer was stopped before firing
			if group.Timer.Stop() {
				// Timer was stopped before firing - we need to process this group
				mg.logger.Debug("processing pending group on shutdown", slog.Int64("user_id", userID))
				pendingGroups = append(pendingGroups, group)
			} else {
				// Timer already fired - callback is running or will run
				mg.logger.Debug("timer already fired for user", slog.Int64("user_id", userID))
			}
		}
		// Don't cancel the context - let processing complete
	}

	// Clear the groups map
	mg.groups = make(map[int64]*MessageGroup)
	mg.mu.Unlock()

	// Process pending groups immediately with non-cancellable context
	for _, group := range pendingGroups {
		// The wg.Add(1) was already called in AddMessage, wg.Done() will be called after processing
		go func(g *MessageGroup) {
			defer mg.wg.Done()
			mg.logger.Info("processing message group on shutdown",
				slog.Int64("user_id", g.UserID),
				slog.Int("message_count", len(g.Messages)))
			// Use background context since we're shutting down
			mg.onGroupReady(context.Background(), g)
		}(group)
	}

	// Wait for all active processGroup operations to complete
	mg.logger.Info("Waiting for active message processing to complete...")
	mg.wg.Wait()

	// Now cancel the parent context (cleanup)
	mg.parentCancel()
	mg.logger.Info("Message grouper stopped")
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
			Messages:  []*telegram.Message{},
			UserID:    userID,
			StartedAt: time.Now(),
		}
		mg.groups[userID] = group
		mg.logger.Debug("created new message group", slog.Int64("user_id", userID))
	}

	if ok {
		// If a group exists, cancel the previous timer and any ongoing processing.
		if group.Timer != nil {
			// Timer.Stop returns true if the timer was stopped before firing
			// In that case, the callback won't run, so we need to call wg.Done()
			if group.Timer.Stop() {
				mg.wg.Done()
			}
		}
		if group.CancelFunc != nil {
			group.CancelFunc()
			mg.logger.Debug("cancelled previous processing", slog.Int64("user_id", userID))
		}
	}

	group.Messages = append(group.Messages, msg)
	mg.logger.Debug("added message to group", slog.Int64("user_id", userID), slog.Int("message_id", msg.MessageID))

	// Create a new context derived from parent context.
	// This ensures child contexts are cancelled when Stop() is called.
	ctx, cancel := context.WithCancel(mg.parentCtx)
	group.CancelFunc = cancel

	// Reset the timer.
	// Add to WaitGroup BEFORE starting timer to ensure proper shutdown ordering
	mg.wg.Add(1)
	group.Timer = time.AfterFunc(mg.turnWait, func() {
		defer mg.wg.Done()
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

// GetActiveSessions returns information about all active message grouping sessions.
func (mg *MessageGrouper) GetActiveSessions() []SessionInfo {
	mg.mu.Lock()
	defer mg.mu.Unlock()

	sessions := make([]SessionInfo, 0, len(mg.groups))
	for _, group := range mg.groups {
		if len(group.Messages) == 0 {
			continue
		}

		// Get the timestamp of the last message
		lastMsg := group.Messages[len(group.Messages)-1]
		lastMsgTime := time.Unix(int64(lastMsg.Date), 0)

		sessions = append(sessions, SessionInfo{
			UserID:       group.UserID,
			MessageCount: len(group.Messages),
			StartedAt:    group.StartedAt,
			LastMessage:  lastMsgTime,
		})
	}

	return sessions
}

// ForceCloseSession immediately processes and closes a session for the given user.
// Returns true if a session was found and closed, false otherwise.
func (mg *MessageGrouper) ForceCloseSession(userID int64) bool {
	mg.mu.Lock()

	group, ok := mg.groups[userID]
	if !ok || len(group.Messages) == 0 {
		mg.mu.Unlock()
		return false
	}

	// Stop the timer if it's running
	if group.Timer != nil {
		if group.Timer.Stop() {
			// Timer was stopped before firing, we need to call wg.Done() since
			// we're going to process it ourselves
			mg.wg.Done()
		}
	}

	// Take ownership of messages
	messagesToProcess := make([]*telegram.Message, len(group.Messages))
	copy(messagesToProcess, group.Messages)

	// Clear the group
	delete(mg.groups, userID)
	mg.mu.Unlock()

	mg.logger.Info("force closing session",
		slog.Int64("user_id", userID),
		slog.Int("message_count", len(messagesToProcess)))

	// Process the group immediately
	processingGroup := &MessageGroup{
		Messages:  messagesToProcess,
		UserID:    userID,
		StartedAt: group.StartedAt,
	}

	// Track this in WaitGroup and process
	mg.wg.Add(1)
	go func() {
		defer mg.wg.Done()
		mg.onGroupReady(context.Background(), processingGroup)
	}()

	return true
}

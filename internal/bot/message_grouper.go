package bot

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/runixer/laplaced/internal/storage"
)

// SessionInfo represents information about an active message grouping session.
type SessionInfo struct {
	UserID       storage.ScopeID
	MessageCount int
	StartedAt    time.Time
	LastMessage  time.Time
}

// MessageGroup represents a collection of messages processed together as one
// turn. UserID is the resolved internal scope id (the storage partition key) —
// for Telegram it equals the sender id (passthrough); for a channel it is the
// channel's scope id, shared by all participants.
type MessageGroup struct {
	Messages   []IncomingMessage
	Timer      *time.Timer
	CancelFunc context.CancelFunc
	UserID     storage.ScopeID
	StartedAt  time.Time // When the first message in this group was received
}

// groupKeyFor computes the grouping key for an incoming message. Grouping is
// per-conversation in a DM (key = scope id) but per-participant in a channel
// (key = scope id + sender), so two people @mentioning the bot within turnWait
// get separate replies instead of being merged into one turn. The storage scope
// (MessageGroup.UserID) stays the scope id in both cases.
func groupKeyFor(scopeID storage.ScopeID, im IncomingMessage) string {
	key := string(scopeID)
	if im.IsDirect {
		return key
	}
	return key + ":" + im.SenderID
}

// userProcessingLock tracks a per-key mutex and its last usage time for cleanup.
type userProcessingLock struct {
	mu       sync.Mutex
	lastUsed time.Time
}

// MessageGrouper handles the grouping of incoming messages.
type MessageGrouper struct {
	mu           sync.Mutex
	wg           sync.WaitGroup // tracks active processGroup operations
	groups       map[string]*MessageGroup
	bot          *Bot
	logger       *slog.Logger
	turnWait     time.Duration
	onGroupReady func(ctx context.Context, group *MessageGroup)
	parentCtx    context.Context
	parentCancel context.CancelFunc

	// Per-key processing locks ensure strict FIFO ordering. A key's next message
	// group won't start processing until the previous one completes.
	processingLocks   map[string]*userProcessingLock
	processingLocksMu sync.Mutex
}

// NewMessageGrouper creates a new MessageGrouper.
func NewMessageGrouper(b *Bot, logger *slog.Logger, turnWait time.Duration, onGroupReady func(ctx context.Context, group *MessageGroup)) *MessageGrouper {
	// cancel is stored as parentCancel and invoked from Stop().
	ctx, cancel := context.WithCancel(context.Background()) // #nosec G118 -- cancel persisted in struct, invoked in Stop
	return &MessageGrouper{
		groups:          make(map[string]*MessageGroup),
		bot:             b,
		logger:          logger.With(slog.String("component", "message_grouper")),
		turnWait:        turnWait,
		onGroupReady:    onGroupReady,
		parentCtx:       ctx,
		parentCancel:    cancel,
		processingLocks: make(map[string]*userProcessingLock),
	}
}

// Stop processes all pending message groups and waits for completion.
// Should be called during graceful shutdown.
func (mg *MessageGrouper) Stop() {
	mg.logger.Info("Stopping message grouper...")

	// Don't cancel parent context yet - we want pending groups to be processed

	mg.mu.Lock()
	// Collect groups that need immediate processing, keyed for FIFO locking.
	pendingGroups := make(map[string]*MessageGroup)
	for key, group := range mg.groups {
		if group.Timer != nil {
			// Timer.Stop returns true if the timer was stopped before firing
			if group.Timer.Stop() {
				// Timer was stopped before firing - we need to process this group
				mg.logger.Debug("processing pending group on shutdown", slog.String("group", key))
				pendingGroups[key] = group
			} else {
				// Timer already fired - callback is running or will run
				mg.logger.Debug("timer already fired", slog.String("group", key))
			}
		}
		// Don't cancel the context - let processing complete
	}

	// Clear the groups map
	mg.groups = make(map[string]*MessageGroup)
	mg.mu.Unlock()

	// Process pending groups immediately with non-cancellable context
	for key, group := range pendingGroups {
		// The wg.Add(1) was already called in AddMessage, wg.Done() will be called after processing
		go func(key string, g *MessageGroup) {
			defer mg.wg.Done()

			// Acquire per-key lock to ensure FIFO ordering during shutdown
			userLock := mg.getProcessingLock(key)
			userLock.mu.Lock()
			defer func() {
				userLock.lastUsed = time.Now()
				userLock.mu.Unlock()
			}()

			mg.logger.Info("processing message group on shutdown",
				slog.String("user_id", string(g.UserID)),
				slog.Int("message_count", len(g.Messages)))
			// Use background context since we're shutting down
			mg.onGroupReady(context.Background(), g)
		}(key, group)
	}

	// Wait for all active processGroup operations to complete
	mg.logger.Info("Waiting for active message processing to complete...")
	mg.wg.Wait()

	// Now cancel the parent context (cleanup)
	mg.parentCancel()
	mg.logger.Info("Message grouper stopped")
}

// AddMessage adds a new message to its group. scopeID is the resolved internal
// scope id (storage partition key); the grouping key is derived from it and the
// message (per-sender in channels — see groupKeyFor).
func (mg *MessageGrouper) AddMessage(scopeID storage.ScopeID, im IncomingMessage) {
	mg.mu.Lock()
	defer mg.mu.Unlock()

	key := groupKeyFor(scopeID, im)

	group, ok := mg.groups[key]
	if !ok {
		// Create a new group if one doesn't exist
		group = &MessageGroup{
			Messages:  []IncomingMessage{},
			UserID:    scopeID,
			StartedAt: time.Now(),
		}
		mg.groups[key] = group
		mg.logger.Debug("created new message group", slog.String("group", key))
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
			mg.logger.Debug("cancelled previous processing", slog.String("group", key))
		}
	}

	group.Messages = append(group.Messages, im)
	mg.logger.Debug("added message to group", slog.String("group", key), slog.String("message_id", im.MessageID))

	// Create a new context derived from parent context.
	// This ensures child contexts are cancelled when Stop() is called.
	ctx, cancel := context.WithCancel(mg.parentCtx)
	group.CancelFunc = cancel

	// Reset the timer.
	// Add to WaitGroup BEFORE starting timer to ensure proper shutdown ordering
	mg.wg.Add(1)
	group.Timer = time.AfterFunc(mg.turnWait, func() {
		defer mg.wg.Done()
		defer cancel() // release ctx when processing completes; idempotent if AddMessage already preempted
		mg.processGroup(ctx, key)
	})
}

// getProcessingLock returns the processing lock for a key, creating one if needed.
func (mg *MessageGrouper) getProcessingLock(key string) *userProcessingLock {
	mg.processingLocksMu.Lock()
	defer mg.processingLocksMu.Unlock()

	lock, ok := mg.processingLocks[key]
	if !ok {
		lock = &userProcessingLock{}
		mg.processingLocks[key] = lock
	}
	return lock
}

func (mg *MessageGrouper) processGroup(ctx context.Context, key string) {
	mg.mu.Lock()
	group, ok := mg.groups[key]
	if !ok {
		mg.mu.Unlock()
		return
	}

	// Take a snapshot of the messages to process.
	messagesToProcess := make([]IncomingMessage, len(group.Messages))
	copy(messagesToProcess, group.Messages)

	// Clear the group's message list for the next batch.
	group.Messages = []IncomingMessage{}
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
		Messages:  messagesToProcess,
		UserID:    group.UserID,
		StartedAt: group.StartedAt,
	}

	// Acquire per-key processing lock to ensure FIFO ordering.
	// This blocks until any previous message group for this key finishes processing.
	userLock := mg.getProcessingLock(key)
	userLock.mu.Lock()
	defer func() {
		userLock.lastUsed = time.Now()
		userLock.mu.Unlock()
	}()

	mg.logger.Info("processing message group", slog.String("user_id", string(processingGroup.UserID)), slog.Int("message_count", len(processingGroup.Messages)))

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
		lastMsgTime := lastMsg.SentAt

		sessions = append(sessions, SessionInfo{
			UserID:       group.UserID,
			MessageCount: len(group.Messages),
			StartedAt:    group.StartedAt,
			LastMessage:  lastMsgTime,
		})
	}

	return sessions
}

// ForceCloseSession immediately processes and closes any active groups for the
// given scope id. Returns true if at least one group was found and closed. In a
// channel a scope may have several per-sender groups; all are closed.
func (mg *MessageGrouper) ForceCloseSession(scopeID storage.ScopeID) bool {
	mg.mu.Lock()

	type pending struct {
		key   string
		group *MessageGroup
	}
	var toProcess []pending
	for key, group := range mg.groups {
		if group.UserID != scopeID || len(group.Messages) == 0 {
			continue
		}
		if group.Timer != nil {
			if group.Timer.Stop() {
				// Timer stopped before firing — we process it ourselves, so the
				// callback won't run; balance the wg.Add from AddMessage.
				mg.wg.Done()
			}
		}
		messagesToProcess := make([]IncomingMessage, len(group.Messages))
		copy(messagesToProcess, group.Messages)
		toProcess = append(toProcess, pending{key: key, group: &MessageGroup{
			Messages:  messagesToProcess,
			UserID:    group.UserID,
			StartedAt: group.StartedAt,
		}})
		delete(mg.groups, key)
	}
	mg.mu.Unlock()

	if len(toProcess) == 0 {
		return false
	}

	for _, p := range toProcess {
		mg.logger.Info("force closing session",
			slog.String("group", p.key),
			slog.Int("message_count", len(p.group.Messages)))
		mg.wg.Add(1)
		go func(key string, g *MessageGroup) {
			defer mg.wg.Done()
			userLock := mg.getProcessingLock(key)
			userLock.mu.Lock()
			defer func() {
				userLock.lastUsed = time.Now()
				userLock.mu.Unlock()
			}()
			mg.onGroupReady(context.Background(), g)
		}(p.key, p.group)
	}

	return true
}

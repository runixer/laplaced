package bot

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/telegram"

	"github.com/stretchr/testify/assert"
)

func TestMessageGrouper_SingleMessage(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	var groupProcessed *MessageGroup
	var wg sync.WaitGroup
	wg.Add(1)

	onGroupReady := func(ctx context.Context, group *MessageGroup) {
		groupProcessed = group
		wg.Done()
	}

	grouper := NewMessageGrouper(nil, logger, 10*time.Millisecond, onGroupReady)

	msg := &telegram.Message{
		MessageID: 1,
		From:      &telegram.User{ID: 123},
	}

	grouper.AddMessage(msg)

	wg.Wait()

	assert.NotNil(t, groupProcessed)
	assert.Len(t, groupProcessed.Messages, 1)
	assert.Equal(t, msg.MessageID, groupProcessed.Messages[0].MessageID)
}

func TestMessageGrouper_MultipleMessages(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	var groupProcessed *MessageGroup
	var wg sync.WaitGroup
	wg.Add(1)

	onGroupReady := func(ctx context.Context, group *MessageGroup) {
		groupProcessed = group
		wg.Done()
	}

	grouper := NewMessageGrouper(nil, logger, 50*time.Millisecond, onGroupReady)

	msg1 := &telegram.Message{MessageID: 1, From: &telegram.User{ID: 123}}
	msg2 := &telegram.Message{MessageID: 2, From: &telegram.User{ID: 123}}
	msg3 := &telegram.Message{MessageID: 3, From: &telegram.User{ID: 123}}

	grouper.AddMessage(msg1)
	time.Sleep(10 * time.Millisecond)
	grouper.AddMessage(msg2)
	time.Sleep(10 * time.Millisecond)
	grouper.AddMessage(msg3)

	wg.Wait()

	assert.NotNil(t, groupProcessed)
	assert.Len(t, groupProcessed.Messages, 3)
	assert.Equal(t, msg1.MessageID, groupProcessed.Messages[0].MessageID)
	assert.Equal(t, msg2.MessageID, groupProcessed.Messages[1].MessageID)
	assert.Equal(t, msg3.MessageID, groupProcessed.Messages[2].MessageID)
}

func TestMessageGrouper_TimerReset(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	var groupProcessed *MessageGroup
	var wg sync.WaitGroup
	wg.Add(1)

	onGroupReady := func(ctx context.Context, group *MessageGroup) {
		groupProcessed = group
		wg.Done()
	}

	grouper := NewMessageGrouper(nil, logger, 20*time.Millisecond, onGroupReady)

	msg1 := &telegram.Message{MessageID: 1, From: &telegram.User{ID: 123}}
	msg2 := &telegram.Message{MessageID: 2, From: &telegram.User{ID: 123}}

	grouper.AddMessage(msg1)
	time.Sleep(15 * time.Millisecond) // Less than the turnWait duration
	grouper.AddMessage(msg2)

	wg.Wait()

	assert.NotNil(t, groupProcessed)
	assert.Len(t, groupProcessed.Messages, 2)
}

// TestMessageGrouper_Stop_ProcessesPendingGroups verifies that Stop() processes
// pending message groups instead of dropping them. This is critical for graceful
// shutdown - messages sent during turnWait must not be lost because Telegram
// considers them already delivered.
func TestMessageGrouper_Stop_ProcessesPendingGroups(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	var groupsProcessed []*MessageGroup
	var mu sync.Mutex

	onGroupReady := func(ctx context.Context, group *MessageGroup) {
		mu.Lock()
		groupsProcessed = append(groupsProcessed, group)
		mu.Unlock()
	}

	// Use long turnWait to ensure timer doesn't fire before Stop()
	grouper := NewMessageGrouper(nil, logger, 1*time.Second, onGroupReady)

	// Add messages for two different users
	msg1 := &telegram.Message{MessageID: 1, From: &telegram.User{ID: 123}}
	msg2 := &telegram.Message{MessageID: 2, From: &telegram.User{ID: 456}}
	msg3 := &telegram.Message{MessageID: 3, From: &telegram.User{ID: 123}} // Same user as msg1

	grouper.AddMessage(msg1)
	grouper.AddMessage(msg2)
	grouper.AddMessage(msg3)

	// Stop should process both pending groups
	grouper.Stop()

	// Verify both groups were processed
	mu.Lock()
	defer mu.Unlock()

	assert.Len(t, groupsProcessed, 2, "Should process 2 pending groups (one per user)")

	// Find groups by user ID
	var user123Group, user456Group *MessageGroup
	for _, g := range groupsProcessed {
		switch g.UserID {
		case 123:
			user123Group = g
		case 456:
			user456Group = g
		}
	}

	assert.NotNil(t, user123Group, "Group for user 123 should be processed")
	assert.NotNil(t, user456Group, "Group for user 456 should be processed")

	assert.Len(t, user123Group.Messages, 2, "User 123 should have 2 messages")
	assert.Len(t, user456Group.Messages, 1, "User 456 should have 1 message")
}

// TestMessageGrouper_Stop_WaitsForActiveProcessing verifies that Stop() waits
// for active processing to complete before returning.
func TestMessageGrouper_Stop_WaitsForActiveProcessing(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	processingStarted := make(chan struct{})
	processingDone := make(chan struct{})

	onGroupReady := func(ctx context.Context, group *MessageGroup) {
		close(processingStarted)
		// Simulate slow processing
		time.Sleep(100 * time.Millisecond)
		close(processingDone)
	}

	// Short turnWait so timer fires quickly
	grouper := NewMessageGrouper(nil, logger, 10*time.Millisecond, onGroupReady)

	msg := &telegram.Message{MessageID: 1, From: &telegram.User{ID: 123}}
	grouper.AddMessage(msg)

	// Wait for processing to start
	<-processingStarted

	// Stop should block until processing completes
	stopDone := make(chan struct{})
	go func() {
		grouper.Stop()
		close(stopDone)
	}()

	// Verify Stop() hasn't returned yet
	select {
	case <-stopDone:
		t.Fatal("Stop() returned before processing completed")
	case <-time.After(50 * time.Millisecond):
		// Good, Stop is still waiting
	}

	// Wait for processing to complete
	<-processingDone

	// Now Stop should return
	select {
	case <-stopDone:
		// Good, Stop returned after processing completed
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Stop() didn't return after processing completed")
	}
}

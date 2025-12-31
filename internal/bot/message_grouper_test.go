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
	"github.com/stretchr/testify/mock"
)

type MockBot struct {
	mock.Mock
}

func (m *MockBot) processMessageGroup(ctx context.Context, group *MessageGroup) {
	m.Called(ctx, group)
}

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

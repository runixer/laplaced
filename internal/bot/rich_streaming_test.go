package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"
)

func newRichDraftTestSink(t *testing.T, api *testutil.MockBotAPI, cfgMax int) *richDraftSink {
	t.Helper()
	translator, err := i18n.NewTranslator("en")
	require.NoError(t, err)
	cfg := defaultStreamingCfg()
	if cfgMax > 0 {
		cfg.MaxBufferChars = cfgMax
	}
	sink := newRichDraftSink(
		context.Background(), api, translator, "en", cfg,
		123, 0, 42, testutil.TestLogger(),
	)
	require.True(t, sink.Active())
	t.Cleanup(func() { sink.Close() })
	allowNextRichDraftUpdate(sink)
	return sink
}

func setRichDraftSourceLimit(sink *richDraftSink, maxBytes int) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.maxSourceBytes = maxBytes
}

func expectInitialRichDraft(api *testutil.MockBotAPI) {
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return req.ChatID == 123 && req.MessageThreadID == nil && req.DraftID == 42 &&
			req.RichMessage.SkipEntityDetection &&
			strings.Contains(req.RichMessage.HTML, "<tg-thinking>") &&
			strings.Contains(req.RichMessage.HTML, "Thinking")
	})).Return(nil).Once()
}

func setRichDraftClock(sink *richDraftSink, clock *fakeClock) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.now = clock.Now
	sink.lastDraftAt = clock.Now()
	sink.lastAttemptAt = clock.Now().Add(-richDraftMinUpdateInterval)
}

func allowNextRichDraftUpdate(sink *richDraftSink) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	sink.lastAttemptAt = sink.now().Add(-richDraftMinUpdateInterval)
}

func TestRichDraftSink_InitialPreviewUsesStableTriggerID(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)

	sink := newRichDraftTestSink(t, api, 0)
	stats := sink.Close()

	assert.Equal(t, 1, stats.updates)
	api.AssertExpectations(t)
}

func TestRichDraftSink_PartialContentUsesSafeLinklessRichHTML(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		html := req.RichMessage.HTML
		return strings.Contains(html, "<h1>Heading</h1>") &&
			strings.Contains(html, "<tg-math>x^2</tg-math>") &&
			strings.Contains(html, "<strong>unfinished</strong>") &&
			strings.Contains(html, "safe label") &&
			!strings.Contains(strings.ToLower(html), "<a") &&
			!strings.Contains(strings.ToLower(html), "<img") &&
			!strings.Contains(strings.ToLower(html), "<script")
	})).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	sink.Delta("# Heading\n\n[safe label](https://example.com) ![alt](https://example.com/a.jpg) <script>x</script> $x^2$ **unfinished")
	sink.Close()

	// Terminal callbacks must never recreate or mutate an expired draft.
	sink.Delta(" late")
	sink.Status("internet_search", `{"query":"late"}`)
	sink.RAG("late")
	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 2)
	api.AssertExpectations(t)
}

func TestRichDraftSink_HidesGeneratedMediaDirectiveAndKeepsSurroundingContent(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		html := req.RichMessage.HTML
		return strings.Contains(html, "before") &&
			strings.Contains(html, "after") &&
			!strings.Contains(html, generatedMediaDirectiveStem)
	})).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	sink.Delta("before\n###MEDIA:1###\nafter")
	sink.Close()

	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 2)
	api.AssertExpectations(t)
}

func TestRichDraftSink_HidesAllTurnLocalProtocolAndArtifactReferences(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		html := req.RichMessage.HTML
		return strings.Contains(html, "before") && strings.Contains(html, "after") &&
			!strings.Contains(html, generatedMediaDirectiveStem) &&
			!strings.Contains(html, richSplitDelimiter) &&
			!strings.Contains(strings.ToLower(html), "artifact") &&
			!strings.Contains(html, "42")
	})).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	sink.Delta("before artifact:42\n###MEDIA:1###\n###SPLIT###\nafter")
	sink.Close()

	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 2)
	api.AssertExpectations(t)
}

func TestRichDraftSink_NeverFlashesChunkedArtifactReference(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		html := strings.ToLower(req.RichMessage.HTML)
		return strings.Contains(html, "visible") &&
			!strings.Contains(html, "artifact") && !strings.Contains(html, "42")
	})).Return(nil).Times(2)

	sink := newRichDraftTestSink(t, api, 0)
	clock := newFakeClock()
	setRichDraftClock(sink, clock)
	for i, delta := range []string{"visible artifact", ":", "42 tail"} {
		if i > 0 {
			clock.Advance(richDraftMinUpdateInterval)
		}
		sink.Delta(delta)
	}
	sink.Close()

	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 3)
	api.AssertExpectations(t)
}

func TestRichDraftSink_MediaDirectiveOnlyKeepsExistingThinkingPreview(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)

	sink := newRichDraftTestSink(t, api, 0)
	sink.Delta("###MEDIA:1###")
	stats := sink.Close()

	assert.Equal(t, 1, stats.updates, "hidden protocol line must not replace the thinking placeholder")
	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 1)
	api.AssertExpectations(t)
}

func TestRichDraftSink_StatusUsesThinkingAndEscapesArguments(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		html := req.RichMessage.HTML
		return strings.Contains(html, "<tg-thinking>") &&
			strings.Contains(html, "Thinking…<br>") &&
			strings.Contains(html, "Searching the web:") &&
			strings.Contains(html, "&lt;script&gt;") &&
			!strings.Contains(strings.ToLower(html), "artifact") &&
			!strings.Contains(html, "42") &&
			!strings.Contains(html, "Thinking…\n") &&
			!strings.Contains(html, "<script>")
	})).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	sink.Status("internet_search", `{"query":"<script>alert(1)</script> artifact:42"}`)
	sink.Close()

	api.AssertExpectations(t)
}

func TestRichDraftSink_ThrottlesSmallDeltasAndSendsLatestSnapshot(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, "a")
	})).Return(nil).Once()
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, "abc")
	})).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	clock := newFakeClock()
	setRichDraftClock(sink, clock)

	sink.Delta("a") // first content is eligible after the test backdates the floor
	sink.Delta("b") // below time and character thresholds
	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 2)
	clock.Advance(1300 * time.Millisecond)
	sink.Delta("c")
	sink.Close()

	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 3)
	api.AssertExpectations(t)
}

func TestRichDraftSink_CoalescesRapidStatusAndDeltaBurstsWithinFloodBudget(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.Anything).Return(nil)

	sink := newRichDraftTestSink(t, api, 20_000)
	clock := newFakeClock()
	sink.mu.Lock()
	sink.now = clock.Now
	sink.lastDraftAt = clock.Now()
	sink.lastAttemptAt = clock.Now()
	sink.mu.Unlock()

	for i := 0; i < 300; i++ {
		clock.Advance(100 * time.Millisecond)
		sink.Status("internet_search", fmt.Sprintf(`{"query":"q-%03d"}`, i))
		sink.Delta("0123456789")
	}
	stats := sink.Close()

	// Thirty simulated seconds at the 1.2-second floor can produce at most 25
	// progressive calls plus the constructor's initial draft.
	assert.LessOrEqual(t, stats.updates, 26)
	api.AssertExpectations(t)
}

func TestRichDraftSink_ScheduledRefreshSendsLatestSnapshot(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	sent := make(chan struct{}, 1)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, "first latest")
	})).Run(func(mock.Arguments) {
		sent <- struct{}{}
	}).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	sink.mu.Lock()
	sink.lastDraftAt = sink.now()
	sink.lastAttemptAt = sink.lastDraftAt
	sink.mu.Unlock()
	sink.Delta("first")
	sink.Delta(" latest")

	select {
	case <-sent:
	case <-time.After(3 * time.Second):
		t.Fatal("coalesced rich draft snapshot was not sent")
	}
	sink.Close()
	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 2)
	api.AssertExpectations(t)
}

func TestRichDraftSink_TransientFailureBacksOffAndCoalescesLatestSnapshot(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, "first")
	})).Return(errors.New("temporary network failure")).Once()
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, "recovered")
	})).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	clock := newFakeClock()
	setRichDraftClock(sink, clock)
	sink.Delta("first")

	for i := 0; i < 10; i++ {
		sink.Delta(strings.Repeat("x", 100))
	}
	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 2)

	clock.Advance(richDraftTransientBackoff - time.Millisecond)
	sink.Delta(" still cooling down")
	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 2)

	clock.Advance(time.Millisecond)
	sink.Delta(" recovered")
	sink.Close()

	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 3)
	api.AssertExpectations(t)
}

func TestRichDraftSink_HeartbeatHonorsRetryAfterAndRetriesSamePayload(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.Anything).
		Return(&telegram.APIError{
			Code:        429,
			Description: "Too Many Requests",
			Parameters:  &telegram.ResponseParameters{RetryAfter: 7},
		}).Once()
	api.On("SendRichMessageDraft", mock.Anything, mock.Anything).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	clock := newFakeClock()
	setRichDraftClock(sink, clock)
	clock.Advance(richDraftHeartbeatInterval)

	sink.mu.Lock()
	sink.heartbeatLocked()
	assert.Equal(t, clock.Now().Add(7*time.Second), sink.cooldownTill)
	sink.mu.Unlock()
	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 2)

	clock.Advance(7 * time.Second)
	sink.mu.Lock()
	sink.refreshLocked(true)
	sink.mu.Unlock()
	sink.Close()

	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 3)
	api.AssertExpectations(t)
}

func TestRichDraftSink_CloseWaitsForHeartbeatAndBlocksLateCallbacks(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	started := make(chan struct{})
	release := make(chan struct{})
	api.On("SendRichMessageDraft", mock.Anything, mock.Anything).
		Run(func(mock.Arguments) {
			close(started)
			<-release
		}).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	clock := newFakeClock()
	setRichDraftClock(sink, clock)
	clock.Advance(richDraftHeartbeatInterval)

	heartbeatDone := make(chan struct{})
	go func() {
		sink.mu.Lock()
		sink.heartbeatLocked()
		sink.mu.Unlock()
		close(heartbeatDone)
	}()
	<-started

	closeDone := make(chan struct{})
	go func() {
		sink.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("Close returned while heartbeat still held the sink mutex")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	<-heartbeatDone
	<-closeDone
	sink.Delta("late")
	sink.Status("internet_search", `{"query":"late"}`)

	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 2)
	api.AssertExpectations(t)
}

func TestRichDraftSink_FinalizePreviewFlushesUnsentCoalescedTail(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, "prefix") &&
			!strings.Contains(req.RichMessage.HTML, "TERMINAL_TAIL")
	})).Return(nil).Once()
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, "prefix") &&
			strings.Contains(req.RichMessage.HTML, "TERMINAL_TAIL")
	})).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	clock := newFakeClock()
	setRichDraftClock(sink, clock)

	sink.Delta("prefix")
	sink.Delta(strings.Repeat("x", 80) + " TERMINAL_TAIL")
	sink.mu.Lock()
	require.NotNil(t, sink.refreshTimer, "tail should be waiting behind the peer-rate floor")
	sink.mu.Unlock()

	stats := sink.FinalizePreview()
	statsAgain := sink.FinalizePreview()
	statsClosed := sink.Close()
	sink.Delta(" late")
	sink.Status("internet_search", `{"query":"late"}`)
	sink.RAG("late")

	assert.Equal(t, 3, stats.updates, "initial, prefix, terminal catch-up")
	assert.Equal(t, 2, stats.contentSnapshots)
	assert.Equal(t, richDraftCatchupSent, stats.terminalCatchup)
	assert.Equal(t, stats, statsAgain, "terminal finalization must be idempotent")
	assert.Equal(t, stats, statsClosed, "cleanup close must not double-send or change accounting")
	sink.mu.Lock()
	assert.True(t, sink.finalized)
	assert.False(t, sink.active)
	assert.Nil(t, sink.refreshTimer)
	sink.mu.Unlock()
	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 3)
	api.AssertExpectations(t)
}

func TestRichDraftSink_FinalizePreviewSkipsTailWhenCooldownExceedsBudget(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.Anything).
		Return(&telegram.APIError{
			Code:        429,
			Description: "Too Many Requests",
			Parameters:  &telegram.ResponseParameters{RetryAfter: 7},
		}).Once()

	sink := newRichDraftTestSink(t, api, 0)
	clock := newFakeClock()
	setRichDraftClock(sink, clock)
	sink.Delta("prefix")
	sink.Delta(strings.Repeat("x", 80) + " TERMINAL_TAIL")

	stats := sink.FinalizePreview()

	assert.Equal(t, 2, stats.updates, "initial plus the throttled attempt; no terminal retry")
	assert.Zero(t, stats.contentSnapshots)
	assert.Equal(t, richDraftCatchupSkippedCooldown, stats.terminalCatchup)
	sink.mu.Lock()
	assert.Nil(t, sink.refreshTimer)
	assert.True(t, sink.finalized)
	sink.mu.Unlock()
	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 2)
	api.AssertExpectations(t)
}

func TestRichDraftSink_DoesNotReuseLegacyMaxBufferChars(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, "123456789")
	})).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 8)
	sink.Delta("123456789")
	stats := sink.Close()

	assert.False(t, stats.overflow)
	assert.Equal(t, 1, stats.contentSnapshots)
	assert.Equal(t, 2, stats.updates)
	api.AssertExpectations(t)
}

func TestRichDraftSink_OverflowAppendsValidUTF8PrefixAndSendsIt(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	var frozenPayload string
	api.On("SendRichMessageDraft", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			frozenPayload = args.Get(1).(telegram.SendRichMessageDraftRequest).RichMessage.HTML
		}).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	setRichDraftSourceLimit(sink, 7)
	sink.Delta("aaaaaé🙂TAIL")
	sink.Delta(strings.Repeat("x", 10_000))
	sink.Status("internet_search", `{"query":"late"}`)
	sink.RAG("late")
	stats := sink.Close()

	sink.mu.Lock()
	buffer := sink.buf.String()
	lastPayload := sink.lastPayload
	lastDraftLen := sink.lastDraftLen
	timer := sink.refreshTimer
	sink.mu.Unlock()

	assert.Equal(t, "aaaaaé", buffer)
	assert.True(t, utf8.ValidString(buffer))
	assert.Equal(t, 7, len(buffer))
	assert.Contains(t, frozenPayload, "aaaaaé")
	assert.NotContains(t, frozenPayload, "🙂")
	assert.NotContains(t, frozenPayload, "TAIL")
	assert.Equal(t, frozenPayload, lastPayload)
	assert.Equal(t, len(buffer), lastDraftLen)
	assert.Nil(t, timer)
	assert.True(t, stats.overflow)
	assert.Equal(t, 1, stats.contentSnapshots)
	assert.Equal(t, 2, stats.updates)
	api.AssertExpectations(t)
}

func TestRichDraftSink_OverflowPreservesPendingFrozenSnapshot(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	sent := make(chan struct{}, 1)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, "12345678")
	})).Run(func(mock.Arguments) {
		sent <- struct{}{}
	}).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	setRichDraftSourceLimit(sink, 8)
	sink.mu.Lock()
	sink.lastAttemptAt = sink.now()
	sink.mu.Unlock()

	sink.Delta("1234")
	sink.Delta("56789")
	sink.mu.Lock()
	require.True(t, sink.overflow)
	require.NotNil(t, sink.refreshTimer, "crossing the cap must retain the coalesced refresh")
	sink.mu.Unlock()

	select {
	case <-sent:
	case <-time.After(3 * time.Second):
		t.Fatal("capped rich draft snapshot was not sent by the pending timer")
	}
	stats := sink.Close()

	assert.True(t, stats.overflow)
	assert.Equal(t, 1, stats.contentSnapshots)
	assert.Equal(t, 2, stats.updates)
	api.AssertExpectations(t)
}

func TestRichDraftSink_OverflowHeartbeatReplaysExactFrozenSnapshot(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	var frozenPayload string
	api.On("SendRichMessageDraft", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			frozenPayload = args.Get(1).(telegram.SendRichMessageDraftRequest).RichMessage.HTML
		}).Return(nil).Once()
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return req.RichMessage.HTML == frozenPayload
	})).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	setRichDraftSourceLimit(sink, 8)
	clock := newFakeClock()
	setRichDraftClock(sink, clock)
	sink.mu.Lock()
	sink.lastAttemptAt = clock.Now()
	sink.mu.Unlock()
	sink.Status("internet_search", `{"query":"before"}`)
	allowNextRichDraftUpdate(sink)
	sink.Delta("123456789")
	sink.Status("internet_search", `{"query":"after"}`)
	sink.RAG("after")

	clock.Advance(richDraftHeartbeatInterval)
	sink.mu.Lock()
	sink.heartbeatLocked()
	sink.mu.Unlock()
	stats := sink.Close()

	assert.True(t, stats.overflow)
	assert.Equal(t, 1, stats.contentSnapshots)
	assert.Equal(t, 3, stats.updates, "initial, capped content and heartbeat")
	api.AssertExpectations(t)
}

func TestRichDraftSink_FrozenSnapshotTransientFailureRetriesSamePayload(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	var attemptedPayload string
	api.On("SendRichMessageDraft", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			attemptedPayload = args.Get(1).(telegram.SendRichMessageDraftRequest).RichMessage.HTML
		}).Return(errors.New("temporary network failure")).Once()
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return req.RichMessage.HTML == attemptedPayload
	})).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	setRichDraftSourceLimit(sink, 8)
	clock := newFakeClock()
	setRichDraftClock(sink, clock)
	sink.Delta("123456789")

	clock.Advance(richDraftTransientBackoff)
	sink.mu.Lock()
	sink.refreshLocked(false)
	sink.mu.Unlock()
	stats := sink.Close()

	assert.True(t, stats.overflow)
	assert.Equal(t, 1, stats.contentSnapshots)
	assert.Equal(t, 3, stats.updates)
	api.AssertExpectations(t)
}

func TestRichDraftSink_RealBudgetLeavesRoomForMaxStatusJourney(t *testing.T) {
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, strings.Repeat("a", 100)) &&
			strings.Contains(req.RichMessage.HTML, "Searching the web")
	})).Return(nil).Once()

	sink := newRichDraftTestSink(t, api, 0)
	sink.mu.Lock()
	sink.lastAttemptAt = sink.now()
	sink.mu.Unlock()
	for i := 0; i < richDraftMaxStatusLines-1; i++ {
		sink.Status("internet_search", fmt.Sprintf(
			`{"query":"%02d-%s"}`, i, strings.Repeat("q", streamingMaxStatusArgChars),
		))
	}
	allowNextRichDraftUpdate(sink)
	sink.Delta(strings.Repeat("a", richDraftMaxSourceBytes+1))
	stats := sink.Close()

	sink.mu.Lock()
	buffer := sink.buf.String()
	statusLines := len(sink.statusLog)
	frozenPayload := sink.frozenPayload
	sink.mu.Unlock()
	assert.Len(t, buffer, richDraftMaxSourceBytes)
	assert.True(t, utf8.ValidString(buffer))
	assert.Equal(t, richDraftMaxStatusLines, statusLines)
	assert.NotEmpty(t, frozenPayload)
	assert.True(t, stats.overflow)
	assert.Equal(t, 1, stats.contentSnapshots)
	assert.Equal(t, 2, stats.updates)
	api.AssertExpectations(t)
}

func TestResponsePath_RichDraftIsPreviewOnlyAndFinalOwnsHistory(t *testing.T) {
	transport := &recordingRichTransport{ids: map[int]string{0: "telegram-final-77"}}
	bot := newRichDeliveryTestBot(t, transport)
	bot.cfg.Telegram.RichMessages.DraftStreamingEnabled = true
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, "partial")
	})).Return(nil).Once()
	bot.api = api

	store := new(testutil.MockStorage)
	bot.msgRepo = store
	store.On("AddMessageToHistory", storage.ScopeID("user"), mock.MatchedBy(func(message storage.Message) bool {
		return message.Role == "assistant" && message.Content == "# Final answer"
	})).Return(nil).Once()
	store.On("SetReplyTransportID", storage.ScopeID("user"), "telegram-final-77").Return(nil).Once()

	path := bot.newResponsePath(
		context.Background(), storage.ScopeID("user"), "123", true,
		"123", "", "42", bot.logger,
	)
	require.True(t, path.usesRichDraft())
	assert.Nil(t, path.sink)
	allowNextRichDraftUpdate(path.richDraft)
	path.streamDelta("partial")

	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	ok := path.sendFinalAndPersist(ctx, span, "# Final answer", nil)
	span.End()

	require.True(t, ok)
	assert.False(t, path.usesRichDraft())
	require.Len(t, transport.responses, 1, "drafts must not count as persistent sends")
	assert.Equal(t, ResponseFormatRichHTML, transport.responses[0].Format)
	assert.Equal(t, "telegram-final-77", path.deliveredMessageID)
	assert.Equal(t, 3, path.tgCalls, "two draft snapshots plus one persistent final")
	assert.Equal(t, richDraftCatchupSkippedNoTail, path.richDraft.stats.terminalCatchup)
	store.AssertExpectations(t)
	api.AssertExpectations(t)
}

func TestResponsePath_RichDraftOverflowStillDeliversAndPersistsFullFinal(t *testing.T) {
	transport := &recordingRichTransport{ids: map[int]string{0: "telegram-final-88"}}
	bot := newRichDeliveryTestBot(t, transport)
	bot.cfg.Telegram.RichMessages.DraftStreamingEnabled = true
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	var previewPayload string
	api.On("SendRichMessageDraft", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			previewPayload = args.Get(1).(telegram.SendRichMessageDraftRequest).RichMessage.HTML
		}).Return(nil).Once()
	bot.api = api

	full := "# Full answer\n\n" + strings.Repeat("x", 40) + " FINAL_TAIL"
	store := new(testutil.MockStorage)
	bot.msgRepo = store
	store.On("AddMessageToHistory", storage.ScopeID("user"), mock.MatchedBy(func(message storage.Message) bool {
		return message.Role == "assistant" && message.Content == full
	})).Return(nil).Once()
	store.On("SetReplyTransportID", storage.ScopeID("user"), "telegram-final-88").Return(nil).Once()

	path := bot.newResponsePath(
		context.Background(), storage.ScopeID("user"), "123", true,
		"123", "", "42", bot.logger,
	)
	require.True(t, path.usesRichDraft())
	setRichDraftSourceLimit(path.richDraft, 8)
	allowNextRichDraftUpdate(path.richDraft)
	path.streamDelta(full)

	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	ok := path.sendFinalAndPersist(ctx, span, full, nil)
	span.End()

	require.True(t, ok)
	assert.NotContains(t, previewPayload, "FINAL_TAIL")
	require.Len(t, transport.responses, 1)
	assert.Contains(t, transport.responses[0].Text, "FINAL_TAIL")
	assert.Equal(t, "telegram-final-88", path.deliveredMessageID)
	assert.True(t, path.richDraft.Close().overflow)
	store.AssertExpectations(t)
	api.AssertExpectations(t)
}

func TestResponsePath_RichDraftTerminalCatchupFailureDoesNotBlockSplitFinal(t *testing.T) {
	transport := &recordingRichTransport{ids: map[int]string{
		0: "telegram-final-1",
		1: "telegram-final-2",
	}}
	bot := newRichDeliveryTestBot(t, transport)
	bot.cfg.Telegram.RichMessages.DraftStreamingEnabled = true
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, "First section") &&
			!strings.Contains(req.RichMessage.HTML, "Second section") &&
			!strings.Contains(req.RichMessage.HTML, richSplitDelimiter)
	})).Return(nil).Once()
	api.On("SendRichMessageDraft", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageDraftRequest) bool {
		return strings.Contains(req.RichMessage.HTML, "First section") &&
			strings.Contains(req.RichMessage.HTML, "Second section") &&
			!strings.Contains(req.RichMessage.HTML, richSplitDelimiter)
	})).Run(func(args mock.Arguments) {
		assert.Empty(t, transport.responses, "terminal catch-up must precede every persistent split operation")
		ctx := args.Get(0).(context.Context)
		deadline, ok := ctx.Deadline()
		require.True(t, ok, "terminal catch-up must carry a hard request deadline")
		remaining := time.Until(deadline)
		assert.Positive(t, remaining)
		assert.LessOrEqual(t, remaining, richDraftTerminalCatchupBudget)
	}).Return(errors.New("terminal preview timeout with unknown outcome")).Once()
	bot.api = api

	path := bot.newResponsePath(
		context.Background(), storage.ScopeID("user"), "123", true,
		"123", "", "42", bot.logger,
	)
	require.True(t, path.usesRichDraft())
	clock := newFakeClock()
	setRichDraftClock(path.richDraft, clock)

	first := "# First section\n\nVisible prefix.\n"
	second := "\n###SPLIT###\n\n# Second section\n\n" + strings.Repeat("tail ", 20)
	full := first + second
	path.streamDelta(first)
	path.streamDelta(second)

	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	ok := path.sendFinal(ctx, span, full)
	span.End()

	require.True(t, ok, "ephemeral catch-up failure must not affect persistent delivery")
	require.Len(t, transport.responses, 2, "standalone split marker must still produce two persistent operations")
	assert.Contains(t, transport.responses[0].Text, "First section")
	assert.Contains(t, transport.responses[1].Text, "Second section")
	assert.Equal(t, []string{"telegram-final-1", "telegram-final-2"}, path.deliveredMessageIDs)
	assert.Equal(t, 5, path.tgCalls, "initial, prefix, catch-up and two persistent operations")
	assert.Equal(t, richDraftCatchupFailed, path.richDraft.stats.terminalCatchup)

	path.streamDelta(" late")
	path.streamStatus("internet_search", `{"query":"late"}`)
	path.streamRAG("late")
	api.AssertNumberOfCalls(t, "SendRichMessageDraft", 3)
	api.AssertExpectations(t)
}

func TestResponsePath_RichDraftFinalUnknownIsNotPersistedOrResent(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{
		0: errors.New("connection reset after final request write"),
	}}
	bot := newRichDeliveryTestBot(t, transport)
	bot.cfg.Telegram.RichMessages.DraftStreamingEnabled = true
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	bot.api = api
	store := new(testutil.MockStorage)
	bot.msgRepo = store

	path := bot.newResponsePath(
		context.Background(), storage.ScopeID("user"), "123", true,
		"123", "", "42", bot.logger,
	)
	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	ok := path.sendFinalAndPersist(ctx, span, "# Final answer", nil)
	span.End()

	assert.False(t, ok)
	require.Len(t, transport.responses, 1)
	store.AssertNotCalled(t, "AddMessageToHistory", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "SetReplyTransportID", mock.Anything, mock.Anything)
	api.AssertExpectations(t)
}

func TestResponsePath_RichDraftRejectionFallsBackToBufferedFinal(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	bot.cfg.Telegram.RichMessages.DraftStreamingEnabled = true
	api := new(testutil.MockBotAPI)
	api.On("SendRichMessageDraft", mock.Anything, mock.Anything).
		Return(&telegram.APIError{Code: 400, Description: "draft unsupported"}).Once()
	bot.api = api

	path := bot.newResponsePath(
		context.Background(), storage.ScopeID("user"), "123", true,
		"123", "", "42", bot.logger,
	)
	assert.False(t, path.usesStreaming())

	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	ok := path.sendFinal(ctx, span, "# Buffered final")
	span.End()

	require.True(t, ok)
	require.Len(t, transport.responses, 1)
	assert.Equal(t, ResponseFormatRichHTML, transport.responses[0].Format)
	assert.Equal(t, 2, path.tgCalls, "one rejected draft plus one persistent final")
	api.AssertExpectations(t)
}

func TestResponsePath_RichDraftErrorClosesPreviewBeforePersistentError(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	bot.cfg.Telegram.RichMessages.DraftStreamingEnabled = true
	api := new(testutil.MockBotAPI)
	expectInitialRichDraft(api)
	bot.api = api

	path := bot.newResponsePath(
		context.Background(), storage.ScopeID("user"), "123", true,
		"123", "", "42", bot.logger,
	)
	require.True(t, path.usesRichDraft())
	path.sendError(context.Background(), "temporary error")

	assert.False(t, path.usesRichDraft())
	require.Len(t, transport.responses, 1)
	assert.Equal(t, ResponseFormatDefault, transport.responses[0].Format)
	assert.Equal(t, 2, path.tgCalls, "one draft plus one persistent error")
	api.AssertExpectations(t)
}

func TestNewResponsePath_NoRichDraftOutsideEligiblePrivateContext(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	bot.cfg.Telegram.RichMessages.DraftStreamingEnabled = true
	bot.cfg.Bot.Streaming.Enabled = true
	api := new(testutil.MockBotAPI)
	api.On("SendMessage", mock.Anything, mock.Anything).
		Return(&telegram.Message{MessageID: 7}, nil).Once()
	bot.api = api

	path := bot.newResponsePath(
		context.Background(), storage.ScopeID("user"), "123", false,
		"-100123", "", "42", bot.logger,
	)

	assert.False(t, path.usesRichDraft())
	assert.NotNil(t, path.sink, "non-rich contexts retain the established persistent edit stream")
	api.AssertNotCalled(t, "SendRichMessageDraft", mock.Anything, mock.Anything)
	api.AssertExpectations(t)
}

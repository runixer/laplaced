package bot

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// fakeClock is an injectable time source. NextTick advances time by the given
// duration; Now returns the current value.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func defaultStreamingCfg() config.StreamingConfig {
	return config.StreamingConfig{
		Enabled:        true,
		EditThrottleMs: 1000,
		EditMinChars:   80,
		MaxBufferChars: 3400,
	}
}

func newSinkWithMockAPI(t *testing.T) (*streamSink, *testutil.MockBotAPI, *fakeClock, *i18n.Translator) {
	t.Helper()
	api := new(testutil.MockBotAPI)
	api.On("SendMessage", mock.Anything, mock.Anything).Return(
		&telegram.Message{MessageID: 42, Chat: &telegram.Chat{ID: 1}}, nil,
	).Once()

	translator, terr := i18n.NewTranslator("en")
	require.NoError(t, terr)
	clock := newFakeClock()
	cfg := defaultStreamingCfg()
	sink := newStreamSink(
		context.Background(), api, translator, "en", cfg,
		1, 0, 7, testutil.TestLogger(),
	)
	require.NotNil(t, sink)
	require.Equal(t, 42, sink.msgID)
	sink.now = clock.Now
	return sink, api, clock, translator
}

func TestStreamSink_PlaceholderSentOnConstruction(t *testing.T) {
	api := new(testutil.MockBotAPI)
	api.On("SendMessage", mock.Anything, mock.MatchedBy(func(req telegram.SendMessageRequest) bool {
		return req.ChatID == 99 &&
			req.ReplyToMessageID == 7 &&
			strings.Contains(req.Text, "Thinking")
	})).Return(&telegram.Message{MessageID: 42}, nil).Once()

	translator, terr := i18n.NewTranslator("en")
	require.NoError(t, terr)
	sink := newStreamSink(
		context.Background(), api, translator, "en",
		defaultStreamingCfg(), 99, 0, 7, testutil.TestLogger(),
	)
	require.NotNil(t, sink)
	assert.Equal(t, 42, sink.msgID)
	api.AssertExpectations(t)
}

func TestStreamSink_StatusReplacesPlaceholder(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		// search_history's bare (no-arg) status in EN.
		return req.MessageID == 42 && strings.Contains(req.Text, "Recalling") && req.ParseMode == "HTML"
	})).Return(&telegram.Message{MessageID: 42}, nil).Once()

	sink.Status("search_history", "")
	api.AssertExpectations(t)
}

// TestStreamSink_StatusWithArg verifies that when a tool's primary argument
// is present in arguments JSON, the localized "%s" variant is used and the
// argument is HTML-escaped and inlined.
func TestStreamSink_StatusWithArg(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		// Expect: "🔍 Recalling: <i>что писал Петров</i>"
		return req.ParseMode == "HTML" &&
			strings.Contains(req.Text, "Recalling:") &&
			strings.Contains(req.Text, "<i>") &&
			strings.Contains(req.Text, "what Bob wrote")
	})).Return(&telegram.Message{}, nil).Once()

	sink.Status("search_history", `{"query":"what Bob wrote"}`)
	api.AssertExpectations(t)
}

// TestStreamSink_StatusArgHTMLEscaped guards against arg text containing
// HTML metacharacters from breaking the rendered status.
func TestStreamSink_StatusArgHTMLEscaped(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		// "<script>" must NOT survive as a tag.
		return req.ParseMode == "HTML" &&
			!strings.Contains(req.Text, "<script>") &&
			strings.Contains(req.Text, "&lt;script&gt;")
	})).Return(&telegram.Message{}, nil).Once()

	sink.Status("internet_search", `{"query":"<script>alert(1)</script>"}`)
	api.AssertExpectations(t)
}

// TestStreamSink_StatusArgTruncation verifies long args are clipped with
// an ellipsis instead of producing a massive status line.
func TestStreamSink_StatusArgTruncation(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		// Truncation marker is the horizontal ellipsis "…".
		return req.ParseMode == "HTML" &&
			strings.Contains(req.Text, "Drawing:") &&
			strings.Contains(req.Text, "…")
	})).Return(&telegram.Message{}, nil).Once()

	prompt := strings.Repeat("a samurai cat in golden armor at dawn, ", 10) // ~390 chars
	sink.Status("generate_image", `{"prompt":"`+prompt+`"}`)
	api.AssertExpectations(t)
}

// TestStreamSink_RAGShowsEnrichedQuery verifies the dedicated RAG status edit.
func TestStreamSink_RAGShowsEnrichedQuery(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" &&
			strings.Contains(req.Text, "Searching memory:") &&
			strings.Contains(req.Text, "<i>vLLM on H100</i>")
	})).Return(&telegram.Message{}, nil).Once()

	sink.RAG("vLLM on H100")
	api.AssertExpectations(t)
}

// TestStreamSink_StatusAppendedInContentMode verifies the central fix: a
// tool's Status edit that arrives AFTER the model has already streamed a
// content preamble must still appear above the streaming text (some
// models emit "Let me check…" before issuing tool_calls). The bubble
// becomes "<blockquote with new step>...\n\n<preamble text>".
func TestStreamSink_StatusAppendedInContentMode(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)

	// Initial content delta — flips to content mode.
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" && strings.Contains(req.Text, "Let me check")
	})).Return(&telegram.Message{}, nil).Once()
	sink.Delta("Let me check that")

	// Status fires after content — must re-edit with blockquote prepended,
	// not silently no-op.
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" &&
			strings.HasPrefix(req.Text, "<blockquote expandable>") &&
			strings.Contains(req.Text, "Searching the web:") &&
			strings.Contains(req.Text, "Subnautica 2") &&
			strings.Contains(req.Text, "</blockquote>\n\n") &&
			strings.Contains(req.Text, "Let me check")
	})).Return(&telegram.Message{}, nil).Once()
	sink.Status("internet_search", `{"query":"Subnautica 2"}`)

	api.AssertExpectations(t)
}

// TestStreamSink_StatusJourneyAccumulates verifies that each step appends
// to a blockquote keeping all prior steps visible, and that the initial
// "Thinking…" placeholder is seeded as the first journey entry.
func TestStreamSink_StatusJourneyAccumulates(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)

	// Step 1: RAG enrichment. The placeholder ("💭 Thinking…") should be
	// seeded as line 1 and the RAG line as line 2, all wrapped in
	// <blockquote expandable>.
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" &&
			strings.HasPrefix(req.Text, "<blockquote expandable>") &&
			strings.Contains(req.Text, "Thinking") &&
			strings.Contains(req.Text, "Searching memory:") &&
			strings.Contains(req.Text, "vLLM on H100")
	})).Return(&telegram.Message{}, nil).Once()
	sink.RAG("vLLM on H100")

	// Step 2: search_history tool. Both prior lines must still be present.
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" &&
			strings.Contains(req.Text, "Thinking") &&
			strings.Contains(req.Text, "Searching memory:") && // RAG line still there
			strings.Contains(req.Text, "Recalling:") &&
			strings.Contains(req.Text, "Petrov")
	})).Return(&telegram.Message{}, nil).Once()
	sink.Status("search_history", `{"query":"Petrov"}`)

	// Step 3: internet_search. Now three lines in the journey.
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" &&
			strings.Contains(req.Text, "Thinking") &&
			strings.Contains(req.Text, "Searching memory:") &&
			strings.Contains(req.Text, "Recalling:") &&
			strings.Contains(req.Text, "Searching the web:") &&
			strings.Contains(req.Text, "benchmark")
	})).Return(&telegram.Message{}, nil).Once()
	sink.Status("internet_search", `{"query":"vLLM benchmark"}`)

	api.AssertExpectations(t)
}

// TestStreamSink_StatusPrefixedAboveContent verifies that once content
// streaming begins, every content edit is prefixed with the accumulated
// status journey (so previous steps remain pinned above the answer).
func TestStreamSink_StatusPrefixedAboveContent(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)

	// Status edit
	api.On("EditMessageText", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil).Once()
	sink.RAG("test query")

	// Content edit must carry the blockquote prefix AND the rendered content.
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" &&
			strings.HasPrefix(req.Text, "<blockquote expandable>") &&
			strings.Contains(req.Text, "</blockquote>\n\n") &&
			strings.Contains(req.Text, "Answer")
	})).Return(&telegram.Message{}, nil).Once()
	sink.Delta("Answer")

	api.AssertExpectations(t)
}

// TestStreamSink_NoStatusNoPrefix verifies that simple turns (no RAG, no
// tools) emit content WITHOUT a blockquote prefix. We don't want a
// vestigial "💭 Thinking…" hanging above every short answer.
func TestStreamSink_NoStatusNoPrefix(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" &&
			!strings.Contains(req.Text, "blockquote") &&
			req.Text == "Hello"
	})).Return(&telegram.Message{}, nil).Once()

	sink.Delta("Hello")
	api.AssertExpectations(t)
}

// TestStreamSink_FinalizePrefixesStatusJourney verifies that the final
// HTML edit also carries the status journey on top.
func TestStreamSink_FinalizePrefixesStatusJourney(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)

	api.On("EditMessageText", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil).Once()
	sink.RAG("vLLM")

	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" &&
			strings.HasPrefix(req.Text, "<blockquote expandable>") &&
			strings.Contains(req.Text, "</blockquote>\n\n<b>Final</b>")
	})).Return(&telegram.Message{}, nil).Once()

	finalizer := func(text string) ([]telegram.SendMessageRequest, error) {
		return []telegram.SendMessageRequest{{Text: "<b>Final</b>", ParseMode: "HTML"}}, nil
	}
	extra, _ := sink.Finalize(finalizeArgs{FullText: "**Final**"}, finalizer)
	assert.Empty(t, extra)
	api.AssertExpectations(t)
}

func TestStreamSink_StatusIdempotentSameTool(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)
	// Only ONE edit expected — the second Status call with the same tool
	// should be a no-op because the localized text didn't change.
	api.On("EditMessageText", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil).Once()

	sink.Status("search_history", "")
	sink.Status("search_history", "")
	api.AssertExpectations(t)
}

func TestStreamSink_StatusGenericFallbackForUnknownTool(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return strings.Contains(req.Text, "Running tool") && req.ParseMode == "HTML"
	})).Return(&telegram.Message{}, nil).Once()

	sink.Status("nonexistent_tool", "")
	api.AssertExpectations(t)
}

func TestStreamSink_FirstDeltaImmediateEdit(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)
	// First content delta should produce an HTML-rendered edit (balancer +
	// markdown.ToHTML pipeline). Plain "Hello" round-trips to "Hello".
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.Text == "Hello" && req.ParseMode == "HTML"
	})).Return(&telegram.Message{}, nil).Once()

	sink.Delta("Hello")
	api.AssertExpectations(t)
	assert.Equal(t, streamModeContent, sink.mode)
}

func TestStreamSink_DeltaThrottleByTime(t *testing.T) {
	sink, api, clock, _ := newSinkWithMockAPI(t)

	// First delta: immediate edit (status → content transition).
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.Text == "abc" && req.ParseMode == "HTML"
	})).Return(&telegram.Message{}, nil).Once()
	sink.Delta("abc")

	// Throttled — only 100ms elapsed, no min-chars trigger (10 chars < 80).
	clock.Advance(100 * time.Millisecond)
	sink.Delta("def")
	// no edit expected here

	// After 1100ms total, throttle elapses; next delta should edit.
	clock.Advance(1000 * time.Millisecond)
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.Text == "abcdefghi" && req.ParseMode == "HTML"
	})).Return(&telegram.Message{}, nil).Once()
	sink.Delta("ghi")

	api.AssertExpectations(t)
}

func TestStreamSink_DeltaThrottleByMinChars(t *testing.T) {
	sink, api, clock, _ := newSinkWithMockAPI(t)

	// First edit (immediate).
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.Text == "x" && req.ParseMode == "HTML"
	})).Return(&telegram.Message{}, nil).Once()
	sink.Delta("x")

	// 100ms later — no time trigger; but a 90-char delta exceeds min_chars=80.
	clock.Advance(100 * time.Millisecond)
	big := strings.Repeat("a", 90)
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.Text == "x"+big && req.ParseMode == "HTML"
	})).Return(&telegram.Message{}, nil).Once()
	sink.Delta(big)

	api.AssertExpectations(t)
}

func TestStreamSink_OverflowStopsEditing(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)

	// First edit (immediate).
	api.On("EditMessageText", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil).Once()
	sink.Delta("seed")

	// Push past the 3400-char cap. After that, no more edits even though
	// throttle has elapsed (we don't advance the clock — but mode=overflow
	// short-circuits before the throttle check).
	huge := strings.Repeat("y", 3500)
	sink.Delta(huge)
	assert.Equal(t, streamModeOverflow, sink.mode)
	assert.Greater(t, sink.buf.Len(), 3400)

	// Subsequent deltas should NOT generate edits.
	sink.Delta("more")

	api.AssertExpectations(t)
}

func TestStreamSink_FinalizeNormalRendersHTML(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)

	// First Delta: immediate HTML edit. The balancer leaves "**bold** ok"
	// (already balanced) untouched; goldmark renders it as "<b>bold</b> ok".
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" && strings.Contains(req.Text, "<b>bold</b>")
	})).Return(&telegram.Message{}, nil).Once()
	sink.Delta("**bold** ok")

	// Finalize: ONE more edit with HTML parse mode, content from finalizer.
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" && strings.Contains(req.Text, "<b>bold</b>")
	})).Return(&telegram.Message{}, nil).Once()

	finalizer := func(text string) ([]telegram.SendMessageRequest, error) {
		return []telegram.SendMessageRequest{{Text: "<b>bold</b> ok", ParseMode: "HTML"}}, nil
	}
	extra, edits := sink.Finalize(finalizeArgs{FullText: "**bold** ok"}, finalizer)
	assert.Empty(t, extra)
	assert.GreaterOrEqual(t, edits, 2)
	api.AssertExpectations(t)
}

// TestStreamSink_DeltaInlineFormattingMidStream verifies that an unclosed
// `**` mid-stream produces a balanced HTML render, so the user sees the
// bold partially applied instead of stray asterisks.
func TestStreamSink_DeltaInlineFormattingMidStream(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)

	// Delta arrives with `**bo` — balancer appends `**` so goldmark renders
	// `<b>bo</b>`. The user sees correctly-formatted bold mid-stream.
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" && strings.Contains(req.Text, "<b>bo</b>")
	})).Return(&telegram.Message{}, nil).Once()

	sink.Delta("hi **bo")
	api.AssertExpectations(t)
}

func TestStreamSink_FinalizeEmptyUsesEmptyResponseLocale(t *testing.T) {
	sink, api, _, translator := newSinkWithMockAPI(t)
	expected := translator.Get("en", "bot.empty_response")
	require.NotEmpty(t, expected)

	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" && strings.Contains(req.Text, "Seems")
	})).Return(&telegram.Message{}, nil).Once()

	extra, _ := sink.Finalize(finalizeArgs{FullText: ""}, func(string) ([]telegram.SendMessageRequest, error) {
		t.Fatal("finalizeResponse should not be called for empty FullText")
		return nil, nil
	})
	assert.Empty(t, extra)
	api.AssertExpectations(t)
}

func TestStreamSink_FinalizeErrorUsesErrorText(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)

	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" && strings.Contains(req.Text, "boom")
	})).Return(&telegram.Message{}, nil).Once()

	extra, _ := sink.Finalize(
		finalizeArgs{HadError: true, ErrorText: "boom"},
		func(string) ([]telegram.SendMessageRequest, error) {
			t.Fatal("finalizeResponse should not be called for error path")
			return nil, nil
		},
	)
	assert.Empty(t, extra)
	api.AssertExpectations(t)
}

func TestStreamSink_FinalizeOverflowReturnsExtraChunks(t *testing.T) {
	sink, api, _, _ := newSinkWithMockAPI(t)

	// Push to overflow (only the first immediate HTML edit fires; the
	// huge follow-up flips mode to overflow before any edit).
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.ParseMode == "HTML" && strings.Contains(req.Text, "seed")
	})).Return(&telegram.Message{}, nil).Once()
	sink.Delta("seed")
	sink.Delta(strings.Repeat("z", 3500))
	require.Equal(t, streamModeOverflow, sink.mode)

	// Finalize: chunks[0] edits the placeholder; chunks[1:] returned to caller.
	api.On("EditMessageText", mock.Anything, mock.MatchedBy(func(req telegram.EditMessageTextRequest) bool {
		return req.Text == "first chunk"
	})).Return(&telegram.Message{}, nil).Once()

	finalizer := func(text string) ([]telegram.SendMessageRequest, error) {
		return []telegram.SendMessageRequest{
			{Text: "first chunk", ParseMode: "HTML"},
			{Text: "second chunk", ParseMode: "HTML"},
			{Text: "third chunk", ParseMode: "HTML"},
		}, nil
	}
	extra, _ := sink.Finalize(finalizeArgs{FullText: "long text"}, finalizer)
	assert.Len(t, extra, 2, "extra chunks returned for caller to send as new messages")
	assert.Equal(t, "second chunk", extra[0].Text)
	api.AssertExpectations(t)
}

func TestStreamSink_PlaceholderSendFailureDegradesGracefully(t *testing.T) {
	api := new(testutil.MockBotAPI)
	api.On("SendMessage", mock.Anything, mock.Anything).Return(nil, assert.AnError).Once()

	translator, terr := i18n.NewTranslator("en")
	require.NoError(t, terr)
	sink := newStreamSink(
		context.Background(), api, translator, "en",
		defaultStreamingCfg(), 1, 0, 7, testutil.TestLogger(),
	)
	require.NotNil(t, sink)
	assert.Equal(t, 0, sink.msgID, "msgID stays zero when placeholder send fails")

	// Status / Delta become no-ops; no further API calls expected.
	sink.Status("search_history", "")
	sink.Delta("hello")
	api.AssertExpectations(t)

	// Finalize returns nil chunks (caller falls back to its own send path).
	extra, edits := sink.Finalize(finalizeArgs{FullText: "x"}, func(string) ([]telegram.SendMessageRequest, error) {
		t.Fatal("finalizeResponse should not be called when placeholder failed")
		return nil, nil
	})
	assert.Nil(t, extra)
	assert.Equal(t, 0, edits)
}

func TestStreamSink_StatusKeyMapping(t *testing.T) {
	tests := []struct {
		toolName string
		key      string
	}{
		{"search_history", "bot.streaming.tool_search_history"},
		{"internet_search", "bot.streaming.tool_internet_search"},
		{"manage_memory", "bot.streaming.tool_manage_memory"},
		{"read_url", "bot.streaming.tool_read_url"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.key, streamingToolStatusKey(tt.toolName))
	}
}

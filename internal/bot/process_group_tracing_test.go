package bot

import (
	"context"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/agent/laplace"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/openrouter"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkcodes "go.opentelemetry.io/otel/codes"
)

// TestProcessMessageGroup_RecordsRootSpan verifies the shape of the root
// span produced by Bot.processMessageGroup. Kept as the minimal template
// for future span-structure tests added in Stage 1.2 (RAG, reranker, LLM,
// tools): copy this pattern, swap the exercised function, assert on the
// new span's Name/Attributes/Events.
func TestProcessMessageGroup_RecordsRootSpan(t *testing.T) {
	getSpans := testutil.WithTracingCapture(t)

	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockAPI := new(testutil.MockBotAPI)
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false

	mockDownloader := new(testutil.MockFileDownloader)
	laplaceAgent := laplace.New(cfg, mockORClient, nil, mockStore, mockStore, nil, translator, logger)

	b := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		downloader:      mockDownloader,
		fileProcessor:   files.NewProcessor(mockDownloader, translator, "en", testutil.TestLogger()),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
		laplaceAgent:    laplaceAgent,
	}
	b.messageGrouper = NewMessageGrouper(b, logger, 0, b.processMessageGroup)

	const userID = int64(555)
	const text = "Hello tracing world"
	chatID := int64(777)
	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 42,
			Text:      text,
			From:      &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
			Chat:      &telegram.Chat{ID: chatID},
			Date:      now,
		},
	}

	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil)

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: messages[0].BuildContent(translator, "en")},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.Anything).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(openrouter.ChatCompletionResponse{
		Choices: []openrouter.ResponseChoice{
			{Message: openrouter.ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{TotalTokens: 5},
	}, nil)

	b.processMessageGroup(context.Background(), &MessageGroup{
		Messages: messages,
		UserID:   userID,
	})

	spans := getSpans()
	// processMessageGroup now starts a child laplace.Execute span too —
	// pick out the root by name rather than asserting span count.
	var rootIdx = -1
	for i := range spans {
		if spans[i].Name == "bot.processMessageGroup" {
			rootIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, rootIdx, 0, "bot.processMessageGroup span must be present")
	span := spans[rootIdx]

	attrs := make(map[attribute.Key]attribute.Value, len(span.Attributes))
	for _, kv := range span.Attributes {
		attrs[kv.Key] = kv.Value
	}

	userIDAttr, ok := attrs["user.id"]
	require.True(t, ok, "user.id attribute missing")
	assert.Equal(t, userID, userIDAttr.AsInt64())

	countAttr, ok := attrs["message.count"]
	require.True(t, ok, "message.count attribute missing")
	assert.Equal(t, int64(1), countAttr.AsInt64())

	charsAttr, ok := attrs["message.total_chars"]
	require.True(t, ok, "message.total_chars attribute missing")
	assert.Equal(t, int64(len(text)), charsAttr.AsInt64())

	// Successful run leaves status Unset — Error status is reserved for
	// the !success path.
	assert.Equal(t, sdkcodes.Unset, span.Status.Code)
}

// runAnomalyTraceCase wires the same minimal happy-path stack as
// TestProcessMessageGroup_RecordsRootSpan and runs one message group with
// the given user text + LLM completion. Returns attribute map of the root
// span. Used by the bot.anomaly.* tests.
func runAnomalyTraceCase(t *testing.T, userText, llmContent string) map[attribute.Key]attribute.Value {
	t.Helper()
	getSpans := testutil.WithTracingCapture(t)

	translator := testutil.TestTranslator(t)
	logger := testutil.TestLogger()
	mockAPI := new(testutil.MockBotAPI)
	mockStore := new(testutil.MockStorage)
	mockORClient := new(testutil.MockOpenRouterClient)
	cfg := testutil.TestConfig()
	cfg.RAG.Enabled = false

	mockDownloader := new(testutil.MockFileDownloader)
	laplaceAgent := laplace.New(cfg, mockORClient, nil, mockStore, mockStore, nil, translator, logger)

	b := &Bot{
		api:             mockAPI,
		userRepo:        mockStore,
		msgRepo:         mockStore,
		statsRepo:       mockStore,
		factRepo:        mockStore,
		factHistoryRepo: mockStore,
		orClient:        mockORClient,
		downloader:      mockDownloader,
		fileProcessor:   files.NewProcessor(mockDownloader, translator, "en", testutil.TestLogger()),
		cfg:             cfg,
		logger:          logger,
		translator:      translator,
		laplaceAgent:    laplaceAgent,
	}
	b.messageGrouper = NewMessageGrouper(b, logger, 0, b.processMessageGroup)

	const userID = int64(555)
	chatID := int64(777)
	now := int(time.Now().Unix())

	messages := []*telegram.Message{
		{
			MessageID: 42,
			Text:      userText,
			From:      &telegram.User{ID: userID, FirstName: "User", Username: "testuser"},
			Chat:      &telegram.Chat{ID: chatID},
			Date:      now,
		},
	}

	mockAPI.On("SendChatAction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).Return(nil)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(&telegram.Message{}, nil)

	mockStore.On("GetUnprocessedMessages", userID).Return([]storage.Message{
		{Role: "user", Content: messages[0].BuildContent(translator, "en")},
	}, nil)
	mockStore.On("GetFacts", userID).Return([]storage.Fact{}, nil)
	mockStore.On("AddMessageToHistory", userID, mock.Anything).Return(nil)
	mockStore.On("AddStat", mock.Anything).Return(nil)

	mockORClient.On("CreateChatCompletion", mock.Anything, mock.Anything).Return(openrouter.ChatCompletionResponse{
		Choices: []openrouter.ResponseChoice{
			{Message: openrouter.ResponseMessage{Role: "assistant", Content: llmContent}, FinishReason: "stop"},
		},
		Usage: struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost,omitempty"`
		}{TotalTokens: 5},
	}, nil)

	b.processMessageGroup(context.Background(), &MessageGroup{
		Messages: messages,
		UserID:   userID,
	})

	spans := getSpans()
	for i := range spans {
		if spans[i].Name == "bot.processMessageGroup" {
			attrs := make(map[attribute.Key]attribute.Value, len(spans[i].Attributes))
			for _, kv := range spans[i].Attributes {
				attrs[kv.Key] = kv.Value
			}
			return attrs
		}
	}
	t.Fatal("bot.processMessageGroup span not captured")
	return nil
}

// TestProcessMessageGroup_RecordsEmptyResponseAnomaly: the LLM returns an
// empty completion. laplace.Execute substitutes a localized fallback, but
// we record the underlying anomaly on the root span so it's countable in
// TraceQL via {span.bot.anomaly.empty_response=true}.
func TestProcessMessageGroup_RecordsEmptyResponseAnomaly(t *testing.T) {
	attrs := runAnomalyTraceCase(t, "Hello tracing world", "")

	v, ok := attrs["bot.anomaly.empty_response"]
	require.True(t, ok, "bot.anomaly.empty_response attribute missing")
	assert.True(t, v.AsBool())
}

// TestProcessMessageGroup_RecordsSanitizedAnomaly: the LLM emits a known
// hallucination tag (</tool_code>) which SanitizeLLMResponse strips. The
// root span carries the anomaly bool plus a sanitized_from event with the
// original (dirty) content for triage. The event itself is content-gated;
// this assertion only checks the bool.
func TestProcessMessageGroup_RecordsSanitizedAnomaly(t *testing.T) {
	dirty := "Here is the answer.</tool_code> trailing junk"
	attrs := runAnomalyTraceCase(t, "Hello tracing world", dirty)

	v, ok := attrs["bot.anomaly.sanitized"]
	require.True(t, ok, "bot.anomaly.sanitized attribute missing")
	assert.True(t, v.AsBool())
}

// TestProcessMessageGroup_RecordsEchoAnomaly: the LLM returns a completion
// byte-identical to the user's last message — the Gemini 3.x degenerate
// echo failure mode. Asserts the anomaly bool and that
// echo_output_tokens is wired (zero in this mock — the laplace tracker
// isn't fed token counts here, the assertion only checks the attribute
// is present).
func TestProcessMessageGroup_RecordsEchoAnomaly(t *testing.T) {
	echoText := "Как будто не особо охота парится с этим самым npu, что думаешь?"
	attrs := runAnomalyTraceCase(t, echoText, echoText)

	v, ok := attrs["bot.anomaly.echo_user_message"]
	require.True(t, ok, "bot.anomaly.echo_user_message attribute missing")
	assert.True(t, v.AsBool())

	_, ok = attrs["bot.anomaly.echo_output_tokens"]
	require.True(t, ok, "bot.anomaly.echo_output_tokens attribute missing")
}

// TestProcessMessageGroup_DoesNotFlagShortReplyAsEcho: a 5-char user text
// and matching 5-char reply is below the minLen=30 floor — no echo
// attribute should be set.
func TestProcessMessageGroup_DoesNotFlagShortReplyAsEcho(t *testing.T) {
	attrs := runAnomalyTraceCase(t, "hi", "hi")

	_, ok := attrs["bot.anomaly.echo_user_message"]
	assert.False(t, ok, "short reply must not be flagged as echo")
}

// TestDetectEchoOfLastUserMessage covers the core matcher: trim + voice
// quote stripping + length floor.
func TestDetectEchoOfLastUserMessage(t *testing.T) {
	longMatch := "Как будто не особо охота парится с этим самым npu, что думаешь?"
	cases := []struct {
		name     string
		reply    string
		userText string
		want     bool
	}{
		{"exact match above min length", longMatch, longMatch, true},
		{"exact match with voice quote prefix", "> 🎤 voice transcript here\n\n" + longMatch, longMatch, true},
		{"exact match with surrounding whitespace", "  " + longMatch + "  \n", longMatch, true},
		{"reply too short — no echo", "ok", "ok", false},
		{"user text too short — no echo", longMatch, "ok", false},
		{"different content — no echo", longMatch, "Полностью другой длинный текст с теми же словами", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectEchoOfLastUserMessage(tc.reply, tc.userText)
			assert.Equal(t, tc.want, got)
		})
	}
}

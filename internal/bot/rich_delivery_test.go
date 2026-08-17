package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"
)

type recordingRichTransport struct {
	responses []OutgoingResponse
	errors    map[int]error
	ids       map[int]string
}

func (t *recordingRichTransport) SendText(_ context.Context, response OutgoingResponse) (string, error) {
	call := len(t.responses)
	t.responses = append(t.responses, response)
	if err := t.errors[call]; err != nil {
		return "", err
	}
	if id, ok := t.ids[call]; ok {
		return id, nil
	}
	return fmt.Sprintf("message-%d", call+1), nil
}

func (t *recordingRichTransport) SendTextPersistent(ctx context.Context, response OutgoingResponse) (string, error) {
	return t.SendText(ctx, response)
}

func (*recordingRichTransport) SendMedia(context.Context, OutgoingMedia) (string, error) {
	return "", nil
}
func (*recordingRichTransport) SendTyping(context.Context, string) error { return nil }
func (*recordingRichTransport) SetReaction(context.Context, string, string, string) error {
	return nil
}
func (*recordingRichTransport) Kind() string { return transportTelegram }
func (*recordingRichTransport) Capabilities() Capabilities {
	return Capabilities{SupportsRichMessages: true, SupportsStreaming: true}
}
func (*recordingRichTransport) IsAllowed(string) bool     { return true }
func (*recordingRichTransport) AllowlistConfigured() bool { return true }

func newRichDeliveryTestBot(t *testing.T, transport Transport) *Bot {
	t.Helper()
	cfg := testutil.TestConfig()
	cfg.Transport = transportTelegram
	cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
	cfg.Telegram.RichMessages.AllowedUserIDs = []int64{123}
	cfg.Bot.Streaming.Enabled = false
	translator, err := i18n.NewTranslator("en")
	require.NoError(t, err)
	logger := testutil.TestLogger()
	return &Bot{
		cfg:        cfg,
		transport:  transport,
		renderer:   NewTelegramRenderer(logger),
		logger:     logger,
		translator: translator,
	}
}

func TestSendRichRendered_SendsSafeNativeFinal(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.sendRichRendered(
		context.Background(), "123", "9", "42",
		"# Formula\n\nInline $x^2$ and **bold**.", bot.logger,
	)

	require.NoError(t, result.err)
	assert.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Equal(t, richMetricPathNative, result.metricPath)
	assert.Equal(t, richMetricFallbackNone, result.fallbackReason)
	require.Equal(t, 1, result.sent)
	assert.Equal(t, 1, result.attempts)
	assert.Equal(t, "message-1", result.firstMsgID)
	assert.Equal(t, []string{"message-1"}, result.confirmedIDs)
	require.Len(t, transport.responses, 1)
	response := transport.responses[0]
	assert.Equal(t, ResponseFormatRichHTML, response.Format)
	assert.Equal(t, "9", response.ThreadRoot)
	assert.Equal(t, "42", response.ReplyTo)
	assert.Contains(t, response.Text, "<h1>Formula</h1>")
	assert.Contains(t, response.Text, "<tg-math>x^2</tg-math>")
	assert.Contains(t, response.Text, "<strong>bold</strong>")
}

func TestSendRichRendered_PreflightFailureIsAllLegacy(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	tooLong := strings.Repeat("ж", richMessageSafeCharacterLimit+1)

	result := bot.sendRichRendered(context.Background(), "123", "", "42", tooLong, bot.logger)

	require.NoError(t, result.err)
	assert.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Equal(t, richMetricPathLocalFallback, result.metricPath)
	assert.Equal(t, richMetricFallbackRenderOrLimit, result.fallbackReason)
	assert.Greater(t, result.sent, 1)
	assert.Equal(t, "message-1", result.firstMsgID)
	require.Len(t, transport.responses, result.sent)
	for i, response := range transport.responses {
		assert.Equal(t, ResponseFormatDefault, response.Format)
		if i == 0 {
			assert.Equal(t, "42", response.ReplyTo)
		} else {
			assert.Empty(t, response.ReplyTo)
		}
	}
}

func TestSendRichRendered_ConfirmedRejectionLatchesLegacyFallback(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{
		0: fmt.Errorf("%w: invalid rich HTML", ErrRichMessageRejected),
	}}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.sendRichRendered(
		context.Background(), "123", "9", "42",
		"# First\n\n###SPLIT###\n\n## Second", bot.logger,
	)

	require.NoError(t, result.err)
	assert.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Equal(t, richMetricPathAPIFallback, result.metricPath)
	assert.Equal(t, richMetricFallbackFormatRejected, result.fallbackReason)
	assert.Equal(t, 2, result.sent)
	assert.Equal(t, 3, result.attempts)
	assert.Equal(t, "message-2", result.firstMsgID)
	assert.Equal(t, []string{"message-2", "message-3"}, result.confirmedIDs)
	require.Len(t, transport.responses, 3)
	assert.Equal(t, ResponseFormatRichHTML, transport.responses[0].Format)
	assert.Equal(t, ResponseFormatDefault, transport.responses[1].Format)
	assert.Equal(t, ResponseFormatDefault, transport.responses[2].Format)
	assert.Equal(t, "42", transport.responses[0].ReplyTo)
	assert.Equal(t, "42", transport.responses[1].ReplyTo)
	assert.Empty(t, transport.responses[2].ReplyTo)
	assert.Contains(t, transport.responses[1].Text, "<b>First</b>")
	assert.Contains(t, transport.responses[2].Text, "<b>Second</b>")
}

func TestSendRichRendered_LaterPartRejectionFallsBackOnlyThatPart(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{
		1: fmt.Errorf("%w: invalid rich HTML", ErrRichMessageRejected),
	}}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.sendRichRendered(
		context.Background(), "123", "9", "42",
		"# First\n\n###SPLIT###\n\n## Second", bot.logger,
	)

	require.NoError(t, result.err)
	assert.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Equal(t, 2, result.sent)
	assert.Equal(t, 3, result.attempts)
	assert.Equal(t, []string{"message-1", "message-3"}, result.confirmedIDs)
	require.NotNil(t, result.failedPart)
	assert.Equal(t, 1, *result.failedPart)
	assert.Nil(t, result.failedChunk)
	require.Len(t, transport.responses, 3)
	assert.Equal(t, ResponseFormatRichHTML, transport.responses[0].Format)
	assert.Equal(t, ResponseFormatRichHTML, transport.responses[1].Format)
	assert.Equal(t, ResponseFormatDefault, transport.responses[2].Format)
	assert.Equal(t, "42", transport.responses[0].ReplyTo)
	assert.Empty(t, transport.responses[2].ReplyTo)
}

func TestSendRichRendered_AmbiguousFailureDoesNotResend(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{
		0: errors.New("connection reset after request write"),
	}}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.sendRichRendered(
		context.Background(), "123", "", "42", "# Unique answer marker", bot.logger,
	)

	require.Error(t, result.err)
	assert.Equal(t, richDeliveryUnknown, result.outcome)
	assert.Equal(t, richMetricPathNative, result.metricPath)
	assert.Equal(t, richMetricFallbackNone, result.fallbackReason)
	assert.Zero(t, result.sent)
	assert.Equal(t, 1, result.attempts)
	assert.Empty(t, result.firstMsgID)
	require.Len(t, transport.responses, 1)
	assert.Equal(t, ResponseFormatRichHTML, transport.responses[0].Format)
}

func TestSendRichRendered_ServerErrorIsUnknownAndDoesNotResend(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{
		0: &telegram.APIError{Code: 500, Description: "Internal Server Error"},
	}}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.sendRichRendered(
		context.Background(), "123", "", "42", "# Unique answer marker", bot.logger,
	)

	require.Error(t, result.err)
	assert.Equal(t, richDeliveryUnknown, result.outcome)
	assert.Zero(t, result.sent)
	assert.Equal(t, 1, result.attempts)
	assert.Empty(t, result.confirmedIDs)
	require.NotNil(t, result.failedPart)
	assert.Equal(t, 0, *result.failedPart)
	require.Len(t, transport.responses, 1)
	assert.Equal(t, ResponseFormatRichHTML, transport.responses[0].Format)
}

func TestSendRichRendered_LocalFallbackPreservesRichSafetyPolicy(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	source := "[@victim](tg://user?id=123) [js](javascript:alert) " +
		"![cat photo](https://evil.example/cat.jpg) bare @alice\n\n" +
		strings.Repeat("safe filler ", 3000)

	result := bot.sendRichRendered(context.Background(), "123", "", "42", source, bot.logger)

	require.NoError(t, result.err)
	assert.Equal(t, richDeliveryConfirmed, result.outcome)
	assert.Greater(t, result.sent, 1)
	var delivered strings.Builder
	for _, response := range transport.responses {
		assert.Equal(t, ResponseFormatDefault, response.Format)
		delivered.WriteString(response.Text)
	}
	got := delivered.String()
	assert.Contains(t, got, "@\u2060victim")
	assert.Contains(t, got, "bare @\u2060alice")
	assert.Contains(t, got, "cat photo")
	assert.NotContains(t, strings.ToLower(got), "tg://")
	assert.NotContains(t, strings.ToLower(got), "javascript:")
	assert.NotContains(t, strings.ToLower(got), "<img")
}

func TestSendRichRendered_APIFallbackPreservesRichSafetyPolicy(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{
		0: fmt.Errorf("%w: invalid rich HTML", ErrRichMessageRejected),
	}}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.sendRichRendered(
		context.Background(), "123", "", "42",
		"[@victim](tg://user?id=123) ![cat](https://evil.example/cat.jpg) bare @alice",
		bot.logger,
	)

	require.NoError(t, result.err)
	assert.Equal(t, richDeliveryConfirmed, result.outcome)
	require.Len(t, transport.responses, 2)
	fallback := transport.responses[1]
	assert.Equal(t, ResponseFormatDefault, fallback.Format)
	assert.Contains(t, fallback.Text, "@\u2060victim")
	assert.Contains(t, fallback.Text, "cat")
	assert.Contains(t, fallback.Text, "bare @\u2060alice")
	assert.NotContains(t, strings.ToLower(fallback.Text), "tg://")
}

func TestSendRichRendered_PartialFallbackFailureIsNotHidden(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{
		0: fmt.Errorf("%w: invalid rich HTML", ErrRichMessageRejected),
		2: errors.New("connection reset after request write"),
	}}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.sendRichRendered(
		context.Background(), "123", "", "42",
		strings.Repeat("fallback chunk content ", 300),
		bot.logger,
	)

	require.Error(t, result.err)
	assert.Equal(t, richDeliveryPartialUnknown, result.outcome)
	assert.Equal(t, 1, result.sent)
	assert.Equal(t, 3, result.attempts)
	assert.Equal(t, "message-2", result.firstMsgID)
	require.Len(t, transport.responses, 3, "no generic-error send may hide the fallback failure")
}

func TestSendRichRendered_ConfirmedNonFormatRejectionDoesNotFallback(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{
		0: &telegram.APIError{Code: 400, Description: "Bad Request: chat not found"},
	}}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.sendRichRendered(context.Background(), "123", "", "42", "# Unique answer", bot.logger)

	require.Error(t, result.err)
	assert.Equal(t, richDeliveryRejected, result.outcome)
	assert.Zero(t, result.sent)
	assert.Equal(t, 1, result.attempts)
	require.Len(t, transport.responses, 1)
	assert.Equal(t, ResponseFormatRichHTML, transport.responses[0].Format)
}

func TestSendRichRendered_MissingMessageIDIsUnknownAndNotResent(t *testing.T) {
	transport := &recordingRichTransport{ids: map[int]string{0: ""}}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.sendRichRendered(context.Background(), "123", "", "42", "# Unique answer", bot.logger)

	require.Error(t, result.err)
	assert.Equal(t, richDeliveryUnknown, result.outcome)
	assert.Zero(t, result.sent)
	assert.Equal(t, 1, result.attempts)
	require.Len(t, transport.responses, 1)
}

func TestSendRichRendered_HardPreflightBoundsSendNothing(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "source bytes", source: strings.Repeat("x", richMessageMaxSourceBytes+1)},
		{name: "explicit parts", source: strings.Repeat("part\n\n###SPLIT###\n\n", richMessageMaxParts) + "last"},
		{name: "fallback chunk expansion", source: strings.Repeat(`"`, 70_000)},
		{name: "invalid UTF-8", source: string([]byte{'o', 'k', 0xff})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &recordingRichTransport{}
			bot := newRichDeliveryTestBot(t, transport)

			result := bot.sendRichRendered(context.Background(), "123", "", "42", tt.source, bot.logger)

			require.Error(t, result.err)
			assert.Equal(t, richDeliveryRejected, result.outcome)
			assert.Equal(t, richMetricPathPreflightRejected, result.metricPath)
			assert.Equal(t, richMetricFallbackHardPreflight, result.fallbackReason)
			assert.Zero(t, result.attempts)
			assert.Empty(t, transport.responses)
		})
	}
}

func TestSendRichRendered_BoundsRenderedHTMLBytes(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.sendRichRendered(context.Background(), "123", "", "42", strings.Repeat(`"`, 25_000), bot.logger)

	require.NoError(t, result.err)
	assert.Equal(t, richDeliveryConfirmed, result.outcome)
	require.NotEmpty(t, transport.responses)
	for _, response := range transport.responses {
		assert.Equal(t, ResponseFormatDefault, response.Format)
	}
}

func TestRenderRichParts_RejectsInvalidUTF8BeforeSending(t *testing.T) {
	parts, err := renderRichParts(string([]byte{'o', 'k', 0xff}))
	require.Error(t, err)
	assert.Nil(t, parts)
}

func TestRenderRichParts_V2StandaloneSplitOnly(t *testing.T) {
	inline := "before ###SPLIT### after"
	parts, err := renderRichParts(inline)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Contains(t, parts[0].html, "###SPLIT###")

	inCode := "```text\n###SPLIT###\n```"
	parts, err = renderRichParts(inCode)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Contains(t, parts[0].html, "###SPLIT###")

	hard := "# First\n\n###SPLIT###\n\n## Second"
	parts, err = renderRichParts(hard)
	require.NoError(t, err)
	require.Len(t, parts, 2)
	assert.Equal(t, "<h1>First</h1>", parts[0].html)
	assert.Equal(t, "<h2>Second</h2>", parts[1].html)

	withoutBlankLines := "First\n###SPLIT###\nSecond"
	parts, err = renderRichParts(withoutBlankLines)
	require.NoError(t, err)
	require.Len(t, parts, 2)
	assert.Equal(t, "First", strings.TrimSpace(parts[0].source))
	assert.Equal(t, "Second", strings.TrimSpace(parts[1].source))
	assert.Equal(t, "<p>First</p>", parts[0].html)
	assert.Equal(t, "<p>Second</p>", parts[1].html)

	crlfUnicode := "Привет 👋\r\n###SPLIT###\r\nмир"
	parts, err = renderRichParts(crlfUnicode)
	require.NoError(t, err)
	require.Len(t, parts, 2)
	assert.Equal(t, "Привет 👋", strings.TrimSpace(parts[0].source))
	assert.Equal(t, "мир", strings.TrimSpace(parts[1].source))
	assert.Equal(t, "<p>Привет 👋</p>", parts[0].html)
	assert.Equal(t, "<p>мир</p>", parts[1].html)

	multilineSpoiler := "before ||alpha\n###SPLIT###\nomega|| after"
	parts, err = renderRichParts(multilineSpoiler)
	require.NoError(t, err)
	require.Len(t, parts, 2, "the custom spoiler parser deliberately cannot cross a physical line")
	assert.NotContains(t, parts[0].html+parts[1].html, "<tg-spoiler>")

	markerAfterLink := "[label](https://example.com)\n###SPLIT###\nafter"
	parts, err = renderRichParts(markerAfterLink)
	require.NoError(t, err)
	require.Len(t, parts, 2, "a link envelope must not absorb the next standalone marker line")
	assert.Equal(t, `<p><a href="https://example.com">label</a></p>`, parts[0].html)
	assert.Equal(t, "<p>after</p>", parts[1].html)
}

func TestRenderRichParts_V2IgnoresEmptySegmentsAroundStandaloneSplits(t *testing.T) {
	source := "###SPLIT###\nfirst\n###SPLIT###\n###SPLIT###\nsecond\n###SPLIT###"

	parts, err := renderRichParts(source)
	require.NoError(t, err)
	require.Len(t, parts, 2)
	assert.Equal(t, "first", strings.TrimSpace(parts[0].source))
	assert.Equal(t, "second", strings.TrimSpace(parts[1].source))
	assert.Equal(t, "<p>first</p>", parts[0].html)
	assert.Equal(t, "<p>second</p>", parts[1].html)
}

func TestRenderRichParts_V2OnlyStandaloneSplitIsHardRejection(t *testing.T) {
	parts, err := renderRichParts(" \n###SPLIT###\n ")
	require.Error(t, err)
	assert.ErrorIs(t, err, errRichSplitEmpty)
	assert.Nil(t, parts)

	preflight, err := preflightRichDelivery(
		context.Background(), " \n###SPLIT###\n ", NewTelegramRenderer(testutil.TestLogger()),
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, errRichSplitEmpty)
	assert.Empty(t, preflight.parts)
}

func TestRenderRichParts_V2PartFanoutBoundary(t *testing.T) {
	makeSource := func(parts int) string {
		values := make([]string, parts)
		for i := range parts {
			values[i] = fmt.Sprintf("part-%d", i)
		}
		return strings.Join(values, "\n###SPLIT###\n")
	}

	parts, err := renderRichParts(makeSource(richMessageMaxParts))
	require.NoError(t, err)
	assert.Len(t, parts, richMessageMaxParts)

	parts, err = renderRichParts(makeSource(richMessageMaxParts + 1))
	require.Error(t, err)
	assert.ErrorIs(t, err, errRichPartFanout)
	assert.Nil(t, parts)
}

func TestPreflightRichDelivery_V2FallbackDoesNotReinterpretInlineOrProtectedSplit(t *testing.T) {
	tests := []struct {
		name         string
		source       string
		wantFallback string
	}{
		{name: "inline prose", source: "before ###SPLIT### after"},
		{name: "fenced code", source: "```text\n###SPLIT###\n```"},
		{name: "multiline inline code", source: "before `alpha\n###SPLIT###\nomega` after"},
		{name: "multiline emphasis", source: "before *alpha\n###SPLIT###\nomega* after"},
		{name: "multiline link", source: "before [alpha\n###SPLIT###\nomega](https://example.com) after"},
		{name: "multiline image label", source: "before ![alpha\n###SPLIT###\nomega](https://example.com/image.png) after"},
		{name: "multiline link label after bracket code", source: "before [alpha `]`\n###SPLIT###\nomega](https://example.com) after"},
		{name: "multiline image label after bracket code", source: "before ![alpha `]`\n###SPLIT###\nomega](https://example.com/image.png) after"},
		{name: "multiline link label after bracket raw HTML", source: "before [alpha <!-- ] -->\n###SPLIT###\nomega](https://example.com) after"},
		{name: "multiline image label after bracket raw HTML", source: "before ![alpha <!-- ] -->\n###SPLIT###\nomega](https://example.com/image.png) after"},
		{name: "multiline strikethrough", source: "before ~~alpha\n###SPLIT###\nomega~~ after"},
		{
			name:         "multiline link title",
			source:       "before [label](https://example.com \"alpha\n###SPLIT###\nomega\") after",
			wantFallback: `before <a href="https://example.com">label</a> after`,
		},
		{
			name:         "multiline image title",
			source:       "before ![label](https://example.com/image.png \"alpha\n###SPLIT###\nomega\") after",
			wantFallback: "before label after",
		},
		{
			name: "multiline full link reference",
			source: "before [label][alpha\n###SPLIT###\nomega] after\n\n" +
				"[alpha ###SPLIT### omega]: https://example.com",
			wantFallback: `before <a href="https://example.com">label</a> after`,
		},
		{
			name: "multiline full image reference",
			source: "before ![label][alpha\n###SPLIT###\nomega] after\n\n" +
				"[alpha ###SPLIT### omega]: https://example.com/image.png",
			wantFallback: "before label after",
		},
		{
			name:         "raw HTML nested in emphasis",
			source:       "before *alpha <!-- x\n###SPLIT###\ny -->* after",
			wantFallback: "before <i>alpha </i> after",
		},
		{
			name:         "multiline inline raw HTML",
			source:       "before <!-- alpha\n###SPLIT###\nomega --> after",
			wantFallback: "before  after",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preflight, err := preflightRichDelivery(
				context.Background(), tt.source, NewTelegramRenderer(testutil.TestLogger()),
			)
			require.NoError(t, err)
			assert.False(t, preflight.localFallback)
			require.Len(t, preflight.parts, 1)
			require.Len(t, preflight.parts[0].legacyFallback, 1)
			if tt.wantFallback != "" {
				assert.Equal(t, tt.wantFallback, preflight.parts[0].legacyFallback[0])
			} else {
				assert.Contains(t, preflight.parts[0].legacyFallback[0], richSplitDelimiter)
			}
		})
	}
}

func TestRenderRichParts_V2SplitMarkerInsideAtomicBlocksStaysContent(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		wantHTML string
	}{
		{name: "table", source: "| Value |\n|---|\n| ###SPLIT### |"},
		{name: "display math", source: "$$\n###SPLIT###\n$$"},
		{name: "blockquote", source: "> ###SPLIT###"},
		{name: "list", source: "- ###SPLIT###"},
		{name: "multiline inline code", source: "before `alpha\n###SPLIT###\nomega` after"},
		{name: "multiline inline code CRLF", source: "before `alpha\r\n###SPLIT###\r\nomega` after"},
		{name: "multiline double-backtick code", source: "before ``alpha\n###SPLIT###\nomega`` after"},
		{name: "multiline emphasis", source: "before *alpha\n###SPLIT###\nomega* after"},
		{name: "multiline link", source: "before [alpha\n###SPLIT###\nomega](https://example.com) after"},
		{name: "multiline image label", source: "before ![alpha\n###SPLIT###\nomega](https://example.com/image.png) after"},
		{name: "multiline link label after bracket code", source: "before [alpha `]`\n###SPLIT###\nomega](https://example.com) after"},
		{name: "multiline image label after bracket code", source: "before ![alpha `]`\n###SPLIT###\nomega](https://example.com/image.png) after"},
		{name: "multiline link label after bracket raw HTML", source: "before [alpha <!-- ] -->\n###SPLIT###\nomega](https://example.com) after"},
		{name: "multiline image label after bracket raw HTML", source: "before ![alpha <!-- ] -->\n###SPLIT###\nomega](https://example.com/image.png) after"},
		{name: "multiline strikethrough", source: "before ~~alpha\n###SPLIT###\nomega~~ after"},
		{
			name:     "multiline link title",
			source:   "before [label](https://example.com \"alpha\n###SPLIT###\nomega\") after",
			wantHTML: `<p>before <a href="https://example.com">label</a> after</p>`,
		},
		{
			name:     "multiline image title",
			source:   "before ![label](https://example.com/image.png \"alpha\n###SPLIT###\nomega\") after",
			wantHTML: "<p>before label after</p>",
		},
		{
			name: "multiline full link reference",
			source: "before [label][alpha\n###SPLIT###\nomega] after\n\n" +
				"[alpha ###SPLIT### omega]: https://example.com",
			wantHTML: `<p>before <a href="https://example.com">label</a> after</p>`,
		},
		{
			name: "multiline full image reference",
			source: "before ![label][alpha\n###SPLIT###\nomega] after\n\n" +
				"[alpha ###SPLIT### omega]: https://example.com/image.png",
			wantHTML: "<p>before label after</p>",
		},
		{
			name:     "raw HTML nested in emphasis",
			source:   "before *alpha <!-- x\n###SPLIT###\ny -->* after",
			wantHTML: "<p>before <em>alpha </em> after</p>",
		},
		{
			name:     "multiline inline raw HTML",
			source:   "before <!-- alpha\n###SPLIT###\nomega --> after",
			wantHTML: "<p>before  after</p>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts, err := renderRichParts(tt.source)
			require.NoError(t, err)
			require.Len(t, parts, 1)
			assert.Equal(t, tt.source, parts[0].source)
			if tt.wantHTML != "" {
				assert.Equal(t, tt.wantHTML, parts[0].html)
			} else {
				assert.Contains(t, parts[0].html, richSplitDelimiter)
			}
		})
	}
}

func TestRenderRichParts_V2PacksOnlyBetweenTopLevelBlocks(t *testing.T) {
	first := strings.Repeat("a", 16_000)
	second := strings.Repeat("b", 16_000)
	source := first + "\n\n" + second

	parts, err := renderRichParts(source)
	require.NoError(t, err)
	require.Len(t, parts, 2)
	assert.Equal(t, source, parts[0].source+parts[1].source)
	assert.Contains(t, parts[0].html, first)
	assert.Contains(t, parts[1].html, second)
}

func TestRenderRichParts_V2OversizedAtomicStructuresAreNeverSplit(t *testing.T) {
	payload := strings.Repeat("x", richMessageSafeCharacterLimit+1)
	tests := []struct {
		name   string
		kind   string
		source string
	}{
		{name: "fenced code", kind: "code", source: "```text\n" + payload + "\n```"},
		{name: "table", kind: "table", source: "| Value |\n|---|\n| " + payload + " |"},
		{name: "display math", kind: "math", source: "$$\n" + payload + "\n$$"},
		{name: "blockquote", kind: "blockquote", source: "> " + payload},
		{name: "list", kind: "list", source: "- " + payload},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts, err := renderRichParts(tt.source)
			require.Error(t, err)
			assert.Nil(t, parts, "an atomic failure must not expose a sendable prefix")
			assert.Contains(t, err.Error(), "atomic rich fragment 0 ("+tt.kind+")")
		})
	}
}

func TestRenderRichParts_V2StructuralLimitsUseExactFragmentStats(t *testing.T) {
	t.Run("table columns", func(t *testing.T) {
		makeTable := func(columns int) string {
			header := make([]string, columns)
			separator := make([]string, columns)
			row := make([]string, columns)
			for i := range columns {
				header[i] = fmt.Sprintf("H%d", i)
				separator[i] = "---"
				row[i] = "x"
			}
			return "| " + strings.Join(header, " | ") + " |\n| " +
				strings.Join(separator, " | ") + " |\n| " + strings.Join(row, " | ") + " |"
		}

		parts, err := renderRichParts(makeTable(richMessageMaxTableColumns))
		require.NoError(t, err)
		require.Len(t, parts, 1)
		assert.Equal(t, richMessageMaxTableColumns, parts[0].stats.MaxTableColumns)

		parts, err = renderRichParts(makeTable(richMessageMaxTableColumns + 1))
		require.Error(t, err)
		assert.Nil(t, parts)
		assert.Contains(t, err.Error(), "atomic rich fragment 0 (table)")
	})

	t.Run("list block count", func(t *testing.T) {
		makeList := func(items int) string {
			var source strings.Builder
			for i := range items {
				fmt.Fprintf(&source, "- item %d\n", i)
			}
			return source.String()
		}

		// One list container plus 449 list items is exactly the safe limit.
		parts, err := renderRichParts(makeList(richMessageSafeBlockLimit - 1))
		require.NoError(t, err)
		require.Len(t, parts, 1)
		assert.Equal(t, richMessageSafeBlockLimit, parts[0].stats.Blocks)

		parts, err = renderRichParts(makeList(richMessageSafeBlockLimit))
		require.Error(t, err)
		assert.Nil(t, parts)
		assert.Contains(t, err.Error(), "atomic rich fragment 0 (list)")
	})

	t.Run("nested list depth", func(t *testing.T) {
		makeNestedList := func(levels int) string {
			var source strings.Builder
			for level := range levels {
				fmt.Fprintf(&source, "%s- level %d\n", strings.Repeat("  ", level), level)
			}
			return source.String()
		}

		// Every tight-list level contributes one <ul> and one <li> tag.
		parts, err := renderRichParts(makeNestedList(richMessageMaxDepth / 2))
		require.NoError(t, err)
		require.Len(t, parts, 1)
		assert.Equal(t, richMessageMaxDepth, parts[0].stats.MaxDepth)

		parts, err = renderRichParts(makeNestedList(richMessageMaxDepth/2 + 1))
		require.Error(t, err)
		assert.Nil(t, parts)
		assert.Contains(t, err.Error(), "atomic rich fragment 0 (list)")
	})
}

func TestRenderRichParts_V2EmptyRawHTMLDoesNotCreateOrEraseAVisiblePart(t *testing.T) {
	source := "before\n\n<script>\nalert('nope')\n</script>\n\nafter"

	parts, err := renderRichParts(source)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, source, parts[0].source)
	assert.Equal(t, "<p>before</p><p>after</p>", parts[0].html)
	assert.Equal(t, 2, parts[0].stats.Blocks)

	parts, err = renderRichParts("<script>\nalert('nope')\n</script>")
	require.Error(t, err)
	assert.Nil(t, parts, "a semantically empty document cannot become a persistent message")
}

func TestPreflightRichDelivery_V2OversizedAtomicBlockUsesPreparedLegacyFallback(t *testing.T) {
	source := strings.Repeat("x", richMessageSafeCharacterLimit+1)
	preflight, err := preflightRichDelivery(context.Background(), source, NewTelegramRenderer(testutil.TestLogger()))

	require.NoError(t, err)
	assert.True(t, preflight.localFallback)
	assert.Equal(t, richMetricFallbackRenderOrLimit, preflight.fallbackReason)
	require.Len(t, preflight.parts, 1)
	assert.NotEmpty(t, preflight.parts[0].legacyFallback)
}

func TestRenderRichParts_ListBoundaryRepairKeepsFallbackSource(t *testing.T) {
	source := "**Numbered list starting at 3:**\n3. Prepare\n4. Launch"

	parts, err := renderRichParts(source)

	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, source, parts[0].source)
	assert.Equal(t,
		"<p><strong>Numbered list starting at 3:</strong></p>"+
			`<ol start="3"><li>Prepare</li><li>Launch</li></ol>`,
		parts[0].html,
	)
}

func TestNewResponsePath_RichOutputWithoutDraftDoesNotOpenLegacyStreaming(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	bot.cfg.Bot.Streaming.Enabled = true

	path := bot.newResponsePath(
		context.Background(), storage.ScopeID("user"), "123", true, "123", "", "42", bot.logger,
	)

	assert.Nil(t, path.sink)
	assert.False(t, path.usesRichDraft())
	assert.Empty(t, transport.responses, "rich output must not open a legacy placeholder when rich drafts are disabled")
}

func TestResponsePath_RichSendRequiresCanary(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	bot.cfg.Telegram.RichMessages.Mode = "send"
	bot.cfg.Telegram.RichMessages.AllowedUserIDs = []int64{123}

	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	nonCanary := bot.newResponsePath(ctx, storage.ScopeID("user"), "999", true, "999", "", "42", bot.logger)
	require.True(t, nonCanary.sendFinal(ctx, span, "# Legacy for non-canary"))
	span.End()
	require.Len(t, transport.responses, 1)
	assert.Equal(t, ResponseFormatDefault, transport.responses[0].Format)

	ctx, span = otel.Tracer("test").Start(context.Background(), "final")
	canary := bot.newResponsePath(ctx, storage.ScopeID("user"), "123", true, "123", "", "43", bot.logger)
	require.True(t, canary.sendFinal(ctx, span, "# Rich for canary"))
	span.End()
	require.Len(t, transport.responses, 2)
	assert.Equal(t, ResponseFormatRichHTML, transport.responses[1].Format)

	ctx, span = otel.Tracer("test").Start(context.Background(), "final")
	unsupportedContext := bot.newResponsePath(ctx, storage.ScopeID("user"), "123", false, "-100123", "", "44", bot.logger)
	require.True(t, unsupportedContext.sendFinal(ctx, span, "# Legacy in group/business context"))
	span.End()
	require.Len(t, transport.responses, 3)
	assert.Equal(t, ResponseFormatDefault, transport.responses[2].Format)
}

func TestResponsePath_ShadowRendersButSendsLegacy(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	bot.cfg.Telegram.RichMessages.Mode = "shadow"
	bot.cfg.Telegram.RichMessages.AllowedUserIDs = []int64{123}

	path := bot.newResponsePath(context.Background(), storage.ScopeID("user"), "123", true, "123", "", "42", bot.logger)
	assert.Equal(t, "shadow", path.effectiveRichMode())

	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	require.True(t, path.sendFinal(ctx, span, "# Shadow\n\n$x^2$"))
	span.End()
	require.Len(t, transport.responses, 1)
	assert.Equal(t, ResponseFormatDefault, transport.responses[0].Format)
}

func TestResponsePath_PlaceholderFailureFallsBackToBufferedFinal(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	bot.cfg.Telegram.RichMessages.Mode = "off"
	bot.cfg.Bot.Streaming.Enabled = true
	mockAPI := new(testutil.MockBotAPI)
	mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(nil, errors.New("placeholder unavailable")).Once()
	bot.api = mockAPI

	path := bot.newResponsePath(context.Background(), storage.ScopeID("user"), "123", true, "123", "", "42", bot.logger)
	assert.Nil(t, path.sink)

	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	require.True(t, path.sendFinal(ctx, span, "Buffered answer"))
	span.End()
	require.Len(t, transport.responses, 1)
	assert.Equal(t, ResponseFormatDefault, transport.responses[0].Format)
	assert.Equal(t, 2, path.tgCalls, "placeholder attempt plus buffered send")
	mockAPI.AssertExpectations(t)
}

func TestResponsePath_RichIsFinalOnly(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	bot.msgRepo = new(testutil.MockStorage)
	path := &responsePath{
		bot: bot, logger: bot.logger, userID: storage.ScopeID("user"),
		convID: "123", threadRoot: "9", replyTo: "42", richMode: "send",
	}

	path.sendIntermediate(context.Background(), "**working**")
	path.sendError(context.Background(), "temporary error")
	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	ok := path.sendFinal(ctx, span, "# Final\n\n$x^2$")
	span.End()

	require.True(t, ok)
	require.Len(t, transport.responses, 3)
	assert.Equal(t, ResponseFormatDefault, transport.responses[0].Format)
	assert.Equal(t, ResponseFormatDefault, transport.responses[1].Format)
	assert.Equal(t, ResponseFormatRichHTML, transport.responses[2].Format)
	assert.Equal(t, "42", transport.responses[2].ReplyTo)
	assert.Equal(t, 3, path.tgCalls)
}

func TestResponsePath_AmbiguousRichFailureIsNotReportedAsDelivered(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{
		0: errors.New("connection reset after request write"),
	}}
	bot := newRichDeliveryTestBot(t, transport)
	path := &responsePath{
		bot: bot, logger: bot.logger, userID: storage.ScopeID("user"),
		convID: "123", replyTo: "42", richMode: "send",
	}

	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	ok := path.sendFinal(ctx, span, "# Unique answer")
	span.End()

	assert.False(t, ok)
	assert.Equal(t, 1, path.tgCalls)
	require.Len(t, transport.responses, 1)
	assert.Equal(t, ResponseFormatRichHTML, transport.responses[0].Format)
}

func TestResponsePath_AmbiguousRichFailureIsNotPersisted(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{
		0: errors.New("connection reset after request write"),
	}}
	bot := newRichDeliveryTestBot(t, transport)
	store := new(testutil.MockStorage)
	bot.msgRepo = store
	path := &responsePath{
		bot: bot, logger: bot.logger, userID: storage.ScopeID("user"),
		convID: "123", replyTo: "42", richMode: "send",
	}

	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	ok := path.sendFinalAndPersist(ctx, span, "# Unique answer", nil)
	span.End()

	assert.False(t, ok)
	store.AssertNotCalled(t, "AddMessageToHistory", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "SetReplyTransportID", mock.Anything, mock.Anything)
}

func TestSendRenderedDelivery_PartialUnknownTracksConfirmedIDs(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{
		1: errors.New("connection reset after request write"),
	}}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.sendRenderedDelivery(
		context.Background(), "123", "", "42",
		"# First\n\n###SPLIT###\n\n## Second", bot.logger,
	)

	require.Error(t, result.err)
	assert.Equal(t, richDeliveryUnknown, result.outcome)
	assert.Equal(t, 1, result.sent)
	assert.Equal(t, 2, result.attempts)
	assert.Equal(t, []string{"message-1"}, result.confirmedIDs)
	require.NotNil(t, result.failedChunk)
	assert.Equal(t, 1, *result.failedChunk)
	require.Len(t, transport.responses, 2, "unknown outcome must not trigger a generic follow-up")
}

func TestResponsePath_LegacyPartialFailureIsNotPersisted(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{
		1: errors.New("connection reset after request write"),
	}}
	bot := newRichDeliveryTestBot(t, transport)
	store := new(testutil.MockStorage)
	bot.msgRepo = store
	path := &responsePath{
		bot: bot, logger: bot.logger, userID: storage.ScopeID("user"),
		convID: "123", replyTo: "42", richMode: "off",
	}

	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	ok := path.sendFinalAndPersist(ctx, span, "# First\n\n###SPLIT###\n\n## Second", nil)
	span.End()

	assert.False(t, ok)
	assert.Equal(t, 2, path.tgCalls)
	require.Len(t, transport.responses, 2)
	store.AssertNotCalled(t, "AddMessageToHistory", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "SetReplyTransportID", mock.Anything, mock.Anything)
}

func TestSendRenderedDelivery_ServerErrorIsUnknownWithoutGeneric(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{
		0: &telegram.APIError{Code: 500, Description: "Internal Server Error"},
	}}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.sendRenderedDelivery(context.Background(), "123", "", "42", "answer", bot.logger)

	require.Error(t, result.err)
	assert.Equal(t, richDeliveryUnknown, result.outcome)
	assert.Zero(t, result.sent)
	assert.Equal(t, 1, result.attempts)
	require.Len(t, transport.responses, 1, "5xx unknown outcome must not trigger a generic follow-up")
}

func TestSendRenderedDelivery_ClientErrorIsRejectedAndMaySendGeneric(t *testing.T) {
	transport := &recordingRichTransport{errors: map[int]error{
		0: &telegram.APIError{Code: 400, Description: "Bad Request: chat not found"},
	}}
	bot := newRichDeliveryTestBot(t, transport)

	result := bot.sendRenderedDelivery(context.Background(), "123", "", "42", "answer", bot.logger)

	require.Error(t, result.err)
	assert.Equal(t, richDeliveryRejected, result.outcome)
	assert.Zero(t, result.sent)
	assert.Equal(t, 2, result.attempts)
	require.Len(t, transport.responses, 2, "confirmed 4xx rejection may send one generic follow-up")
}

func TestResponsePath_ConfirmedRichIsStoredBeforeTransportIDLink(t *testing.T) {
	transport := &recordingRichTransport{ids: map[int]string{0: "telegram-77"}}
	bot := newRichDeliveryTestBot(t, transport)
	store := new(testutil.MockStorage)
	bot.msgRepo = store
	path := &responsePath{
		bot: bot, logger: bot.logger, userID: storage.ScopeID("user"),
		convID: "123", replyTo: "42", richMode: "send",
	}

	order := make([]string, 0, 2)
	store.On("AddMessageToHistory", storage.ScopeID("user"), mock.MatchedBy(func(message storage.Message) bool {
		return message.Role == "assistant" && message.Content == "# Confirmed"
	})).Run(func(mock.Arguments) { order = append(order, "history") }).Return(nil).Once()
	store.On("SetReplyTransportID", storage.ScopeID("user"), "telegram-77").Run(func(mock.Arguments) {
		order = append(order, "link")
	}).Return(nil).Once()

	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	ok := path.sendFinalAndPersist(ctx, span, "# Confirmed", nil)
	span.End()

	require.True(t, ok)
	assert.Equal(t, []string{"history", "link"}, order)
	store.AssertExpectations(t)
}

func TestResponsePath_HistoryInsertFailureDoesNotLinkOlderReply(t *testing.T) {
	transport := &recordingRichTransport{ids: map[int]string{0: "telegram-77"}}
	bot := newRichDeliveryTestBot(t, transport)
	store := new(testutil.MockStorage)
	bot.msgRepo = store
	path := &responsePath{
		bot: bot, logger: bot.logger, userID: storage.ScopeID("user"),
		convID: "123", replyTo: "42", richMode: "send",
	}
	store.On("AddMessageToHistory", storage.ScopeID("user"), mock.Anything).
		Return(errors.New("database unavailable")).Once()

	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	ok := path.sendFinalAndPersist(ctx, span, "# Confirmed on Telegram", nil)
	span.End()

	require.True(t, ok, "confirmed transport delivery is not undone by best-effort history failure")
	store.AssertNotCalled(t, "SetReplyTransportID", mock.Anything, mock.Anything)
	store.AssertExpectations(t)
}

func TestResponsePath_LocalPreflightRejectionSendsOnlyFixedError(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	path := &responsePath{
		bot: bot, logger: bot.logger, userID: storage.ScopeID("user"),
		convID: "123", replyTo: "42", richMode: "send",
	}
	invalid := string([]byte{'o', 'k', 0xff})

	ctx, span := otel.Tracer("test").Start(context.Background(), "final")
	ok := path.sendFinal(ctx, span, invalid)
	span.End()

	assert.False(t, ok)
	assert.Equal(t, 1, path.tgCalls)
	require.Len(t, transport.responses, 1)
	assert.Equal(t, ResponseFormatDefault, transport.responses[0].Format)
	assert.NotContains(t, transport.responses[0].Text, "ok")
}

func TestResponsePath_ShadowUsesDeliveryEquivalentPreflight(t *testing.T) {
	transport := &recordingRichTransport{}
	bot := newRichDeliveryTestBot(t, transport)
	path := &responsePath{
		bot: bot, logger: bot.logger, userID: storage.ScopeID("user"),
		convID: "123", richMode: "shadow",
	}
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, span := provider.Tracer("test").Start(context.Background(), "shadow")

	// Safe legacy preflight succeeds, while the preferred rich representation
	// crosses the semantic-character ceiling and would select local fallback.
	path.shadowRichRender(ctx, span, strings.Repeat("safe filler ", 3000))
	span.End()

	ended := recorder.Ended()
	require.Len(t, ended, 1)
	attributes := make(map[string]any)
	for _, kv := range ended[0].Attributes() {
		switch kv.Value.Type() {
		case attribute.BOOL:
			attributes[string(kv.Key)] = kv.Value.AsBool()
		case attribute.INT64:
			attributes[string(kv.Key)] = kv.Value.AsInt64()
		case attribute.STRING:
			attributes[string(kv.Key)] = kv.Value.AsString()
		}
	}
	assert.Equal(t, true, attributes["bot.rich_message.shadow"])
	assert.Equal(t, true, attributes["bot.rich_message.shadow_local_fallback"])
	assert.Equal(t, "rich_render_or_limit", attributes["bot.rich_message.shadow_fallback_reason"])
	assert.NotEqual(t, true, attributes["bot.rich_message.shadow_failed"])
}

var _ Transport = (*recordingRichTransport)(nil)

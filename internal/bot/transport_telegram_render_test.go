package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/runixer/laplaced/internal/markdown"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"
)

// incidentTableReply reproduces the production reply shape that used to lose
// chunks: Cyrillic prose around a markdown table long enough to force splits.
func incidentTableReply() string {
	var b strings.Builder
	b.WriteString(strings.Repeat("Вступительный абзац с подробными пояснениями и выводами. ", 30))
	b.WriteString("\n\n### Временное расписание\n\n")
	b.WriteString("| Время | Действие | Комментарий |\n| :--- | :--- | :--- |\n")
	for i := 0; i < 30; i++ {
		b.WriteString("| **07:15 - 08:45** | **Подъём, кормление и бодрствование** | Стартуем как обычно, активно играем, чтобы накопить усталость перед дорогой к врачу. |\n")
	}
	b.WriteString("\nЗаключительный абзац с выводами и рекомендациями.")
	return b.String()
}

// entityHeavyReply builds markdown whose HTML render expands well past the
// source length: bold fragments full of characters that escape to entities.
func entityHeavyReply() string {
	fragment := "**a & b < c > d \"q\"** и `x & y` тоже. "
	return strings.Repeat(fragment, 120) // ~4700 runes, HTML much longer
}

func TestTelegramRenderer_AllChunksWithinWireLimit(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"incident table reply", incidentTableReply()},
		{"entity-heavy reply", entityHeavyReply()},
		{"plain long cyrillic", strings.Repeat("Просто длинный текст без разметки. ", 400)},
	}

	r := NewTelegramRenderer(testutil.TestLogger())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks, err := r.Render(context.Background(), tt.text)
			require.NoError(t, err)
			require.NotEmpty(t, chunks)
			for i, chunk := range chunks {
				assert.LessOrEqual(t, markdown.UTF16Length(chunk), telegramMessageLimit,
					"chunk %d exceeds Telegram wire limit", i)
				assert.NotEmpty(t, strings.TrimSpace(chunk))
			}
		})
	}
}

func TestTelegramRenderer_TableChunksKeepHeader(t *testing.T) {
	r := NewTelegramRenderer(testutil.TestLogger())
	chunks, err := r.Render(context.Background(), incidentTableReply())
	require.NoError(t, err)

	// Every chunk containing table rows must carry the header (tables render
	// as <pre> monospace, header text included).
	for i, chunk := range chunks {
		if strings.Contains(chunk, "07:15 - 08:45") {
			assert.Contains(t, chunk, "Время", "chunk %d has table rows but no header", i)
		}
	}
}

func TestTelegramRenderer_ResplitSetsAnomalyAttributes(t *testing.T) {
	getSpans := testutil.WithTracingCapture(t)

	ctx, span := otel.Tracer("test").Start(context.Background(), "bot.processMessageGroup")
	r := NewTelegramRenderer(testutil.TestLogger())
	chunks, err := r.Render(ctx, entityHeavyReply())
	span.End()

	require.NoError(t, err)
	require.NotEmpty(t, chunks)

	spans := getSpans()
	require.NotEmpty(t, spans)
	attrs := make(map[attribute.Key]attribute.Value)
	for _, kv := range spans[len(spans)-1].Attributes {
		attrs[kv.Key] = kv.Value
	}
	v, ok := attrs["bot.anomaly.chunk_resplit"]
	require.True(t, ok, "bot.anomaly.chunk_resplit attribute missing")
	assert.True(t, v.AsBool())
	c, ok := attrs["bot.anomaly.chunk_resplit_count"]
	require.True(t, ok, "bot.anomaly.chunk_resplit_count attribute missing")
	assert.Positive(t, c.AsInt64())
}

func TestTelegramRenderer_NoResplitNoAnomalyAttributes(t *testing.T) {
	getSpans := testutil.WithTracingCapture(t)

	ctx, span := otel.Tracer("test").Start(context.Background(), "bot.processMessageGroup")
	r := NewTelegramRenderer(testutil.TestLogger())
	_, err := r.Render(ctx, "короткий ответ без таблиц")
	span.End()

	require.NoError(t, err)
	spans := getSpans()
	require.NotEmpty(t, spans)
	for _, kv := range spans[len(spans)-1].Attributes {
		assert.NotEqual(t, attribute.Key("bot.anomaly.chunk_resplit"), kv.Key)
	}
}

// captionIncidentText reproduces the Jun-9 production shape: bold/entity-heavy
// Cyrillic markdown just under the old 1000-rune budget whose HTML render
// exceeded 1024 UTF-16 units, which used to ship as raw markdown.
func captionIncidentText() string {
	line := "• **07:15** — Разбудить ☀️ *(Якорь дня & старт)*\n"
	return "⏱️ **Временное расписание на 10 июня:**\n\n" + strings.Repeat(line, 19)
}

func TestTelegramRenderer_RenderCaption(t *testing.T) {
	r := NewTelegramRenderer(testutil.TestLogger())

	t.Run("incident caption fits after shrink, never raw markdown", func(t *testing.T) {
		text := captionIncidentText()
		require.LessOrEqual(t, len([]rune(text)), 1000, "fixture must fit the old markdown budget")
		require.Greater(t, markdown.UTF16Length(mustToHTML(t, text)), telegramCaptionWireLimit,
			"fixture must overflow the wire budget when rendered")

		caption, overflow := r.RenderCaption(context.Background(), text)
		assert.LessOrEqual(t, markdown.UTF16Length(caption), telegramCaptionWireLimit)
		assert.NotContains(t, caption, "**", "caption must be rendered HTML, not raw markdown")
		assert.Contains(t, caption, "<b>")
		assert.NotEmpty(t, overflow, "overflow must carry the demoted tail")
	})

	t.Run("short caption passes through rendered", func(t *testing.T) {
		caption, overflow := r.RenderCaption(context.Background(), "Готово! **Держи** картинку.")
		assert.Equal(t, "Готово! <b>Держи</b> картинку.", caption)
		assert.Empty(t, overflow)
	})

	t.Run("empty input", func(t *testing.T) {
		caption, overflow := r.RenderCaption(context.Background(), "  ")
		assert.Empty(t, caption)
		assert.Empty(t, overflow)
	})

	t.Run("caption and overflow cover the full text", func(t *testing.T) {
		text := strings.Repeat("Длинное описание сгенерированной картинки. ", 60)
		caption, overflow := r.RenderCaption(context.Background(), text)
		assert.LessOrEqual(t, markdown.UTF16Length(caption), telegramCaptionWireLimit)
		assert.NotEmpty(t, overflow)
		assert.True(t, strings.HasSuffix(strings.TrimSpace(text), strings.TrimSpace(overflow)),
			"overflow must be the tail of the original text")
	})
}

func mustToHTML(t *testing.T, md string) string {
	t.Helper()
	h, err := markdown.ToHTML(md)
	require.NoError(t, err)
	return h
}

func TestMMRenderer_RenderCaption(t *testing.T) {
	r := NewMattermostRenderer(300, testutil.TestLogger())
	long := strings.Repeat("слово ", 100)
	caption, overflow := r.RenderCaption(context.Background(), long)
	assert.NotEmpty(t, caption)
	assert.NotEmpty(t, overflow)
	assert.LessOrEqual(t, len([]rune(caption)), 300)
	// Markdown passes through unchanged.
	caption, overflow = r.RenderCaption(context.Background(), "**bold** text")
	assert.Equal(t, "**bold** text", caption)
	assert.Empty(t, overflow)
}

func TestTelegramTransport_SendText_TooLongFallback(t *testing.T) {
	mockAPI := new(testutil.MockBotAPI)
	tr := NewTelegramTransport(mockAPI, testutil.TestConfig(), testutil.TestTranslator(t), testutil.TestLogger())

	// First HTML send is rejected; the fallback resends as plain text pieces.
	mockAPI.On("SendMessage", mock.Anything, mock.MatchedBy(func(req telegram.SendMessageRequest) bool {
		return req.ParseMode == "HTML"
	})).Return(nil, errors.New("telegram api error: Bad Request: message is too long")).Once()
	mockAPI.On("SendMessage", mock.Anything, mock.MatchedBy(func(req telegram.SendMessageRequest) bool {
		return req.ParseMode == "" && markdown.UTF16Length(req.Text) <= telegramMessageLimit
	})).Return(&telegram.Message{MessageID: 77}, nil)

	longText := strings.Repeat("оченьдлинноесообщение ", 250) // > 4096 UTF-16
	msgID, err := tr.SendText(context.Background(), OutgoingResponse{
		ConversationID: "123",
		Text:           longText,
		ReplyTo:        "42",
	})

	require.NoError(t, err)
	assert.Equal(t, "77", msgID)
	mockAPI.AssertExpectations(t)
	// At least two plain pieces must have been sent for a text this long.
	plainCalls := 0
	for _, call := range mockAPI.Calls {
		if req, ok := call.Arguments.Get(1).(telegram.SendMessageRequest); ok && req.ParseMode == "" {
			plainCalls++
		}
	}
	assert.GreaterOrEqual(t, plainCalls, 2)
}

func TestTelegramTransport_SendText_ParseEntitiesRetry(t *testing.T) {
	mockAPI := new(testutil.MockBotAPI)
	tr := NewTelegramTransport(mockAPI, testutil.TestConfig(), testutil.TestTranslator(t), testutil.TestLogger())

	mockAPI.On("SendMessage", mock.Anything, mock.MatchedBy(func(req telegram.SendMessageRequest) bool {
		return req.ParseMode == "HTML"
	})).Return(nil, errors.New(`telegram api error: Bad Request: can't parse entities: Unsupported start tag "." at byte offset 10`)).Once()
	mockAPI.On("SendMessage", mock.Anything, mock.MatchedBy(func(req telegram.SendMessageRequest) bool {
		return req.ParseMode == ""
	})).Return(&telegram.Message{MessageID: 5}, nil).Once()

	msgID, err := tr.SendText(context.Background(), OutgoingResponse{ConversationID: "123", Text: "broken <. html"})
	require.NoError(t, err)
	assert.Equal(t, "5", msgID)
	mockAPI.AssertExpectations(t)
}

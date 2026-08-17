package bot

import (
	"bytes"
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

	"github.com/runixer/laplaced/internal/config"
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
	})).Return(nil, &telegram.APIError{Code: 400, Description: "Bad Request: message is too long"}).Once()
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
	})).Return(nil, &telegram.APIError{Code: 400, Description: `Bad Request: can't parse entities: Unsupported start tag "." at byte offset 10`}).Once()
	mockAPI.On("SendMessage", mock.Anything, mock.MatchedBy(func(req telegram.SendMessageRequest) bool {
		return req.ParseMode == ""
	})).Return(&telegram.Message{MessageID: 5}, nil).Once()

	msgID, err := tr.SendText(context.Background(), OutgoingResponse{ConversationID: "123", Text: "broken <. html"})
	require.NoError(t, err)
	assert.Equal(t, "5", msgID)
	mockAPI.AssertExpectations(t)
}

func TestTelegramTransport_SendText_AmbiguousErrorTextDoesNotRetry(t *testing.T) {
	mockAPI := new(testutil.MockBotAPI)
	tr := NewTelegramTransport(mockAPI, testutil.TestConfig(), testutil.TestTranslator(t), testutil.TestLogger())

	mockAPI.On("SendMessage", mock.Anything, mock.Anything).
		Return(nil, errors.New("connection reset after request write: message is too long")).Once()

	msgID, err := tr.SendText(context.Background(), OutgoingResponse{
		ConversationID: "123",
		Text:           "unique answer",
	})

	require.Error(t, err)
	assert.Empty(t, msgID)
	mockAPI.AssertNumberOfCalls(t, "SendMessage", 1)
	mockAPI.AssertExpectations(t)
}

func TestTelegramTransport_SendText_RichHTML(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
	mockAPI := new(testutil.MockBotAPI)
	tr := NewTelegramTransport(mockAPI, cfg, testutil.TestTranslator(t), testutil.TestLogger())

	mockAPI.On("SendRichMessage", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageRequest) bool {
		return req.ChatID == 123 &&
			req.MessageThreadID != nil && *req.MessageThreadID == 9 &&
			req.ReplyParameters != nil && req.ReplyParameters.MessageID == 42 &&
			req.RichMessage.HTML == "<h1>Heading</h1>" &&
			req.RichMessage.SkipEntityDetection
	})).Return(&telegram.Message{MessageID: 77}, nil).Once()

	msgID, err := tr.SendText(context.Background(), OutgoingResponse{
		ConversationID: "123",
		ThreadRoot:     "9",
		ReplyTo:        "42",
		Text:           "<h1>Heading</h1>",
		Format:         ResponseFormatRichHTML,
	})
	require.NoError(t, err)
	assert.Equal(t, "77", msgID)
	assert.True(t, tr.Capabilities().SupportsRichMessages)
	assert.True(t, tr.Capabilities().SupportsLatex)
	mockAPI.AssertNotCalled(t, "SendMessage", mock.Anything, mock.Anything)
	mockAPI.AssertExpectations(t)
}

func TestTelegramTransport_SendText_RichHTMLDisabled(t *testing.T) {
	mockAPI := new(testutil.MockBotAPI)
	tr := NewTelegramTransport(mockAPI, testutil.TestConfig(), testutil.TestTranslator(t), testutil.TestLogger())

	_, err := tr.SendText(context.Background(), OutgoingResponse{
		ConversationID: "123",
		Text:           "<h1>Heading</h1>",
		Format:         ResponseFormatRichHTML,
	})
	require.ErrorContains(t, err, "rich messages are disabled")
	assert.False(t, tr.Capabilities().SupportsRichMessages)
	mockAPI.AssertNotCalled(t, "SendRichMessage", mock.Anything, mock.Anything)
}

func TestTelegramTransport_SendRichMedia_InjectsTrustedPhotoBlock(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
	cfg.Agents.ImageGenerator.DocumentThresholdBytes = 1024
	mockAPI := new(testutil.MockBotAPI)
	tr := NewTelegramTransport(mockAPI, cfg, testutil.TestTranslator(t), testutil.TestLogger())

	mockAPI.On("SendRichMessage", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageRequest) bool {
		return req.ChatID == 123 &&
			req.MessageThreadID != nil && *req.MessageThreadID == 9 &&
			req.ReplyParameters != nil && req.ReplyParameters.MessageID == 42 &&
			req.RichMessage.HTML == generatedRichPhotoHTML+"<h1>Heading</h1>" &&
			req.RichMessage.SkipEntityDetection &&
			len(req.RichMessage.Media) == 1 &&
			req.RichMessage.Media[0].ID == generatedRichPhotoID &&
			req.RichMessage.Media[0].Media.Media == "attach://"+generatedRichPhotoAttachID &&
			len(req.Attachments) == 1 &&
			req.Attachments[0].ID == generatedRichPhotoAttachID &&
			req.Attachments[0].Filename == "generated.png" &&
			bytes.Equal(req.Attachments[0].Data, generatedTestPNG)
	})).Return(&telegram.Message{MessageID: 88}, nil).Once()

	msgID, err := tr.SendRichMedia(context.Background(), OutgoingRichMedia{
		ConversationID:  "123",
		ThreadRoot:      "9",
		ReplyTo:         "42",
		HTMLParts:       []string{"", "<h1>Heading</h1>"},
		MediaGroupSizes: []int{1},
		Items: []OutgoingMediaItem{{
			Data: append([]byte(nil), generatedTestPNG...), Filename: "generated.png", MIME: "image/png",
			WireKind: OutgoingMediaWireKindPhoto,
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, "88", msgID)
	mockAPI.AssertNotCalled(t, "SendPhoto", mock.Anything, mock.Anything)
	mockAPI.AssertExpectations(t)
}

func TestTelegramTransport_SendRichMedia_RejectionClassification(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
	cfg.Agents.ImageGenerator.DocumentThresholdBytes = 1024

	for _, tt := range []struct {
		name         string
		err          error
		wantFallback bool
	}{
		{
			name: "confirmed rich format rejection",
			err: &telegram.APIError{
				Code: 400, Description: "Bad Request: RICH_MESSAGE_MEDIA_INVALID",
			},
			wantFallback: true,
		},
		{name: "ambiguous network failure", err: errors.New("connection reset after request write")},
		{name: "server failure", err: &telegram.APIError{Code: 500, Description: "Internal Server Error"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mockAPI := new(testutil.MockBotAPI)
			mockAPI.On("SendRichMessage", mock.Anything, mock.Anything).Return(nil, tt.err).Once()
			tr := NewTelegramTransport(mockAPI, cfg, testutil.TestTranslator(t), testutil.TestLogger())

			_, err := tr.SendRichMedia(context.Background(), OutgoingRichMedia{
				ConversationID:  "123",
				HTMLParts:       []string{"", "<p>answer</p>"},
				MediaGroupSizes: []int{1},
				Items: []OutgoingMediaItem{{
					Data: append([]byte(nil), generatedTestPNG...), Filename: "generated.png", MIME: "image/png",
					WireKind: OutgoingMediaWireKindPhoto,
				}},
			})

			require.Error(t, err)
			assert.Equal(t, tt.wantFallback, errors.Is(err, ErrRichMessageRejected))
			mockAPI.AssertExpectations(t)
		})
	}
}

func TestTelegramTransport_SendRichMedia_ComposesMultipleGroupsWithGlobalIDs(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
	mockAPI := new(testutil.MockBotAPI)
	tr := NewTelegramTransport(mockAPI, cfg, testutil.TestTranslator(t), testutil.TestLogger())

	items := make([]OutgoingMediaItem, 11)
	for i := range items {
		items[i] = OutgoingMediaItem{
			Data:          append([]byte(nil), generatedTestPNG...),
			Filename:      fmt.Sprintf("generated-%d.png", i+1),
			MIME:          "image/png",
			WireKind:      OutgoingMediaWireKindPhoto,
			SourceOrdinal: 100 + i,
		}
	}
	expectedHTML := `<h1>Before</h1><img src="tg://photo?id=rich_photo_0"/>` +
		`<p>Middle</p><tg-collage><img src="tg://photo?id=rich_photo_1"/><img src="tg://photo?id=rich_photo_2"/></tg-collage>` +
		`<hr/><tg-slideshow><img src="tg://photo?id=rich_photo_3"/><img src="tg://photo?id=rich_photo_4"/>` +
		`<img src="tg://photo?id=rich_photo_5"/><img src="tg://photo?id=rich_photo_6"/><img src="tg://photo?id=rich_photo_7"/>` +
		`<img src="tg://photo?id=rich_photo_8"/><img src="tg://photo?id=rich_photo_9"/><img src="tg://photo?id=rich_photo_10"/></tg-slideshow><p>After</p>`
	mockAPI.On("SendRichMessage", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageRequest) bool {
		if req.RichMessage.HTML != expectedHTML || len(req.RichMessage.Media) != len(items) || len(req.Attachments) != len(items) {
			return false
		}
		for i := range items {
			mediaID := fmt.Sprintf("rich_photo_%d", i)
			attachmentID := mediaID + "_file"
			if req.RichMessage.Media[i].ID != mediaID ||
				req.RichMessage.Media[i].Media.Media != "attach://"+attachmentID ||
				req.Attachments[i].ID != attachmentID ||
				req.Attachments[i].Filename != items[i].Filename {
				return false
			}
		}
		return true
	})).Return(&telegram.Message{MessageID: 89}, nil).Once()

	msgID, err := tr.SendRichMedia(context.Background(), OutgoingRichMedia{
		ConversationID:  "123",
		HTMLParts:       []string{"<h1>Before</h1>", "<p>Middle</p>", "<hr/>", "<p>After</p>"},
		MediaGroupSizes: []int{1, 2, 8},
		Items:           items,
	})

	require.NoError(t, err)
	assert.Equal(t, "89", msgID)
	mockAPI.AssertExpectations(t)
}

func TestTelegramTransport_SendRichMedia_AllowsMediaOnlyTopology(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
	mockAPI := new(testutil.MockBotAPI)
	tr := NewTelegramTransport(mockAPI, cfg, testutil.TestTranslator(t), testutil.TestLogger())

	mockAPI.On("SendRichMessage", mock.Anything, mock.MatchedBy(func(req telegram.SendRichMessageRequest) bool {
		return req.RichMessage.HTML == generatedRichPhotoHTML && len(req.RichMessage.Media) == 1
	})).Return(&telegram.Message{MessageID: 90}, nil).Once()

	msgID, err := tr.SendRichMedia(context.Background(), OutgoingRichMedia{
		ConversationID:  "123",
		HTMLParts:       []string{"", ""},
		MediaGroupSizes: []int{1},
		Items: []OutgoingMediaItem{{
			Data: append([]byte(nil), generatedTestPNG...), Filename: "generated.png", MIME: "image/png",
			WireKind: OutgoingMediaWireKindPhoto,
		}},
	})

	require.NoError(t, err)
	assert.Equal(t, "90", msgID)
	mockAPI.AssertExpectations(t)
}

func TestTelegramTransport_SendRichMedia_RejectsInvalidTopologyAndGraphBeforeAPI(t *testing.T) {
	validItem := func() OutgoingMediaItem {
		return OutgoingMediaItem{
			Data: append([]byte(nil), generatedTestPNG...), Filename: "generated.png", MIME: "image/png",
			WireKind: OutgoingMediaWireKindPhoto,
		}
	}
	tests := []struct {
		name      string
		media     OutgoingRichMedia
		wantError string
	}{
		{
			name:      "implicit media-only legacy shape",
			media:     OutgoingRichMedia{ConversationID: "123", Items: []OutgoingMediaItem{validItem()}},
			wantError: "requires explicit topology",
		},
		{
			name: "HTML part count mismatch",
			media: OutgoingRichMedia{ConversationID: "123", HTMLParts: []string{""}, MediaGroupSizes: []int{1},
				Items: []OutgoingMediaItem{validItem()}},
			wantError: "want 2",
		},
		{
			name: "empty media group",
			media: OutgoingRichMedia{ConversationID: "123", HTMLParts: []string{"", ""}, MediaGroupSizes: []int{0},
				Items: []OutgoingMediaItem{validItem()}},
			wantError: "want 1-10",
		},
		{
			name: "oversized media group",
			media: OutgoingRichMedia{ConversationID: "123", HTMLParts: []string{"", ""}, MediaGroupSizes: []int{11},
				Items: []OutgoingMediaItem{validItem()}},
			wantError: "want 1-10",
		},
		{
			name: "item sum mismatch",
			media: OutgoingRichMedia{ConversationID: "123", HTMLParts: []string{"", ""}, MediaGroupSizes: []int{2},
				Items: []OutgoingMediaItem{validItem()}},
			wantError: "consumes 2 items, payload has 1",
		},
		{
			name: "dangling model-authored photo reference",
			media: OutgoingRichMedia{ConversationID: "123", HTMLParts: []string{`<img src="tg://photo?id=bogus"/>`, ""}, MediaGroupSizes: []int{1},
				Items: []OutgoingMediaItem{validItem()}},
			wantError: `photo id "bogus" has no rich message media entry`,
		},
		{
			name: "duplicate generated photo reference",
			media: OutgoingRichMedia{ConversationID: "123", HTMLParts: []string{`<img src="tg://photo?id=rich_photo_0"/>`, ""}, MediaGroupSizes: []int{1},
				Items: []OutgoingMediaItem{validItem()}},
			wantError: `photo id "rich_photo_0" is referenced more than once`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testutil.TestConfig()
			cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
			mockAPI := new(testutil.MockBotAPI)
			tr := NewTelegramTransport(mockAPI, cfg, testutil.TestTranslator(t), testutil.TestLogger())

			_, err := tr.SendRichMedia(context.Background(), tt.media)

			require.ErrorContains(t, err, tt.wantError)
			mockAPI.AssertNotCalled(t, "SendRichMessage", mock.Anything, mock.Anything)
		})
	}
}

func TestTelegramTransport_SendText_RichRejectionClassification(t *testing.T) {
	newTransport := func(t *testing.T, sendErr error) (*TelegramTransport, *testutil.MockBotAPI) {
		t.Helper()
		cfg := testutil.TestConfig()
		cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
		mockAPI := new(testutil.MockBotAPI)
		mockAPI.On("SendRichMessage", mock.Anything, mock.Anything).Return(nil, sendErr).Once()
		return NewTelegramTransport(mockAPI, cfg, testutil.TestTranslator(t), testutil.TestLogger()), mockAPI
	}

	t.Run("confirmed API rejection permits legacy fallback", func(t *testing.T) {
		tr, mockAPI := newTransport(t, &telegram.APIError{Code: 400, Description: "Bad Request: RICH_MESSAGE_INVALID"})
		_, err := tr.SendText(context.Background(), OutgoingResponse{
			ConversationID: "123",
			Text:           "<p>answer</p>",
			Format:         ResponseFormatRichHTML,
		})
		require.ErrorIs(t, err, ErrRichMessageRejected)
		var apiErr *telegram.APIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 400, apiErr.Code)
		mockAPI.AssertExpectations(t)
	})

	t.Run("method not found permits legacy fallback", func(t *testing.T) {
		tr, mockAPI := newTransport(t, &telegram.APIError{Code: 404, Description: "Not Found"})
		_, err := tr.SendText(context.Background(), OutgoingResponse{
			ConversationID: "123",
			Text:           "<p>answer</p>",
			Format:         ResponseFormatRichHTML,
		})
		require.ErrorIs(t, err, ErrRichMessageRejected)
		mockAPI.AssertExpectations(t)
	})

	confirmedNonFormatRejections := []struct {
		name        string
		code        int
		description string
	}{
		{name: "chat not found", code: 400, description: "Bad Request: chat not found"},
		{name: "invalid thread", code: 400, description: "Bad Request: message thread not found"},
		{name: "ordinary HTML parse error", code: 400, description: "Bad Request: can't parse entities"},
		{name: "unauthorized", code: 401, description: "Unauthorized"},
		{name: "forbidden", code: 403, description: "Forbidden: bot was blocked by the user"},
		{name: "generic 404", code: 404, description: "Bad Request: chat not found"},
		{name: "rate limited", code: 429, description: "Too Many Requests"},
		{name: "server error", code: 500, description: "Internal Server Error"},
	}
	for _, tt := range confirmedNonFormatRejections {
		t.Run(tt.name+" does not permit cross-format fallback", func(t *testing.T) {
			tr, mockAPI := newTransport(t, &telegram.APIError{Code: tt.code, Description: tt.description})
			_, err := tr.SendText(context.Background(), OutgoingResponse{
				ConversationID: "123",
				Text:           "<p>answer</p>",
				Format:         ResponseFormatRichHTML,
			})
			require.Error(t, err)
			assert.NotErrorIs(t, err, ErrRichMessageRejected)
			mockAPI.AssertExpectations(t)
		})
	}

	t.Run("ambiguous network error forbids content resend", func(t *testing.T) {
		tr, mockAPI := newTransport(t, errors.New("connection reset after request write"))
		_, err := tr.SendText(context.Background(), OutgoingResponse{
			ConversationID: "123",
			Text:           "<p>answer</p>",
			Format:         ResponseFormatRichHTML,
		})
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrRichMessageRejected)
		mockAPI.AssertExpectations(t)
	})
}

func TestTelegramTransport_SendText_RejectsMalformedSuccess(t *testing.T) {
	for _, tt := range []struct {
		name string
		msg  *telegram.Message
	}{
		{name: "nil message", msg: nil},
		{name: "zero message id", msg: &telegram.Message{}},
	} {
		t.Run("rich/"+tt.name, func(t *testing.T) {
			cfg := testutil.TestConfig()
			cfg.Telegram.RichMessages.Mode = config.TelegramRichMessagesSend
			mockAPI := new(testutil.MockBotAPI)
			mockAPI.On("SendRichMessage", mock.Anything, mock.Anything).Return(tt.msg, nil).Once()
			tr := NewTelegramTransport(mockAPI, cfg, testutil.TestTranslator(t), testutil.TestLogger())

			msgID, err := tr.SendText(context.Background(), OutgoingResponse{
				ConversationID: "123",
				Text:           "<p>answer</p>",
				Format:         ResponseFormatRichHTML,
			})
			require.Error(t, err)
			assert.Empty(t, msgID)
			assert.NotErrorIs(t, err, ErrRichMessageRejected)
			mockAPI.AssertExpectations(t)
		})

		t.Run("legacy/"+tt.name, func(t *testing.T) {
			mockAPI := new(testutil.MockBotAPI)
			mockAPI.On("SendMessage", mock.Anything, mock.Anything).Return(tt.msg, nil).Once()
			tr := NewTelegramTransport(mockAPI, testutil.TestConfig(), testutil.TestTranslator(t), testutil.TestLogger())

			msgID, err := tr.SendText(context.Background(), OutgoingResponse{ConversationID: "123", Text: "answer"})
			require.Error(t, err)
			assert.Empty(t, msgID)
			mockAPI.AssertExpectations(t)
		})
	}
}

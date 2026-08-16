package bot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/runixer/laplaced/internal/storage"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/markdown"
	"github.com/runixer/laplaced/internal/obs"
	"github.com/runixer/laplaced/internal/telegram"
)

// telegramMarkdownSafeLimit is the per-chunk character budget before HTML
// conversion (safety margin under the 4096 UTF-16 wire limit).
const telegramMarkdownSafeLimit = 3500

// telegramReactionEmoji is the complete fixed set a bot may use with
// setMessageReaction (Bot API ReactionTypeEmoji). Forms are API-exact: no
// U+FE0F variation selectors ("❤" not "❤️"), ZWJ sequences kept as listed.
// See https://core.telegram.org/bots/api#reactiontypeemoji
var telegramReactionEmoji = []string{
	"👍", "👎", "❤", "🔥", "🥰", "👏", "😁", "🤔", "🤯", "😱",
	"🤬", "😢", "🎉", "🤩", "🤮", "💩", "🙏", "👌", "🕊", "🤡",
	"🥱", "🥴", "😍", "🐳", "❤‍🔥", "🌚", "🌭", "💯", "🤣", "⚡",
	"🍌", "🏆", "💔", "🤨", "😐", "🍓", "🍾", "💋", "🖕", "😈",
	"😴", "😭", "🤓", "👻", "👨‍💻", "👀", "🎃", "🙈", "😇", "😨",
	"🤝", "✍", "🤗", "🫡", "🎅", "🎄", "☃", "💅", "🤪", "🗿",
	"🆒", "💘", "🙉", "🦄", "😘", "💊", "🙊", "😎", "👾",
	"🤷‍♂", "🤷", "🤷‍♀", "😡",
}

// TelegramTransport adapts the Telegram Bot API to the neutral Transport
// interface. It wraps the same telegram.BotAPI the bot already holds, so the
// Telegram send path is byte-identical to the pre-seam code.
type TelegramTransport struct {
	api        telegram.BotAPI
	cfg        *config.Config
	translator *i18n.Translator
	logger     *slog.Logger
}

// NewTelegramTransport builds the Telegram output adapter.
func NewTelegramTransport(api telegram.BotAPI, cfg *config.Config, translator *i18n.Translator, logger *slog.Logger) *TelegramTransport {
	return &TelegramTransport{api: api, cfg: cfg, translator: translator, logger: logger}
}

func (t *TelegramTransport) Kind() string { return transportTelegram }

func (t *TelegramTransport) Capabilities() Capabilities {
	richMessages := t.cfg.Telegram.RichMessages.AnyEnabled()
	return Capabilities{
		MaxMessageLen:         telegramMessageLimit,
		ParseMode:             "HTML",
		SupportsLatex:         richMessages,
		SupportsStreaming:     true,
		SupportsRichMessages:  richMessages,
		SupportsReactions:     true,
		SupportsMedia:         true,
		MaxMediaItemsPerGroup: 10, // Telegram album limit
		EmojiStyle:            "unicode",
		AvailableReactions:    telegramReactionEmoji,
	}
}

func (t *TelegramTransport) IsAllowed(nativeSenderID string) bool {
	id, err := strconv.ParseInt(nativeSenderID, 10, 64)
	if err != nil {
		return false
	}
	for _, allowed := range t.cfg.Bot.AllowedUserIDs {
		if allowed == id {
			return true
		}
	}
	return false
}

func (t *TelegramTransport) AllowlistConfigured() bool {
	return len(t.cfg.Bot.AllowedUserIDs) > 0
}

// SendText sends one rendered HTML chunk. Two last-resort recoveries keep a
// reply from being lost (both should be unreachable with a correct renderer,
// and are surfaced as bot.anomaly.* span attributes when they fire):
//   - "can't parse entities": retried once as plain text (ParseMode cleared);
//   - "message is too long": resent as plain text hard-split by UTF-16.
//
// Both retries use a fresh, non-cancellable context.
func (t *TelegramTransport) SendText(ctx context.Context, r OutgoingResponse) (string, error) {
	chatID, err := strconv.ParseInt(r.ConversationID, 10, 64)
	if err != nil {
		return "", err
	}
	if r.Format == ResponseFormatRichHTML {
		if !t.cfg.Telegram.RichMessages.AnyEnabled() {
			return "", fmt.Errorf("telegram rich messages are disabled")
		}
		req := telegram.SendRichMessageRequest{
			ChatID:          chatID,
			MessageThreadID: intPtrOrNil(atoiOrZero(r.ThreadRoot)),
			RichMessage: telegram.InputRichMessage{
				HTML:                r.Text,
				SkipEntityDetection: true,
			},
		}
		if replyID := atoiOrZero(r.ReplyTo); replyID != 0 {
			req.ReplyParameters = &telegram.ReplyParameters{MessageID: replyID}
		}

		sent, sendErr := t.api.SendRichMessage(ctx, req)
		if sendErr != nil {
			var apiErr *telegram.APIError
			if errors.As(sendErr, &apiErr) && isRichMessageFormatRejection(apiErr) {
				return "", fmt.Errorf("%w: %w", ErrRichMessageRejected, sendErr)
			}
			return "", sendErr
		}
		if sent == nil || sent.MessageID <= 0 {
			// A malformed success may have followed an accepted request. Surface an
			// unknown outcome and never trigger cross-format fallback.
			return "", fmt.Errorf("sendRichMessage returned no stable message id")
		}
		return strconv.Itoa(sent.MessageID), nil
	}

	req := telegram.SendMessageRequest{
		ChatID:          chatID,
		MessageThreadID: intPtrOrNil(atoiOrZero(r.ThreadRoot)),
		Text:            r.Text,
		ParseMode:       "HTML",
	}
	if replyID := atoiOrZero(r.ReplyTo); replyID != 0 {
		req.ReplyToMessageID = replyID
	}

	sent, err := t.api.SendMessage(ctx, req)
	if isConfirmedTelegramTextRejection(err, "can't parse entities") {
		t.logger.Warn("retrying send without HTML parse mode due to parsing error")
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(attribute.Bool("bot.anomaly.send_fallback_parse_entities", true))
		obs.RecordContent(span, "bot.anomaly.unparsable_html", r.Text)
		retryCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		req.ParseMode = ""
		sent, err = t.api.SendMessage(retryCtx, req)
	}
	if isConfirmedTelegramTextRejection(err, "message is too long") {
		t.logger.Warn("message over the wire limit at send time, resending as hard-split plain text",
			"utf16_length", markdown.UTF16Length(req.Text))
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(attribute.Bool("bot.anomaly.send_fallback_too_long", true))
		obs.RecordContent(span, "bot.anomaly.oversized_html", r.Text)
		retryCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for i, piece := range markdown.HardSplitUTF16(req.Text, telegramMessageLimit) {
			pieceReq := req
			pieceReq.Text = piece
			pieceReq.ParseMode = ""
			if i > 0 {
				pieceReq.ReplyToMessageID = 0
			}
			pieceSent, pieceErr := t.api.SendMessage(retryCtx, pieceReq)
			if pieceErr != nil {
				return "", pieceErr
			}
			if pieceSent == nil || pieceSent.MessageID <= 0 {
				return "", fmt.Errorf("sendMessage hard-split piece %d returned no stable message id", i)
			}
			if i == 0 {
				sent, err = pieceSent, nil
			}
		}
	}
	if err != nil {
		return "", err
	}
	if sent == nil || sent.MessageID <= 0 {
		// As with sendRichMessage, a malformed success has an unknown outcome:
		// the request may already have created a persistent message.
		return "", fmt.Errorf("sendMessage returned no stable message id")
	}
	return strconv.Itoa(sent.MessageID), nil
}

func isConfirmedTelegramTextRejection(err error, descriptionFragment string) bool {
	var apiErr *telegram.APIError
	return errors.As(err, &apiErr) &&
		apiErr.Code >= 400 && apiErr.Code < 500 &&
		strings.Contains(strings.ToLower(apiErr.Description), strings.ToLower(descriptionFragment))
}

func isRichMessageFormatRejection(apiErr *telegram.APIError) bool {
	if apiErr == nil {
		return false
	}
	description := strings.ToUpper(strings.TrimSpace(apiErr.Description))
	if apiErr.Code == 400 {
		// Telegram's rich parser/validator uses stable RICH_MESSAGE_* symbolic
		// descriptions (for example RICH_MESSAGE_DATE_TOO_LONG). Keep this
		// classifier deliberately narrow: chat/thread/reply/permission failures
		// are confirmed request rejections, but are not permission to resend the
		// answer through a different method/format.
		return strings.HasPrefix(description, "BAD REQUEST: RICH_MESSAGE_") ||
			strings.HasPrefix(description, "RICH_MESSAGE_") ||
			strings.HasPrefix(description, "BAD REQUEST: CAN'T PARSE RICH MESSAGE") ||
			strings.HasPrefix(description, "BAD REQUEST: FAILED TO PARSE RICH MESSAGE")
	}
	// A 404 with the canonical Bot API method-not-found description means the
	// endpoint does not implement sendRichMessage, so legacy fallback is safe.
	return apiErr.Code == 404 && (description == "NOT FOUND" || description == "METHOD NOT FOUND")
}

func (t *TelegramTransport) SendTyping(ctx context.Context, conversationID string) error {
	chatID, err := strconv.ParseInt(conversationID, 10, 64)
	if err != nil {
		return err
	}
	return t.api.SendChatAction(ctx, telegram.SendChatActionRequest{
		ChatID: chatID,
		Action: "typing",
	})
}

// SetReaction adds the given emoji reaction to the message. The emoji must be
// one of telegramReactionEmoji (API-exact form).
func (t *TelegramTransport) SetReaction(ctx context.Context, conversationID, messageID, emoji string) error {
	chatID, err := strconv.ParseInt(conversationID, 10, 64)
	if err != nil {
		return err
	}
	msgID, err := strconv.Atoi(messageID)
	if err != nil {
		return err
	}
	return t.api.SetMessageReaction(ctx, telegram.SetMessageReactionRequest{
		ChatID:    chatID,
		MessageID: msgID,
		Reaction:  []telegram.ReactionType{{Type: "emoji", Emoji: emoji}},
	})
}

// telegramCaptionLimit is the initial markdown budget RenderCaption tries for
// sendPhoto / sendMediaGroup captions; the real constraint is
// telegramCaptionWireLimit on the rendered HTML, which RenderCaption enforces
// by shrinking this budget. Overflow is sent as follow-up text.
const telegramCaptionLimit = 1000

const (
	generatedRichPhotoID       = "generated_photo_0"
	generatedRichPhotoAttachID = "generated_photo_0_file"
	generatedRichPhotoHTML     = `<img src="tg://photo?id=generated_photo_0"/>`
)

// SendRichMedia atomically uploads one trusted generated photo and persists it
// together with the complete Rich HTML response. This is intentionally the
// narrow first vertical slice: albums and images selected for document-quality
// delivery continue through SendMedia until their native layout is validated.
//
// The photo block is injected after model Markdown has passed the allowlisted
// Rich HTML renderer. Consequently model-authored URLs can never become media
// side effects; only the bytes resolved from GeneratedArtifactIDs arrive here.
func (t *TelegramTransport) SendRichMedia(ctx context.Context, m OutgoingRichMedia) (string, error) {
	if !t.cfg.Telegram.RichMessages.AnyEnabled() {
		return "", fmt.Errorf("telegram rich messages are disabled")
	}
	if len(m.Items) != 1 {
		return "", fmt.Errorf("telegram rich media MVP requires exactly one photo, got %d", len(m.Items))
	}
	item := m.Items[0]
	if len(item.Data) == 0 || item.AsDocument || !strings.HasPrefix(strings.ToLower(item.MIME), "image/") {
		return "", fmt.Errorf("telegram rich media MVP requires one non-empty image photo")
	}
	threshold := t.cfg.Agents.ImageGenerator.DocumentThresholdBytes
	if threshold > 0 && len(item.Data) > threshold {
		return "", fmt.Errorf("telegram rich media photo exceeds document-quality threshold")
	}
	if strings.TrimSpace(m.HTML) == "" {
		return "", fmt.Errorf("telegram rich media requires a non-empty Rich HTML body")
	}

	chatID, err := strconv.ParseInt(m.ConversationID, 10, 64)
	if err != nil {
		return "", err
	}
	filename := strings.TrimSpace(item.Filename)
	if filename == "" {
		filename = "generated.png"
	}
	// Media must be a top-level block. A bare img is the official no-caption
	// form; the separately rendered body keeps headings/lists/formulas as their
	// own blocks instead of forcing them into RichText-only figcaption content.
	richHTML := generatedRichPhotoHTML + m.HTML
	req := telegram.SendRichMessageRequest{
		ChatID:          chatID,
		MessageThreadID: intPtrOrNil(atoiOrZero(m.ThreadRoot)),
		RichMessage: telegram.InputRichMessage{
			HTML: richHTML,
			Media: []telegram.InputRichMessageMedia{{
				ID: generatedRichPhotoID,
				Media: telegram.InputRichMessagePhoto{
					Media: "attach://" + generatedRichPhotoAttachID,
				},
			}},
			SkipEntityDetection: true,
		},
		Attachments: []telegram.RichMessageAttachment{{
			ID:       generatedRichPhotoAttachID,
			Filename: filename,
			Data:     item.Data,
		}},
	}
	if replyID := atoiOrZero(m.ReplyTo); replyID != 0 {
		req.ReplyParameters = &telegram.ReplyParameters{MessageID: replyID}
	}

	sent, sendErr := t.api.SendRichMessage(ctx, req)
	if sendErr != nil {
		var apiErr *telegram.APIError
		if errors.As(sendErr, &apiErr) && isRichMessageFormatRejection(apiErr) {
			return "", fmt.Errorf("%w: %w", ErrRichMessageRejected, sendErr)
		}
		return "", sendErr
	}
	if sent == nil || sent.MessageID <= 0 {
		return "", fmt.Errorf("sendRichMessage with media returned no stable message id")
	}
	return strconv.Itoa(sent.MessageID), nil
}

// SendMedia delivers a batch of files as Telegram photos and/or documents. It
// reproduces the legacy send_media_response.go policy byte-for-byte: items over
// the configured document threshold (or forced via AsDocument) go as documents
// preserving resolution, the rest as photos; both kinds can't share a media
// group, so they're sent as separate batches. The caption (rendered to HTML)
// rides the document batch when present, else the photo batch; the reply-to
// anchors the caption-bearing batch only.
func (t *TelegramTransport) SendMedia(ctx context.Context, m OutgoingMedia) (string, error) {
	chatID, err := strconv.ParseInt(m.ConversationID, 10, 64)
	if err != nil {
		return "", err
	}
	if len(m.Items) == 0 {
		return "", fmt.Errorf("telegram media delivery requires at least one item")
	}
	thread := intPtrOrNil(atoiOrZero(m.ThreadRoot))
	replyTo := atoiOrZero(m.ReplyTo)

	// Caption arrives in wire format (HTML) from the renderer, already fitted
	// to the caption budget.
	caption := m.Caption
	parseMode := ""
	if caption != "" {
		parseMode = "HTML"
	}

	// Split into photo-size and document-size batches. Threshold default covers
	// 2K/4K outputs (Telegram re-encodes photos to ~1280px; documents preserve
	// originals).
	threshold := t.cfg.Agents.ImageGenerator.DocumentThresholdBytes
	var photoBatch, docBatch []OutgoingMediaItem
	for _, it := range m.Items {
		if it.AsDocument || (threshold > 0 && len(it.Data) > threshold) {
			docBatch = append(docBatch, it)
		} else {
			photoBatch = append(photoBatch, it)
		}
	}

	// Caption goes with documents when any exist (they're the high-res result),
	// else with photos. The caption-bearing batch carries the reply-to.
	captionOwnerIsDoc := len(docBatch) > 0

	var firstMsgID string
	if len(docBatch) > 0 {
		c, pm, rt := "", "", 0
		if captionOwnerIsDoc {
			c, pm, rt = caption, parseMode, replyTo
		}
		id, sendErr := t.sendItemsAsDocuments(ctx, chatID, thread, rt, docBatch, c, pm)
		if firstMsgID == "" {
			firstMsgID = id
		}
		if sendErr != nil {
			return firstMsgID, sendErr
		}
	}
	if len(photoBatch) > 0 {
		c, pm, rt := "", "", replyTo
		if captionOwnerIsDoc {
			rt = 0 // caption + reply-to already went with the documents
		} else {
			c, pm = caption, parseMode
		}
		id, sendErr := t.sendItemsAsPhotos(ctx, chatID, thread, rt, photoBatch, c, pm)
		if firstMsgID == "" {
			firstMsgID = id
		}
		if sendErr != nil {
			return firstMsgID, sendErr
		}
	}
	if firstMsgID == "" {
		return "", fmt.Errorf("telegram media delivery returned no stable message id")
	}
	return firstMsgID, nil
}

// sendItemsAsPhotos sends a batch as sendPhoto (1 item) or sendMediaGroup (2+),
// returning the first resulting message id.
func (t *TelegramTransport) sendItemsAsPhotos(ctx context.Context, chatID int64, thread *int, replyTo int, batch []OutgoingMediaItem, caption, parseMode string) (string, error) {
	if len(batch) == 0 {
		return "", nil
	}
	if len(batch) == 1 {
		sent, err := t.api.SendPhoto(ctx, telegram.SendPhotoRequest{
			ChatID:           chatID,
			MessageThreadID:  thread,
			PhotoData:        batch[0].Data,
			PhotoFilename:    batch[0].Filename,
			Caption:          caption,
			ParseMode:        parseMode,
			ReplyToMessageID: replyTo,
		})
		if err != nil {
			t.logger.Error("sendPhoto failed", "error", err)
			return "", err
		}
		return confirmedTelegramMessageID(sent, "sendPhoto")
	}
	media := make([]telegram.InputMediaPhoto, 0, len(batch))
	for i, it := range batch {
		item := telegram.InputMediaPhoto{Data: it.Data, Filename: it.Filename}
		if i == 0 {
			item.Caption = caption
			item.ParseMode = parseMode
		}
		media = append(media, item)
	}
	sent, err := t.api.SendMediaGroup(ctx, telegram.SendMediaGroupRequest{
		ChatID:           chatID,
		MessageThreadID:  thread,
		Media:            media,
		ReplyToMessageID: replyTo,
	})
	if err != nil {
		t.logger.Error("sendMediaGroup failed", "error", err)
		return "", err
	}
	return confirmedTelegramMediaGroupID(sent, len(batch), "sendMediaGroup")
}

// sendItemsAsDocuments sends a batch as sendDocument (1 item) or
// sendMediaGroup-of-documents (2+), returning the first resulting message id.
func (t *TelegramTransport) sendItemsAsDocuments(ctx context.Context, chatID int64, thread *int, replyTo int, batch []OutgoingMediaItem, caption, parseMode string) (string, error) {
	if len(batch) == 0 {
		return "", nil
	}
	if len(batch) == 1 {
		sent, err := t.api.SendDocument(ctx, telegram.SendDocumentRequest{
			ChatID:           chatID,
			MessageThreadID:  thread,
			Data:             batch[0].Data,
			Filename:         batch[0].Filename,
			Caption:          caption,
			ParseMode:        parseMode,
			ReplyToMessageID: replyTo,
		})
		if err != nil {
			t.logger.Error("sendDocument failed", "error", err)
			return "", err
		}
		return confirmedTelegramMessageID(sent, "sendDocument")
	}
	media := make([]telegram.InputMediaDocument, 0, len(batch))
	for i, it := range batch {
		item := telegram.InputMediaDocument{Data: it.Data, Filename: it.Filename}
		if i == 0 {
			item.Caption = caption
			item.ParseMode = parseMode
		}
		media = append(media, item)
	}
	sent, err := t.api.SendMediaGroupDocuments(ctx, telegram.SendMediaGroupDocumentsRequest{
		ChatID:           chatID,
		MessageThreadID:  thread,
		Media:            media,
		ReplyToMessageID: replyTo,
	})
	if err != nil {
		t.logger.Error("sendMediaGroup(documents) failed", "error", err)
		return "", err
	}
	return confirmedTelegramMediaGroupID(sent, len(batch), "sendMediaGroup(documents)")
}

func confirmedTelegramMessageID(message *telegram.Message, method string) (string, error) {
	if message == nil || message.MessageID <= 0 {
		messageID := 0
		if message != nil {
			messageID = message.MessageID
		}
		return "", fmt.Errorf("%s returned invalid message_id %d", method, messageID)
	}
	return strconv.Itoa(message.MessageID), nil
}

func confirmedTelegramMediaGroupID(messages []telegram.Message, expected int, method string) (string, error) {
	if len(messages) != expected {
		return "", fmt.Errorf("%s returned %d messages, expected %d", method, len(messages), expected)
	}
	for i, message := range messages {
		if message.MessageID <= 0 {
			return "", fmt.Errorf("%s returned invalid message_id %d at index %d", method, message.MessageID, i)
		}
	}
	return strconv.Itoa(messages[0].MessageID), nil
}

// telegramCaptionWireLimit is Telegram's hard caption limit in UTF-16 units.
const telegramCaptionWireLimit = 1024

// RenderCaption fits the start of the markdown response into Telegram's
// caption budget measured on the RENDERED HTML (markdown length is a poor
// proxy: tags and entity escaping expand the text). The split point shrinks
// by the observed expansion ratio until the HTML fits; overflow markdown is
// returned for the caller to send as follow-up text. The caption is always
// valid HTML — never raw markdown.
func (r *TelegramRenderer) RenderCaption(ctx context.Context, text string) (string, string) {
	return r.renderCaptionWith(ctx, text, markdown.ToHTML, html.EscapeString)
}

// RenderSafeRichCaption keeps generated-media captions on the legacy media
// envelope while applying the same active-content policy as Rich Message
// output. Model-authored images, unsafe links and direct/bare mentions remain
// visible text but cannot become Telegram entities.
func (r *TelegramRenderer) RenderSafeRichCaption(ctx context.Context, text string) (string, string) {
	return r.renderCaptionWith(ctx, text, markdown.ToSafeLegacyHTML, markdown.SafeLegacyPlainText)
}

func (r *TelegramRenderer) renderCaptionWith(ctx context.Context, text string, convert telegramHTMLConverter, escapePlain telegramPlainEscaper) (string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}

	budget := telegramCaptionLimit
	shrinks := 0
	for attempt := 0; attempt < 6; attempt++ {
		caption, overflow := splitCaption(text, budget)
		htmlStr, err := convert(caption)
		if err != nil {
			r.logger.Warn("caption markdown -> HTML failed, using escaped plain text", "error", err)
			htmlStr = escapePlain(caption)
		}
		utf16Len := markdown.UTF16Length(htmlStr)
		if utf16Len <= telegramCaptionWireLimit {
			if shrinks > 0 {
				r.logger.Warn("caption over the wire budget after HTML render, shrunk",
					"iterations", shrinks)
				r.recordCaptionShrink(ctx, shrinks)
			}
			return htmlStr, overflow
		}
		shrinks++
		newBudget := budget * telegramCaptionWireLimit / utf16Len
		if newBudget >= budget {
			newBudget = budget - 1
		}
		if newBudget < 1 {
			break
		}
		budget = newBudget
	}

	// Terminal fallback (pathological expansion): send the media without a
	// caption and deliver the whole text as follow-up messages — lossless.
	r.logger.Warn("caption could not be fitted into the wire budget, demoting to follow-up text")
	r.recordCaptionShrink(ctx, shrinks)
	return "", text
}

// recordCaptionShrink surfaces caption demotion on the root span.
func (r *TelegramRenderer) recordCaptionShrink(ctx context.Context, iterations int) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(
		attribute.Bool("bot.anomaly.caption_demoted", true),
		attribute.Int("bot.anomaly.caption_shrink_iterations", iterations),
	)
}

// strPtrOrNil returns a pointer to s, or nil when s is empty — for nullable
// string columns (history attribution) that should stay NULL when unset.
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// atoiOrZero parses a string id to int, returning 0 on empty/invalid input
// (so intPtrOrNil drops it from the request).
func atoiOrZero(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// TelegramRenderer converts canonical markdown into wire-format HTML chunks:
// split on the ###SPLIT### delimiter, fix list numbering across parts, split
// oversized parts, then convert each chunk markdown -> HTML. Chunks whose
// rendered HTML exceeds the 4096 UTF-16 wire limit (tags and entity escaping
// expand the text unpredictably) are re-split with a smaller source budget;
// the terminal fallback is escaped plain text hard-split by UTF-16, so a
// reply is never lost to "message is too long".
type TelegramRenderer struct {
	logger *slog.Logger
}

// maxRenderResplitDepth caps the render -> overflow -> re-split recursion.
const maxRenderResplitDepth = 5

// NewTelegramRenderer builds the Telegram HTML renderer.
func NewTelegramRenderer(logger *slog.Logger) *TelegramRenderer {
	return &TelegramRenderer{logger: logger}
}

func (r *TelegramRenderer) Render(ctx context.Context, text string) ([]string, error) {
	return r.renderWith(ctx, text, markdown.ToHTML, html.EscapeString)
}

// renderSafeRichFallback pre-renders the legacy representation used after a
// confirmed/local Rich Message rejection. It intentionally has a separate
// entry point from Render: the normal flag-off renderer remains byte-compatible,
// while fallback output enforces the model-safe link/media/mention policy.
func (r *TelegramRenderer) renderSafeRichFallback(ctx context.Context, text string) ([]string, error) {
	return r.renderWith(ctx, text, markdown.ToSafeLegacyHTML, markdown.SafeLegacyPlainText)
}

type telegramHTMLConverter func(string) (string, error)
type telegramPlainEscaper func(string) string

func (r *TelegramRenderer) renderWith(ctx context.Context, text string, convert telegramHTMLConverter, escapePlain telegramPlainEscaper) ([]string, error) {
	parts := fixListNumbering(splitByDelimiter(text))

	var rawChunks []string
	for _, part := range parts {
		rawChunks = append(rawChunks, telegram.SplitMessageSmart(part, telegramMarkdownSafeLimit)...)
	}

	span := trace.SpanFromContext(ctx)
	resplits := 0

	var out []string
	for _, chunk := range rawChunks {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		out = append(out, r.renderChunk(span, chunk, telegramMarkdownSafeLimit, 0, &resplits, convert, escapePlain)...)
	}

	if resplits > 0 {
		r.logger.Warn("HTML chunks exceeded Telegram limit; re-split from source",
			"resplit_count", resplits)
		span.SetAttributes(
			attribute.Bool("bot.anomaly.chunk_resplit", true),
			attribute.Int("bot.anomaly.chunk_resplit_count", resplits),
		)
	}
	return out, nil
}

// renderChunk converts one markdown chunk to HTML. When the rendered HTML
// exceeds the wire limit, the source markdown is re-split with a budget scaled
// down by the observed expansion ratio and each piece is rendered again
// (bounded by maxRenderResplitDepth); past the depth cap the chunk ships as
// escaped plain text hard-split by UTF-16.
func (r *TelegramRenderer) renderChunk(
	span trace.Span,
	chunk string,
	mdLimit, depth int,
	resplits *int,
	convert telegramHTMLConverter,
	escapePlain telegramPlainEscaper,
) []string {
	htmlChunk, err := convert(chunk)
	if err != nil {
		r.logger.Warn("failed to convert markdown to HTML, using plain text", "error", err)
		return markdown.HardSplitUTF16(escapePlain(chunk), telegramMessageLimit)
	}

	utf16Len := markdown.UTF16Length(htmlChunk)
	if utf16Len <= telegramMessageLimit {
		return []string{htmlChunk}
	}

	*resplits++
	obs.RecordContent(span, "bot.anomaly.chunk_resplit_from", chunk,
		attribute.Int("utf16_length", utf16Len))

	if depth >= maxRenderResplitDepth {
		r.logger.Warn("re-split depth exhausted, sending escaped plain text",
			"utf16_length", utf16Len)
		return markdown.HardSplitUTF16(escapePlain(chunk), telegramMessageLimit)
	}

	// Scale the source budget by the observed expansion ratio with a 10%
	// safety margin; always strictly decrease so the recursion terminates.
	newLimit := mdLimit * telegramMessageLimit / utf16Len * 9 / 10
	if newLimit < 64 {
		newLimit = 64
	}
	if newLimit >= mdLimit {
		newLimit = mdLimit - 1
	}

	var out []string
	for _, sub := range telegram.SplitMessageSmart(chunk, newLimit) {
		if strings.TrimSpace(sub) == "" {
			continue
		}
		out = append(out, r.renderChunk(span, sub, newLimit, depth+1, resplits, convert, escapePlain)...)
	}
	return out
}

// incomingFromTelegram maps a Telegram message into the neutral envelope.
// Telegram is treated as DM-scoped (IsDirect=true) so the resolved scope id is
// always the sender id — byte-identical to the pre-seam grouping/storage key.
func (b *Bot) incomingFromTelegram(msg *telegram.Message) IncomingMessage {
	detectionText := msg.Text
	if detectionText == "" {
		detectionText = msg.Caption
	}
	text := b.projectTelegramEntityText(msg)
	userID := storage.PassthroughScopeID(transportTelegram, strconv.FormatInt(msg.From.ID, 10))
	legacyFiles := b.fileProcessor.ExtractFiles(msg, userID)
	allFiles := legacyFiles
	var ingress *IngressMetadata
	if msg.RichMessage != nil {
		richText, richFiles, richIngress := b.projectTelegramRich(
			msg,
			userID,
			hasLegacyTelegramContent(msg, legacyFiles),
		)
		text = mergeIncomingText(text, richText)
		detectionText = mergeIncomingText(detectionText, richText)
		allFiles = append(allFiles, richFiles...)
		richIngress.HasVisibleText = richIngress.HasVisibleText || msg.Text != "" || msg.Caption != ""
		ingress = richIngress
	}

	im := IncomingMessage{
		ConversationID: strconv.FormatInt(msg.Chat.ID, 10),
		SenderID:       strconv.FormatInt(msg.From.ID, 10),
		MessageID:      strconv.Itoa(msg.MessageID),
		Text:           text,
		DetectionText:  detectionText,
		SenderDisplay:  msg.From.Format(),
		Prefix:         msg.BuildPrefix(b.translator, b.cfg.Bot.Language),
		ThreadRoot:     threadRootFromTelegram(msg.MessageThreadID),
		IsDirect:       true,
		SentAt:         time.Unix(int64(msg.Date), 0),
		Files:          allFiles,
		Ingress:        ingress,
		RichEgressEligible: msg.Chat.Type == "private" &&
			msg.BusinessConnectionID == "" && msg.DirectMessagesTopic == nil,
	}

	if fo := msg.ForwardOrigin; fo != nil && fo.SenderUser != nil {
		s := fo.SenderUser
		im.Forward = &ForwardInfo{
			SenderID:  strconv.FormatInt(s.ID, 10),
			FirstName: s.FirstName,
			LastName:  s.LastName,
			Username:  s.Username,
			IsBot:     s.IsBot,
			IsUser:    true,
		}
	}
	return im
}

// incomingReactionFromTelegram maps a Telegram message_reaction update into the
// neutral reaction envelope. Telegram is DM-scoped (IsDirect=true), so the
// reacting user's id resolves to the same scope as their messages.
func incomingReactionFromTelegram(r *telegram.MessageReactionUpdated) IncomingReaction {
	ir := IncomingReaction{
		MessageID: strconv.Itoa(r.MessageID),
		OldEmojis: reactionEmojis(r.OldReaction),
		NewEmojis: reactionEmojis(r.NewReaction),
		IsDirect:  true,
	}
	if r.Chat != nil {
		ir.ConversationID = strconv.FormatInt(r.Chat.ID, 10)
	}
	if r.User != nil {
		ir.SenderID = strconv.FormatInt(r.User.ID, 10)
	}
	return ir
}

// threadRootFromTelegram stringifies a forum MessageThreadID, mapping 0 to ""
// (top-level) so the round-trip through OutgoingResponse.ThreadRoot is exact.
func threadRootFromTelegram(threadID int) string {
	if threadID == 0 {
		return ""
	}
	return strconv.Itoa(threadID)
}

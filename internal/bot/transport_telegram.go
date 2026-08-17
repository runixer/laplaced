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
	if r.Format == ResponseFormatRichHTML {
		return t.SendTextPersistent(ctx, r)
	}
	chatID, err := strconv.ParseInt(r.ConversationID, 10, 64)
	if err != nil {
		return "", err
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

// SendTextPersistent performs exactly one Bot API request and returns its
// stable ID. Delivery plans use it instead of SendText's compatibility
// retries, because a ledger operation must never conceal a second
// non-idempotent request.
func (t *TelegramTransport) SendTextPersistent(ctx context.Context, r OutgoingResponse) (string, error) {
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
	sent, sendErr := t.api.SendMessage(ctx, req)
	if sendErr != nil {
		return "", sendErr
	}
	if sent == nil || sent.MessageID <= 0 {
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
	generatedRichPhotoID       = "rich_photo_0"
	generatedRichPhotoAttachID = "rich_photo_0_file"
	generatedRichPhotoHTML     = `<img src="tg://photo?id=rich_photo_0"/>`
	generatedRichGalleryMax    = 10
	telegramRichPhotoMaxBytes  = 10 << 20
	telegramDocumentMaxBytes   = 50 << 20
)

func generatedRichGalleryHTML(media []telegram.InputRichMessageMedia) (string, error) {
	if len(media) == 0 || len(media) > generatedRichGalleryMax {
		return "", fmt.Errorf("rich photo gallery requires 1-%d photos, got %d", generatedRichGalleryMax, len(media))
	}
	if len(media) == 1 {
		return fmt.Sprintf(`<img src="tg://photo?id=%s"/>`, media[0].ID), nil
	}
	tag := "tg-collage"
	if len(media) >= 5 {
		tag = "tg-slideshow"
	}
	var html strings.Builder
	fmt.Fprintf(&html, "<%s>", tag)
	for _, item := range media {
		fmt.Fprintf(&html, `<img src="tg://photo?id=%s"/>`, item.ID)
	}
	fmt.Fprintf(&html, "</%s>", tag)
	return html.String(), nil
}

func outgoingRichMediaLayout(m OutgoingRichMedia) ([]string, []int, error) {
	if len(m.MediaGroupSizes) == 0 {
		return nil, nil, errors.New("rich media requires explicit topology with at least one media group")
	}
	if len(m.HTMLParts) != len(m.MediaGroupSizes)+1 {
		return nil, nil, fmt.Errorf("rich media topology has %d HTML parts for %d media groups; want %d",
			len(m.HTMLParts), len(m.MediaGroupSizes), len(m.MediaGroupSizes)+1)
	}
	total := 0
	for i, size := range m.MediaGroupSizes {
		if size < 1 || size > generatedRichGalleryMax {
			return nil, nil, fmt.Errorf("rich media group %d has %d items; want 1-%d", i, size, generatedRichGalleryMax)
		}
		total += size
	}
	if total != len(m.Items) {
		return nil, nil, fmt.Errorf("rich media topology consumes %d items, payload has %d", total, len(m.Items))
	}
	return m.HTMLParts, m.MediaGroupSizes, nil
}

type outgoingRichMediaComposition struct {
	html        string
	media       []telegram.InputRichMessageMedia
	attachments []telegram.RichMessageAttachment
}

// composeOutgoingRichMedia validates the full local request graph and assigns
// attachment/media IDs once across the flattened item list. Building each
// visual group separately would restart the ID sequence and create collisions.
func composeOutgoingRichMedia(m OutgoingRichMedia) (outgoingRichMediaComposition, error) {
	if len(m.Items) == 0 || len(m.Items) > telegram.MaxRichMessageMedia {
		return outgoingRichMediaComposition{}, fmt.Errorf("telegram rich media requires 1-%d photos, got %d",
			telegram.MaxRichMessageMedia, len(m.Items))
	}
	htmlParts, groupSizes, err := outgoingRichMediaLayout(m)
	if err != nil {
		return outgoingRichMediaComposition{}, err
	}

	uploads := make([]telegram.RichPhotoUpload, 0, len(m.Items))
	for i, item := range m.Items {
		if err := validatePersistentMediaItem(item); err != nil {
			return outgoingRichMediaComposition{}, fmt.Errorf("telegram rich media item %d: %w", i, err)
		}
		if item.WireKind != OutgoingMediaWireKindPhoto {
			return outgoingRichMediaComposition{}, fmt.Errorf("telegram rich media item %d must explicitly select photo wire kind", i)
		}
		if !strings.HasPrefix(persistentMediaType(item), "image/") || !generatedPhotoCanBePreviewed(item) {
			return outgoingRichMediaComposition{}, fmt.Errorf("telegram rich media item %d is outside the photo envelope", i)
		}
		filename := strings.TrimSpace(item.Filename)
		if filename == "" {
			filename = fmt.Sprintf("generated-%d.png", i+1)
		}
		uploads = append(uploads, telegram.RichPhotoUpload{Filename: filename, MIME: item.MIME, Data: item.Data})
	}

	media, attachments, err := telegram.BuildRichPhotoMedia(uploads)
	if err != nil {
		return outgoingRichMediaComposition{}, fmt.Errorf("build telegram rich photo media: %w", err)
	}
	var richHTML strings.Builder
	offset := 0
	for i, size := range groupSizes {
		richHTML.WriteString(htmlParts[i])
		galleryHTML, err := generatedRichGalleryHTML(media[offset : offset+size])
		if err != nil {
			return outgoingRichMediaComposition{}, err
		}
		richHTML.WriteString(galleryHTML)
		offset += size
	}
	richHTML.WriteString(htmlParts[len(htmlParts)-1])
	if richHTML.Len() > richMessageMaxRenderedBytes {
		return outgoingRichMediaComposition{}, fmt.Errorf("telegram rich media payload has %d bytes, limit is %d",
			richHTML.Len(), richMessageMaxRenderedBytes)
	}

	composition := outgoingRichMediaComposition{
		html:        richHTML.String(),
		media:       media,
		attachments: attachments,
	}
	if err := telegram.ValidateRichMessageRequest(telegram.SendRichMessageRequest{
		RichMessage: telegram.InputRichMessage{
			HTML:                composition.html,
			Media:               composition.media,
			SkipEntityDetection: true,
		},
		Attachments: composition.attachments,
	}); err != nil {
		return outgoingRichMediaComposition{}, fmt.Errorf("validate telegram rich media graph: %w", err)
	}
	return composition, nil
}

// SendRichMedia atomically uploads trusted generated-photo groups and
// persists them together with the complete Rich HTML response. One photo is a
// bare image block, 2-4 photos form a collage and 5-10 form a slideshow.
//
// The photo block is injected after model Markdown has passed the allowlisted
// Rich HTML renderer. Consequently model-authored URLs can never become media
// side effects; only the bytes resolved from GeneratedArtifactIDs arrive here.
func (t *TelegramTransport) SendRichMedia(ctx context.Context, m OutgoingRichMedia) (string, error) {
	if !t.cfg.Telegram.RichMessages.AnyEnabled() {
		return "", fmt.Errorf("telegram rich messages are disabled")
	}
	chatID, err := strconv.ParseInt(m.ConversationID, 10, 64)
	if err != nil {
		return "", err
	}
	composition, err := composeOutgoingRichMedia(m)
	if err != nil {
		return "", err
	}
	req := telegram.SendRichMessageRequest{
		ChatID:          chatID,
		MessageThreadID: intPtrOrNil(atoiOrZero(m.ThreadRoot)),
		RichMessage: telegram.InputRichMessage{
			HTML:                composition.html,
			Media:               composition.media,
			SkipEntityDetection: true,
		},
		Attachments: composition.attachments,
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
// preserves the legacy send_media_response.go classification and caption
// policy for zero-kind items: payloads over the configured document threshold
// (or forced via AsDocument) go as documents preserving resolution. An
// explicit WireKind always wins over those heuristics. Photo and document
// kinds can't share a media group, so they're sent as separate batches. Each
// homogeneous batch is additionally capped at Telegram's ten-item limit. The
// caption (rendered to HTML) rides the first batch; the reply-to anchors that
// caption-bearing batch only.
func (t *TelegramTransport) SendMedia(ctx context.Context, m OutgoingMedia) (string, error) {
	result, err := t.sendMediaCompatibility(ctx, m)
	return result.primaryMessageID(), err
}

// SendMediaPersistent executes exactly one Bot API media call and returns all
// stable IDs from that call. V2 planners must pre-split mixed photo/document
// sets and batches above Telegram's group limit before entering the ledger's
// non-idempotent sending state.
func (t *TelegramTransport) SendMediaPersistent(ctx context.Context, m OutgoingMedia) (persistentSendResult, error) {
	chatID, err := strconv.ParseInt(m.ConversationID, 10, 64)
	if err != nil {
		return persistentSendResult{}, err
	}
	threshold := t.cfg.Agents.ImageGenerator.DocumentThresholdBytes
	wireKind, err := validateTelegramPersistentMediaBatch(m.Items, threshold)
	if err != nil {
		return persistentSendResult{}, fmt.Errorf("persistent Telegram media operation %w", err)
	}
	thread := intPtrOrNil(atoiOrZero(m.ThreadRoot))
	replyTo := atoiOrZero(m.ReplyTo)
	parseMode := ""
	if m.Caption != "" {
		parseMode = "HTML"
	}
	var ids []string
	if wireKind == OutgoingMediaWireKindDocument {
		ids, err = t.sendItemsAsDocuments(ctx, chatID, thread, replyTo, m.Items, m.Caption, parseMode)
	} else {
		ids, err = t.sendItemsAsPhotos(ctx, chatID, thread, replyTo, m.Items, m.Caption, parseMode)
	}
	return persistentSendResult{MessageIDs: ids}, err
}

// sendMediaCompatibility preserves the established broad Transport.SendMedia
// behavior for non-V2 callers. It may use several document/photo calls (each
// album is capped at ten) and therefore must never be used as one durable
// ledger operation.
func (t *TelegramTransport) sendMediaCompatibility(ctx context.Context, m OutgoingMedia) (persistentSendResult, error) {
	chatID, err := strconv.ParseInt(m.ConversationID, 10, 64)
	if err != nil {
		return persistentSendResult{}, err
	}
	if len(m.Items) == 0 {
		return persistentSendResult{}, fmt.Errorf("telegram media delivery requires at least one item")
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
		switch resolvedTelegramMediaWireKind(it, threshold) {
		case OutgoingMediaWireKindDocument:
			docBatch = append(docBatch, it)
		case OutgoingMediaWireKindPhoto:
			photoBatch = append(photoBatch, it)
		default:
			return persistentSendResult{}, fmt.Errorf("telegram media item has unsupported wire kind %q", it.WireKind)
		}
	}

	var messageIDs []string
	sendBatches := func(items []OutgoingMediaItem, documents bool) error {
		for len(items) > 0 {
			size := min(10, len(items))
			batch := items[:size]
			items = items[size:]
			batchCaption, batchParseMode, batchReplyTo := "", "", 0
			if len(messageIDs) == 0 {
				batchCaption, batchParseMode, batchReplyTo = caption, parseMode, replyTo
			}
			var ids []string
			var sendErr error
			if documents {
				ids, sendErr = t.sendItemsAsDocuments(ctx, chatID, thread, batchReplyTo, batch, batchCaption, batchParseMode)
			} else {
				ids, sendErr = t.sendItemsAsPhotos(ctx, chatID, thread, batchReplyTo, batch, batchCaption, batchParseMode)
			}
			messageIDs = append(messageIDs, ids...)
			if sendErr != nil {
				return sendErr
			}
		}
		return nil
	}
	// Documents stay first so the original-quality result owns caption/reply.
	// Once any batch confirms, all later batches are unanchored.
	if sendErr := sendBatches(docBatch, true); sendErr != nil {
		return persistentSendResult{MessageIDs: messageIDs}, sendErr
	}
	if sendErr := sendBatches(photoBatch, false); sendErr != nil {
		return persistentSendResult{MessageIDs: messageIDs}, sendErr
	}
	if len(messageIDs) == 0 {
		return persistentSendResult{}, fmt.Errorf("telegram media delivery returned no stable message id")
	}
	return persistentSendResult{MessageIDs: messageIDs}, nil
}

// sendItemsAsPhotos sends a batch as sendPhoto (1 item) or sendMediaGroup (2+),
// returning every resulting message id.
func (t *TelegramTransport) sendItemsAsPhotos(ctx context.Context, chatID int64, thread *int, replyTo int, batch []OutgoingMediaItem, caption, parseMode string) ([]string, error) {
	if len(batch) == 0 {
		return nil, nil
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
			return nil, err
		}
		id, confirmErr := confirmedTelegramMessageID(sent, "sendPhoto")
		if confirmErr != nil {
			return nil, confirmErr
		}
		return []string{id}, nil
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
		return nil, err
	}
	return confirmedTelegramMediaGroupIDs(sent, len(batch), "sendMediaGroup")
}

// sendItemsAsDocuments sends a batch as sendDocument (1 item) or
// sendMediaGroup-of-documents (2+), returning every resulting message id.
func (t *TelegramTransport) sendItemsAsDocuments(ctx context.Context, chatID int64, thread *int, replyTo int, batch []OutgoingMediaItem, caption, parseMode string) ([]string, error) {
	if len(batch) == 0 {
		return nil, nil
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
			return nil, err
		}
		id, confirmErr := confirmedTelegramMessageID(sent, "sendDocument")
		if confirmErr != nil {
			return nil, confirmErr
		}
		return []string{id}, nil
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
		return nil, err
	}
	return confirmedTelegramMediaGroupIDs(sent, len(batch), "sendMediaGroup(documents)")
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

func confirmedTelegramMediaGroupIDs(messages []telegram.Message, expected int, method string) ([]string, error) {
	if len(messages) != expected {
		return nil, fmt.Errorf("%s returned %d messages, expected %d", method, len(messages), expected)
	}
	ids := make([]string, 0, len(messages))
	seen := make(map[int]struct{}, len(messages))
	for i, message := range messages {
		if message.MessageID <= 0 {
			return nil, fmt.Errorf("%s returned invalid message_id %d at index %d", method, message.MessageID, i)
		}
		if _, ok := seen[message.MessageID]; ok {
			return nil, fmt.Errorf("%s returned duplicate message_id %d at index %d", method, message.MessageID, i)
		}
		seen[message.MessageID] = struct{}{}
		ids = append(ids, strconv.Itoa(message.MessageID))
	}
	return ids, nil
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

// renderSafeRichFallbackPart renders one source part whose application-level
// split boundaries were already resolved by the Rich Message packer. It must
// not reinterpret an inline ###SPLIT### token as a second protocol boundary.
func (r *TelegramRenderer) renderSafeRichFallbackPart(ctx context.Context, text string) ([]string, error) {
	return r.renderWithParts(ctx, []string{text}, markdown.ToSafeLegacyHTML, markdown.SafeLegacyPlainText)
}

type telegramHTMLConverter func(string) (string, error)
type telegramPlainEscaper func(string) string

func (r *TelegramRenderer) renderWith(ctx context.Context, text string, convert telegramHTMLConverter, escapePlain telegramPlainEscaper) ([]string, error) {
	parts := fixListNumbering(splitByDelimiter(text))
	return r.renderWithParts(ctx, parts, convert, escapePlain)
}

func (r *TelegramRenderer) renderWithParts(ctx context.Context, parts []string, convert telegramHTMLConverter, escapePlain telegramPlainEscaper) ([]string, error) {
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

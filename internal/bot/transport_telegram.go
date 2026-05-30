package bot

import (
	"context"
	"html"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/i18n"
	"github.com/runixer/laplaced/internal/markdown"
	"github.com/runixer/laplaced/internal/telegram"
)

// telegramMarkdownSafeLimit is the per-chunk character budget before HTML
// conversion (safety margin under the 4096 UTF-16 wire limit).
const telegramMarkdownSafeLimit = 3500

// TelegramTransport adapts the Telegram Bot API to the neutral Transport
// interface. It wraps the same telegram.BotAPI the bot already holds, so the
// home send path is byte-identical to the pre-seam code.
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
	return Capabilities{
		MaxMessageLen:         telegramMessageLimit,
		ParseMode:             "HTML",
		SupportsLatex:         false, // converted to unicode by the renderer
		SupportsStreaming:     true,
		SupportsReactions:     true,
		SupportsMedia:         true,
		MaxMediaItemsPerGroup: 10, // Telegram album limit
		MaxMediaCaptionLen:    telegramCaptionLimit,
		EmojiStyle:            "unicode",
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

// SendText sends one rendered HTML chunk. It preserves the legacy
// "can't parse entities" recovery: on a parse error it retries once as plain
// text (ParseMode cleared) using a fresh, non-cancellable context.
func (t *TelegramTransport) SendText(ctx context.Context, r OutgoingResponse) (string, error) {
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
	if err != nil && strings.Contains(err.Error(), "can't parse entities") {
		t.logger.Warn("retrying send without HTML parse mode due to parsing error")
		retryCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		req.ParseMode = ""
		sent, err = t.api.SendMessage(retryCtx, req)
	}
	if err != nil {
		return "", err
	}
	if sent != nil {
		return strconv.Itoa(sent.MessageID), nil
	}
	return "", nil
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

// SetReaction adds a random allowed emoji reaction to the message.
func (t *TelegramTransport) SetReaction(ctx context.Context, conversationID, messageID string) error {
	chatID, err := strconv.ParseInt(conversationID, 10, 64)
	if err != nil {
		return err
	}
	msgID, err := strconv.Atoi(messageID)
	if err != nil {
		return err
	}
	// #nosec G404 -- emoji reactions are a UX flourish, not a security primitive
	emoji := availableReactions[rand.IntN(len(availableReactions))]
	return t.api.SetMessageReaction(ctx, telegram.SetMessageReactionRequest{
		ChatID:    chatID,
		MessageID: msgID,
		Reaction:  []telegram.ReactionType{{Type: "emoji", Emoji: emoji}},
	})
}

// telegramCaptionLimit is the Telegram caption budget for sendPhoto /
// sendMediaGroup items (rune proxy for the 1024 UTF-16 limit). We stay under to
// leave room for emoji and HTML entities; overflow is sent as follow-up text.
const telegramCaptionLimit = 1000

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
		return "", nil
	}
	thread := intPtrOrNil(atoiOrZero(m.ThreadRoot))
	replyTo := atoiOrZero(m.ReplyTo)

	caption, parseMode := renderCaptionHTML(m.Caption, t.logger)

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
		id := t.sendItemsAsDocuments(ctx, chatID, thread, rt, docBatch, c, pm)
		if firstMsgID == "" {
			firstMsgID = id
		}
	}
	if len(photoBatch) > 0 {
		c, pm, rt := "", "", replyTo
		if captionOwnerIsDoc {
			rt = 0 // caption + reply-to already went with the documents
		} else {
			c, pm = caption, parseMode
		}
		id := t.sendItemsAsPhotos(ctx, chatID, thread, rt, photoBatch, c, pm)
		if firstMsgID == "" {
			firstMsgID = id
		}
	}
	return firstMsgID, nil
}

// sendItemsAsPhotos sends a batch as sendPhoto (1 item) or sendMediaGroup (2+),
// returning the first resulting message id.
func (t *TelegramTransport) sendItemsAsPhotos(ctx context.Context, chatID int64, thread *int, replyTo int, batch []OutgoingMediaItem, caption, parseMode string) string {
	if len(batch) == 0 {
		return ""
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
			return ""
		}
		return messageIDOrEmpty(sent)
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
		return ""
	}
	if len(sent) > 0 {
		return strconv.Itoa(sent[0].MessageID)
	}
	return ""
}

// sendItemsAsDocuments sends a batch as sendDocument (1 item) or
// sendMediaGroup-of-documents (2+), returning the first resulting message id.
func (t *TelegramTransport) sendItemsAsDocuments(ctx context.Context, chatID int64, thread *int, replyTo int, batch []OutgoingMediaItem, caption, parseMode string) string {
	if len(batch) == 0 {
		return ""
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
			return ""
		}
		return messageIDOrEmpty(sent)
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
		return ""
	}
	if len(sent) > 0 {
		return strconv.Itoa(sent[0].MessageID)
	}
	return ""
}

// messageIDOrEmpty stringifies a sent message's id, or returns "" for nil.
func messageIDOrEmpty(m *telegram.Message) string {
	if m == nil {
		return ""
	}
	return strconv.Itoa(m.MessageID)
}

// renderCaptionHTML converts a markdown caption to HTML with a caption-size
// budget check. If the HTML render exceeds Telegram's 1024 UTF-16 char limit, or
// markdown.ToHTML fails, it falls back to the raw plain-text caption (Telegram
// accepts any text when parse_mode is unset). Returns the caption and parse_mode.
func renderCaptionHTML(plain string, logger *slog.Logger) (string, string) {
	if plain == "" {
		return "", ""
	}
	htmlStr, err := markdown.ToHTML(plain)
	if err != nil {
		logger.Warn("caption markdown → HTML failed, falling back to plain", "err", err)
		return plain, ""
	}
	if markdown.UTF16Length(htmlStr) > 1024 {
		logger.Warn("HTML caption over 1024 chars, falling back to plain text",
			"utf16_len", markdown.UTF16Length(htmlStr))
		return plain, ""
	}
	return htmlStr, "HTML"
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

// TelegramRenderer reproduces the legacy finalizeResponse pipeline: split on the
// ###SPLIT### delimiter, fix list numbering across parts, hard-split oversized
// parts, then convert each chunk markdown -> HTML (falling back to escaped
// plain text on conversion error).
type TelegramRenderer struct {
	logger *slog.Logger
}

// NewTelegramRenderer builds the Telegram HTML renderer.
func NewTelegramRenderer(logger *slog.Logger) *TelegramRenderer {
	return &TelegramRenderer{logger: logger}
}

func (r *TelegramRenderer) Render(text string) ([]string, error) {
	parts := fixListNumbering(splitByDelimiter(text))

	var rawChunks []string
	for _, part := range parts {
		rawChunks = append(rawChunks, telegram.SplitMessageSmart(part, telegramMarkdownSafeLimit)...)
	}

	var out []string
	for _, chunk := range rawChunks {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		htmlChunk, err := markdown.ToHTML(chunk)
		if err != nil {
			r.logger.Warn("failed to convert markdown to HTML, using plain text", "error", err)
			htmlChunk = html.EscapeString(chunk)
		}
		if markdown.UTF16Length(htmlChunk) > telegramMessageLimit {
			r.logger.Warn("HTML chunk exceeds Telegram limit after conversion",
				"utf16_length", markdown.UTF16Length(htmlChunk), "limit", telegramMessageLimit)
		}
		out = append(out, htmlChunk)
	}
	return out, nil
}

// incomingFromTelegram maps a Telegram message into the neutral envelope.
// Telegram is treated as DM-scoped (IsDirect=true) so the resolved scope id is
// always the sender id — byte-identical to the pre-seam grouping/storage key.
func (b *Bot) incomingFromTelegram(msg *telegram.Message) IncomingMessage {
	text := msg.Text
	if text == "" {
		text = msg.Caption
	}

	im := IncomingMessage{
		ConversationID: strconv.FormatInt(msg.Chat.ID, 10),
		SenderID:       strconv.FormatInt(msg.From.ID, 10),
		MessageID:      strconv.Itoa(msg.MessageID),
		Text:           text,
		SenderDisplay:  msg.From.Format(),
		Prefix:         msg.BuildPrefix(b.translator, b.cfg.Bot.Language),
		ThreadRoot:     threadRootFromTelegram(msg.MessageThreadID),
		IsDirect:       true,
		SentAt:         time.Unix(int64(msg.Date), 0),
		Files:          b.fileProcessor.ExtractFiles(msg, msg.From.ID),
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

// threadRootFromTelegram stringifies a forum MessageThreadID, mapping 0 to ""
// (top-level) so the round-trip through OutgoingResponse.ThreadRoot is exact.
func threadRootFromTelegram(threadID int) string {
	if threadID == 0 {
		return ""
	}
	return strconv.Itoa(threadID)
}

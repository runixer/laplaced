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
		MaxMessageLen:     telegramMessageLimit,
		ParseMode:         "HTML",
		SupportsLatex:     false, // converted to unicode by the renderer
		SupportsStreaming: true,
		SupportsReactions: true,
		EmojiStyle:        "unicode",
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

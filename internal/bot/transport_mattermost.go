package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"math/rand/v2"
	"strings"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/mattermost"
	"github.com/runixer/laplaced/internal/telegram"
)

// mmMarkdownSafeMargin is subtracted from the server's MaxPostSize to leave room
// for any per-post overhead, mirroring the Telegram renderer's safety margin.
const mmMarkdownSafeMargin = 200

// mmReactions are the emoji shortcodes the bot may react with (Mattermost takes
// names, not unicode). MM analog of availableReactions.
var mmReactions = []string{"eyes", "thinking_face", "white_check_mark", "+1", "fire", "raised_hands"}

// MMTransport adapts the Mattermost/Time client to the neutral Transport
// interface. Output-only: ingestion runs via startMattermostIngestion.
type MMTransport struct {
	client *mattermost.Client
	cfg    *config.Config
	logger *slog.Logger
}

// NewMattermostTransport builds the Mattermost output adapter.
func NewMattermostTransport(client *mattermost.Client, cfg *config.Config, logger *slog.Logger) *MMTransport {
	return &MMTransport{client: client, cfg: cfg, logger: logger.With("component", "mm-transport")}
}

func (t *MMTransport) Kind() string { return transportTime }

func (t *MMTransport) Capabilities() Capabilities {
	return Capabilities{
		MaxMessageLen:     t.client.MaxPostSize(),
		ParseMode:         "", // native markdown — no HTML conversion
		SupportsLatex:     true,
		SupportsStreaming: false,
		SupportsReactions: true,
		EmojiStyle:        "shortcode",
	}
}

// IsAllowed checks the native Mattermost sender id (26-char string) against the
// configured allowlist. Empty allowlist rejects everyone (fail closed).
func (t *MMTransport) IsAllowed(nativeSenderID string) bool {
	for _, allowed := range t.cfg.Mattermost.AllowedUserIDs {
		if allowed == nativeSenderID {
			return true
		}
	}
	return false
}

// SendText posts one rendered markdown chunk. It threads under ThreadRoot (or
// anchors to ReplyTo when ThreadRoot is empty) and sends a deterministic
// idempotency key so a retried send is collapsed server-side.
func (t *MMTransport) SendText(ctx context.Context, r OutgoingResponse) (string, error) {
	rootID := r.ThreadRoot
	if rootID == "" && r.ReplyTo != "" {
		rootID = r.ReplyTo
	}
	post, err := t.client.CreatePost(ctx, mattermost.CreatePostReq{
		ChannelID:      r.ConversationID,
		Message:        r.Text,
		RootID:         rootID,
		IdempotencyKey: mmIdempotencyKey(r.ConversationID, rootID, r.Text),
	})
	if err != nil {
		return "", err
	}
	return post.ID, nil
}

func (t *MMTransport) SendTyping(ctx context.Context, conversationID string) error {
	return t.client.SendTyping(ctx, conversationID)
}

// SetReaction adds a random allowed shortcode reaction to the post.
func (t *MMTransport) SetReaction(ctx context.Context, _ /* conversationID */, messageID string) error {
	// #nosec G404 -- emoji reactions are a UX flourish, not a security primitive
	emoji := mmReactions[rand.IntN(len(mmReactions))]
	return t.client.SetReaction(ctx, messageID, emoji)
}

// mmIdempotencyKey derives a stable key for a (channel, thread, text) tuple so a
// retried send within the server's 30s TTL is deduped instead of double-posted.
func mmIdempotencyKey(channelID, rootID, text string) string {
	sum := sha256.Sum256([]byte(channelID + "\x00" + rootID + "\x00" + text))
	return hex.EncodeToString(sum[:16])
}

// MMRenderer renders canonical markdown for Mattermost: split on the ###SPLIT###
// delimiter, fix list numbering, and hard-split oversized parts at MaxPostSize.
// Markdown is passed through unchanged — MM renders it natively (no HTML, no
// latex->unicode conversion).
type MMRenderer struct {
	maxLen int
	logger *slog.Logger
}

// NewMattermostRenderer builds the markdown pass-through renderer. maxPostSize is
// the server's limit; a safety margin is subtracted internally.
func NewMattermostRenderer(maxPostSize int, logger *slog.Logger) *MMRenderer {
	limit := maxPostSize - mmMarkdownSafeMargin
	if limit < 1 {
		limit = maxPostSize
	}
	return &MMRenderer{maxLen: limit, logger: logger.With("component", "mm-renderer")}
}

func (r *MMRenderer) Render(text string) ([]string, error) {
	parts := fixListNumbering(splitByDelimiter(text))

	var out []string
	for _, part := range parts {
		for _, chunk := range telegram.SplitMessageSmart(part, r.maxLen) {
			if strings.TrimSpace(chunk) == "" {
				continue
			}
			out = append(out, chunk)
		}
	}
	return out, nil
}

// incomingFromMattermost maps a parsed "posted" event into the neutral envelope.
// DMs (channel_type "D") are IsDirect so the scope resolves to the sender; the
// reply threads under the user's message (top-level posts thread on their own id).
func (b *Bot) incomingFromMattermost(ev mattermost.PostedEvent) IncomingMessage {
	threadRoot := ev.Post.RootID
	if threadRoot == "" {
		threadRoot = ev.Post.ID
	}
	return IncomingMessage{
		ConversationID: ev.Post.ChannelID,
		SenderID:       ev.Post.UserID,
		MessageID:      ev.Post.ID,
		Text:           ev.Post.Message,
		SenderDisplay:  ev.Post.UserID,
		ThreadRoot:     threadRoot,
		IsDirect:       ev.ChannelType == "D",
		Files:          nil,
	}
}

// StartMattermostIngestion consumes "posted" events from the client and feeds the
// allowed ones into the neutral pipeline. It returns when the client closes its
// events channel (on ctx cancellation), making shutdown deterministic.
//
// The allowlist gate lives here because HandleIncoming does not check it — the
// Telegram path gates earlier in ProcessUpdate.
func (b *Bot) StartMattermostIngestion(client *mattermost.Client) {
	botID := client.BotID()
	for ev := range client.Events() {
		b.logger.Debug("mm ingestion event",
			"post_id", ev.Post.ID, "user_id", ev.Post.UserID, "type", ev.Post.Type,
			"channel_type", ev.ChannelType, "channel_id", ev.Post.ChannelID)
		if !mmShouldProcess(ev.Post, botID, b.transport.IsAllowed) {
			b.logger.Debug("mm ingestion: post filtered out", "post_id", ev.Post.ID, "sender_id", ev.Post.UserID)
			continue
		}
		b.HandleIncoming(b.incomingFromMattermost(ev))
	}
}

// mmShouldProcess decides whether an incoming post should enter the pipeline:
// skip the bot's own posts, skip system posts (non-empty type), and enforce the
// allowlist (HandleIncoming itself does not gate).
func mmShouldProcess(post mattermost.Post, botID string, isAllowed func(string) bool) bool {
	if post.UserID == botID {
		return false
	}
	if post.Type != "" {
		return false
	}
	return isAllowed(post.UserID)
}

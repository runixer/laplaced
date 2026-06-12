package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/files"
	"github.com/runixer/laplaced/internal/mattermost"
	"github.com/runixer/laplaced/internal/telegram"
)

// mmMarkdownSafeMargin is subtracted from the server's MaxPostSize to leave room
// for any per-post overhead, mirroring the Telegram renderer's safety margin.
const mmMarkdownSafeMargin = 200

// mmMaxFilesPerPost is the Mattermost limit of attachments per post; batches
// larger than this are split across multiple posts.
const mmMaxFilesPerPost = 5

// mmReactions are the emoji shortcodes the bot may react with (Mattermost takes
// names, not unicode). Curated subset of the standard emoji set; MM analog of
// telegramReactionEmoji. Entries must render on the target server — prune any
// that 400 there.
var mmReactions = []string{
	"+1", "-1", "heart", "fire", "joy", "rofl", "smile", "sweat_smile",
	"thinking_face", "eyes", "tada", "clap", "scream", "cry", "sob",
	"pray", "ok_hand", "100", "muscle", "rocket", "white_check_mark",
	"raised_hands", "smirk", "grimacing", "exploding_head", "handshake",
	"brain", "coffee", "wave", "point_up", "heart_eyes", "sleeping",
	"face_palm", "shrug", "trophy", "zap",
}

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

func (t *MMTransport) Kind() string { return transportMattermost }

func (t *MMTransport) Capabilities() Capabilities {
	maxPost := t.client.MaxPostSize()
	captionLen := maxPost - mmMarkdownSafeMargin
	if captionLen < 1 {
		captionLen = maxPost
	}
	return Capabilities{
		MaxMessageLen:         maxPost,
		ParseMode:             "",   // native markdown — no HTML conversion
		SupportsLatex:         true, // Time renders LaTeX natively via KaTeX (the prompt steers the model to clean $…$)
		SupportsStreaming:     false,
		SupportsReactions:     true,
		SupportsMedia:         true,
		MaxMediaItemsPerGroup: mmMaxFilesPerPost,
		MaxMediaCaptionLen:    captionLen,
		EmojiStyle:            "shortcode",
		AvailableReactions:    mmReactions,
	}
}

// IsAllowed checks the native Mattermost sender id (26-char string) against the
// configured allowlist. Empty allowlist rejects everyone (fail closed) — in SSO
// mode the caller treats an unconfigured allowlist as "all trusted senders"
// instead, see Bot.authorizeSender.
func (t *MMTransport) IsAllowed(nativeSenderID string) bool {
	for _, allowed := range t.cfg.Mattermost.AllowedUserIDs {
		if allowed == nativeSenderID {
			return true
		}
	}
	return false
}

func (t *MMTransport) AllowlistConfigured() bool {
	return len(t.cfg.Mattermost.AllowedUserIDs) > 0
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

// SendMedia uploads each item to the channel and posts them with the caption.
// Mattermost caps attachments at mmMaxFilesPerPost per post, so larger batches
// are split across posts; the caption rides the first post only. The post
// threads under ThreadRoot (or ReplyTo when ThreadRoot is empty), mirroring
// SendText. Markdown is passed through (MM renders it natively).
func (t *MMTransport) SendMedia(ctx context.Context, m OutgoingMedia) (string, error) {
	if len(m.Items) == 0 {
		return "", nil
	}
	rootID := m.ThreadRoot
	if rootID == "" && m.ReplyTo != "" {
		rootID = m.ReplyTo
	}

	// Upload every item up front; skip individual failures so a partial batch
	// still posts.
	fileIDs := make([]string, 0, len(m.Items))
	for _, it := range m.Items {
		id, err := t.client.UploadFile(ctx, m.ConversationID, it.Filename, it.MIME, it.Data)
		if err != nil {
			t.logger.Error("mm upload file failed", "filename", it.Filename, "error", err)
			continue
		}
		fileIDs = append(fileIDs, id)
	}
	if len(fileIDs) == 0 {
		return "", fmt.Errorf("mattermost: no files uploaded")
	}

	var firstPostID string
	for i := 0; i < len(fileIDs); i += mmMaxFilesPerPost {
		end := i + mmMaxFilesPerPost
		if end > len(fileIDs) {
			end = len(fileIDs)
		}
		chunk := fileIDs[i:end]
		message := ""
		if i == 0 {
			message = m.Caption // caption on the first post only
		}
		post, err := t.client.CreatePost(ctx, mattermost.CreatePostReq{
			ChannelID:      m.ConversationID,
			Message:        message,
			RootID:         rootID,
			FileIDs:        chunk,
			IdempotencyKey: mmIdempotencyKey(m.ConversationID, rootID, message+strings.Join(chunk, ",")),
		})
		if err != nil {
			t.logger.Error("mm create media post failed", "error", err)
			if firstPostID == "" {
				return "", err
			}
			return firstPostID, err
		}
		if firstPostID == "" {
			firstPostID = post.ID
		}
	}
	return firstPostID, nil
}

func (t *MMTransport) SendTyping(ctx context.Context, conversationID string) error {
	return t.client.SendTyping(ctx, conversationID)
}

// SetReaction adds the given shortcode reaction to the post. The emoji must be
// one of mmReactions.
func (t *MMTransport) SetReaction(ctx context.Context, _ /* conversationID */, messageID, emoji string) error {
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
// Markdown (incl. LaTeX math) is passed through unchanged — Time renders it
// natively via KaTeX; the system prompt steers the model toward clean $…$.
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

func (r *MMRenderer) Render(_ context.Context, text string) ([]string, error) {
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
// It resolves the sender's display name (cached) so the model sees who it talks
// to via Prefix, mirroring the Telegram "[Name (@handle) (time)]" format.
func (b *Bot) incomingFromMattermost(client *mattermost.Client, ev mattermost.PostedEvent) IncomingMessage {
	threadRoot := ev.Post.RootID
	if threadRoot == "" {
		threadRoot = ev.Post.ID
	}

	sentAt := time.UnixMilli(ev.Post.CreateAt)
	display := ev.Post.UserID
	if u, err := client.GetUser(context.Background(), ev.Post.UserID); err == nil {
		display = mmFormatUser(u, ev.Post.UserID)
	} else {
		b.logger.Warn("failed to fetch mm user for prefix", "user_id", ev.Post.UserID, "error", err)
	}

	// For a channel post, resolve the channel's display name so the channel scope
	// is labeled by the channel (not by the last poster) in the dashboard. DMs
	// have no meaningful channel name — the scope is the sender.
	convDisplay := ""
	if ev.ChannelType != "D" {
		if ch, err := client.GetChannel(context.Background(), ev.Post.ChannelID); err == nil {
			convDisplay = ch.DisplayName
			if convDisplay == "" {
				convDisplay = ch.Name
			}
		} else {
			b.logger.Warn("failed to fetch mm channel for scope label", "channel_id", ev.Post.ChannelID, "error", err)
		}
	}

	return IncomingMessage{
		ConversationID:      ev.Post.ChannelID,
		SenderID:            ev.Post.UserID,
		MessageID:           ev.Post.ID,
		Text:                ev.Post.Message,
		SenderDisplay:       display,
		ConversationDisplay: convDisplay,
		Prefix:              fmt.Sprintf("[%s (%s)]", display, mmFormatTime(ev.Post.CreateAt)),
		ThreadRoot:          threadRoot,
		IsDirect:            ev.ChannelType == "D",
		// Channel reply-gating: the bot replies when @mentioned or when the post
		// replies to (quotes) one of the bot's own messages. A plain post in a
		// thread the bot merely spoke in does NOT trigger a reply.
		Mention:    slices.Contains(ev.Mentions, client.BotID()),
		ReplyToBot: ev.Post.QuotedAuthorID() == client.BotID(),
		SentAt:     sentAt,
		Files:      nil,
	}
}

// mmFormatUser renders a Mattermost user as "First Last (@username)", falling
// back through nickname, @username, and finally the raw id — mirroring the
// Telegram User.Format shape so both transports read identically to the model.
func mmFormatUser(u *mattermost.User, fallbackID string) string {
	if u == nil {
		return fallbackID
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = strings.TrimSpace(u.Nickname)
	}
	if u.Username != "" {
		if name != "" {
			name = fmt.Sprintf("%s (@%s)", name, u.Username)
		} else {
			name = "@" + u.Username
		}
	}
	if name == "" {
		name = fallbackID
	}
	return name
}

// mmFormatTime formats a Mattermost epoch-millis timestamp like the Telegram
// prefix does, mapping 0 to a neutral label.
func mmFormatTime(createAtMillis int64) string {
	if createAtMillis == 0 {
		return "unknown time"
	}
	return time.UnixMilli(createAtMillis).Format("2006-01-02 15:04:05")
}

// StartMattermostIngestion consumes "posted" events from the client and feeds
// them into the neutral pipeline. It returns when the client closes its events
// channel (on ctx cancellation), making shutdown deterministic.
//
// mmShouldProcess drops the bot's own/system posts. Authorization (the SSO/
// allowlist access gate) and channel reply-gating are owned by HandleIncoming,
// so both DMs and channel posts pass through here.
func (b *Bot) StartMattermostIngestion(client *mattermost.Client) {
	botID := client.BotID()
	for ev := range client.Events() {
		b.logger.Debug("mm ingestion event",
			"post_id", ev.Post.ID, "user_id", ev.Post.UserID, "type", ev.Post.Type,
			"channel_type", ev.ChannelType, "channel_id", ev.Post.ChannelID)
		if !mmShouldProcess(ev.Post, botID) {
			b.logger.Debug("mm ingestion: post filtered out", "post_id", ev.Post.ID, "sender_id", ev.Post.UserID)
			continue
		}
		im := b.incomingFromMattermost(client, ev)
		im.Files = b.extractMattermostFiles(client, ev.Post)
		b.HandleIncoming(im)
	}
}

// extractMattermostFiles maps a post's image/video attachments to neutral
// IncomingFiles whose Fetch lazily downloads the bytes via the MM client. It
// prefers the FileInfo embedded in the post metadata (no extra round-trip) and
// falls back to GET /files/{id}/info when metadata is absent. Non-image/-video
// attachments are skipped (voice/PDF are off on this contour).
func (b *Bot) extractMattermostFiles(client *mattermost.Client, post mattermost.Post) []files.IncomingFile {
	infos := post.Metadata.Files
	if len(infos) == 0 {
		for _, id := range post.FileIDs {
			fi, err := client.GetFileInfo(context.Background(), id)
			if err != nil {
				b.logger.Warn("failed to fetch mm file info", "file_id", id, "error", err)
				continue
			}
			infos = append(infos, *fi)
		}
	}

	var out []files.IncomingFile
	for _, fi := range infos {
		kind, ok := mmFileKind(fi.MimeType)
		if !ok {
			b.logger.Info("skipping unsupported mm attachment", "file_id", fi.ID, "name", fi.Name, "mime", fi.MimeType)
			continue
		}
		id := fi.ID
		out = append(out, files.IncomingFile{
			Kind:     kind,
			SourceID: id,
			FileName: fi.Name,
			MIME:     fi.MimeType,
			Size:     fi.Size,
			Fetch:    func(ctx context.Context) ([]byte, error) { return client.GetFile(ctx, id) },
		})
	}
	return out
}

// mmFileKind maps a MIME type to the neutral FileType the pipeline understands.
// v0.10 inbound scope is visual media (image/video); other kinds are skipped.
func mmFileKind(mime string) (files.FileType, bool) {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return files.FileTypeImage, true
	case strings.HasPrefix(mime, "video/"):
		return files.FileTypeVideo, true
	default:
		return "", false
	}
}

// mmShouldProcess decides whether an incoming post should enter the pipeline:
// skip only the bot's own posts and system posts (non-empty type). Everything
// else — DMs and channel posts alike — passes through to HandleIncoming, which
// owns the access gate (SSO trust / allowlist via authorizeSender) and channel
// reply-gating. Deferring the DM gate there lets a denied sender be told why,
// instead of being silently dropped at ingestion.
func mmShouldProcess(post mattermost.Post, botID string) bool {
	if post.UserID == botID {
		return false
	}
	return post.Type == ""
}

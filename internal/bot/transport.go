package bot

import (
	"context"
	"time"

	"github.com/runixer/laplaced/internal/files"
)

// IncomingMessage is the transport-neutral envelope that flows through the
// message grouper and processing pipeline. Telegram and Mattermost/Time each
// map their native update into this shape at the ingestion boundary; the core
// never sees a transport-specific message type.
type IncomingMessage struct {
	ConversationID      string // Telegram chat.ID (stringified) | Mattermost channel_id
	SenderID            string // Telegram From.ID (stringified) | Mattermost 26-char user id
	MessageID           string // transport message/post id
	Text                string // user text (Telegram Text or Caption, merged)
	SenderDisplay       string // human-readable sender ("Name (@handle)") for logs
	ConversationDisplay string // human-readable channel name (channel scopes); "" for DMs/Telegram
	Prefix              string // pre-built display prefix ("[Name (time)]" or forwarded-from)
	ThreadRoot          string // Telegram MessageThreadID (forum) | Mattermost root_id; "" = top level
	IsDirect            bool   // DM (Telegram private chat | Mattermost channel_type==D)
	Mention             bool   // bot mentioned (for channels); always acted on in DMs
	ReplyToBot          bool   // message replies to / quotes a bot message (channel reply-gating)
	SentAt              time.Time
	Files               []files.IncomingFile
	Forward             *ForwardInfo // Telegram-only forwarded-sender info; nil otherwise
}

// ForwardInfo carries the structured sender of a forwarded message for the
// People social graph. Telegram-only in v0.10 (Mattermost has no forward
// origin with user identity); nil for non-forwarded messages.
type ForwardInfo struct {
	SenderID  string // forwarded sender's native id (Telegram int64 as string)
	FirstName string
	LastName  string
	Username  string
	IsBot     bool
	IsUser    bool // true only when forwarded from a user (not channel/hidden)
}

// OutgoingResponse is one rendered, ready-to-send message chunk. Text is in the
// transport's wire format (HTML for Telegram, markdown for Mattermost) as
// produced by the transport's Renderer.
type OutgoingResponse struct {
	ConversationID string
	Text           string
	ThreadRoot     string // set on every chunk to keep replies threaded
	ReplyTo        string // message id to reply to; set on the first chunk only
}

// OutgoingMedia is a transport-neutral media reply: one or more files sharing a
// single caption, threaded/replied like a text response. Caption is in the
// transport's wire format (HTML for Telegram, markdown for Mattermost) as
// produced by the transport's Renderer.RenderCaption, already fitted to the
// transport's caption budget; any overflow is delivered by the caller as a
// follow-up text message.
type OutgoingMedia struct {
	ConversationID string
	ThreadRoot     string // keeps the media threaded
	ReplyTo        string // message id to reply to; first item only
	Caption        string // wire-format caption (may be empty)
	Items          []OutgoingMediaItem
}

// OutgoingMediaItem is one file in an OutgoingMedia batch.
type OutgoingMediaItem struct {
	Data       []byte
	Filename   string
	MIME       string
	AsDocument bool // force document delivery (Telegram); transports without the photo/doc distinction ignore it
}

// Capabilities describes per-transport rendering and feature support. The core
// branches on these, never on Kind(), so transport-specific behavior stays
// declarative.
type Capabilities struct {
	MaxMessageLen         int    // Telegram 4096 UTF-16; Mattermost MaxPostSize (runtime)
	ParseMode             string // "HTML" (Telegram) | "" native markdown (Mattermost)
	SupportsLatex         bool   // Telegram false (latex->unicode) | Mattermost true (native KaTeX)
	SupportsStreaming     bool   // Telegram true (editMessageText) | Mattermost false
	SupportsReactions     bool   // Telegram true | Mattermost true (by emoji name)
	SupportsMedia         bool   // can SendMedia deliver files
	MaxMediaItemsPerGroup int    // Telegram 10; Mattermost 5 (files per post)
	EmojiStyle            string // "unicode" (Telegram) | "shortcode" (Mattermost)
	MaxFileSize           int64  // 0 = unset/unlimited (files are Phase 4)
	// AvailableReactions are the reaction tokens the bot may use on this
	// transport: unicode emoji for Telegram (the fixed Bot API set), emoji
	// shortcode names for Mattermost. Empty disables reactions.
	AvailableReactions []string
}

// Transport is the output + identity surface a chat backend must implement.
// Ingestion is handled per-transport (each maps its native updates into
// IncomingMessage and feeds Bot.HandleIncoming); only sending, typing,
// reactions, capabilities, and the per-transport allowlist live here.
type Transport interface {
	// SendText transmits one rendered chunk, returning the new message id.
	SendText(ctx context.Context, r OutgoingResponse) (msgID string, err error)
	// SendMedia transmits a batch of files with a shared caption, returning the
	// primary message id. Callers gate on Capabilities().SupportsMedia.
	SendMedia(ctx context.Context, m OutgoingMedia) (msgID string, err error)
	// SendTyping shows a best-effort typing indicator in the conversation.
	SendTyping(ctx context.Context, conversationID string) error
	// SetReaction adds an emoji reaction to a message (best-effort; no-op if
	// unsupported). The emoji must come from Capabilities().AvailableReactions —
	// transport-native form (unicode for Telegram, shortcode for Mattermost).
	SetReaction(ctx context.Context, conversationID, messageID, emoji string) error
	Kind() string
	Capabilities() Capabilities
	// IsAllowed reports whether the native sender id is in the static allowlist.
	IsAllowed(nativeSenderID string) bool
	// AllowlistConfigured reports whether a static allowlist is configured at all.
	// It distinguishes "empty allowlist" (IsAllowed always false, fail-closed in
	// simple mode) from "allowlist used as an optional subset filter" in SSO mode,
	// where an empty list means "all trusted senders" rather than "no one".
	AllowlistConfigured() bool
}

// Renderer converts a canonical-markdown response into one or more wire-format
// chunks for a transport (HTML for Telegram, markdown for Mattermost),
// respecting the transport's message-length limit. The context carries the
// root processing span so renderers can record anomaly attributes (e.g.
// re-splits after HTML expansion).
type Renderer interface {
	Render(ctx context.Context, canonicalMarkdown string) (chunks []string, err error)
	// RenderCaption fits the start of a canonical-markdown response into the
	// transport's media caption budget (measured on the rendered wire format)
	// and returns the wire-format caption plus the remaining markdown to send
	// as follow-up text via Render.
	RenderCaption(ctx context.Context, canonicalMarkdown string) (wireCaption, overflowMarkdown string)
}

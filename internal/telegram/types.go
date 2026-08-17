package telegram

import (
	"encoding/json"
	"fmt"
)

// APIResponse represents a response from the Telegram API.
type APIResponse struct {
	Ok          bool                `json:"ok"`
	Result      json.RawMessage     `json:"result,omitempty"`
	Description string              `json:"description,omitempty"`
	ErrorCode   int                 `json:"error_code,omitempty"`
	Parameters  *ResponseParameters `json:"parameters,omitempty"`
}

// ResponseParameters contains additional information about a failed Telegram
// API request. RetryAfter is populated for rate-limit responses.
type ResponseParameters struct {
	RetryAfter int `json:"retry_after,omitempty"`
}

// APIError is a structured failure returned by the Telegram API. Callers treat
// 4xx responses as confirmed request rejections; 5xx remains an unknown send
// outcome because the server may have accepted a non-idempotent request before
// failing. Network and response-decoding failures are returned as other error
// types and are unknown as well.
type APIError struct {
	Code        int                 `json:"error_code"`
	Description string              `json:"description"`
	Parameters  *ResponseParameters `json:"parameters,omitempty"`
}

func (e *APIError) Error() string {
	if e.Code == 0 {
		return fmt.Sprintf("telegram API error: %s", e.Description)
	}
	return fmt.Sprintf("telegram API error %d: %s", e.Code, e.Description)
}

// Update represents an incoming update.
type Update struct {
	UpdateID        int                     `json:"update_id"`
	Message         *Message                `json:"message,omitempty"`
	MessageReaction *MessageReactionUpdated `json:"message_reaction,omitempty"`
}

// MessageReactionUpdated represents a change of a reaction on a message
// performed by a user. Delivered only when "message_reaction" is in the
// getUpdates allowed_updates list; never for reactions set by bots.
type MessageReactionUpdated struct {
	Chat        *Chat          `json:"chat"`
	MessageID   int            `json:"message_id"`
	User        *User          `json:"user,omitempty"`
	Date        int            `json:"date"`
	OldReaction []ReactionType `json:"old_reaction"`
	NewReaction []ReactionType `json:"new_reaction"`
}

// Message represents a message.
type Message struct {
	MessageID            int                  `json:"message_id"`
	MessageThreadID      int                  `json:"message_thread_id,omitempty"`
	BusinessConnectionID string               `json:"business_connection_id,omitempty"`
	DirectMessagesTopic  *DirectMessagesTopic `json:"direct_messages_topic,omitempty"`
	From                 *User                `json:"from,omitempty"`
	Chat                 *Chat                `json:"chat"`
	Date                 int                  `json:"date"`
	Text                 string               `json:"text,omitempty"`
	Entities             []MessageEntity      `json:"entities,omitempty"`
	Caption              string               `json:"caption,omitempty"`
	CaptionEntities      []MessageEntity      `json:"caption_entities,omitempty"`
	Photo                []PhotoSize          `json:"photo,omitempty"`
	Document             *Document            `json:"document,omitempty"`
	Voice                *Voice               `json:"voice,omitempty"`
	Audio                *Audio               `json:"audio,omitempty"`
	VideoNote            *VideoNote           `json:"video_note,omitempty"`
	RichMessage          *RichMessage         `json:"rich_message,omitempty"`
	ForwardOrigin        *MessageOrigin       `json:"forward_origin,omitempty"`
}

// MessageEntityType identifies one of Telegram's special entities in ordinary
// message text or a media caption. It is a string so newer Bot API entity
// types remain decodable and can degrade to visible text.
type MessageEntityType string

// Telegram Bot API 10.2 MessageEntity types.
const (
	MessageEntityTypeMention              MessageEntityType = "mention"
	MessageEntityTypeHashtag              MessageEntityType = "hashtag"
	MessageEntityTypeCashtag              MessageEntityType = "cashtag"
	MessageEntityTypeBotCommand           MessageEntityType = "bot_command"
	MessageEntityTypeURL                  MessageEntityType = "url"
	MessageEntityTypeEmail                MessageEntityType = "email"
	MessageEntityTypePhoneNumber          MessageEntityType = "phone_number"
	MessageEntityTypeBold                 MessageEntityType = "bold"
	MessageEntityTypeItalic               MessageEntityType = "italic"
	MessageEntityTypeUnderline            MessageEntityType = "underline"
	MessageEntityTypeStrikethrough        MessageEntityType = "strikethrough"
	MessageEntityTypeSpoiler              MessageEntityType = "spoiler"
	MessageEntityTypeBlockquote           MessageEntityType = "blockquote"
	MessageEntityTypeExpandableBlockquote MessageEntityType = "expandable_blockquote"
	MessageEntityTypeCode                 MessageEntityType = "code"
	MessageEntityTypePre                  MessageEntityType = "pre"
	MessageEntityTypeTextLink             MessageEntityType = "text_link"
	MessageEntityTypeTextMention          MessageEntityType = "text_mention"
	MessageEntityTypeCustomEmoji          MessageEntityType = "custom_emoji"
	MessageEntityTypeDateTime             MessageEntityType = "date_time"
)

// MessageEntity represents one special entity in Message.Text or
// Message.Caption. Offset and Length are measured in UTF-16 code units, not
// UTF-8 bytes or Unicode code points.
type MessageEntity struct {
	Type           MessageEntityType `json:"type"`
	Offset         int               `json:"offset"`
	Length         int               `json:"length"`
	URL            string            `json:"url,omitempty"`
	User           *User             `json:"user,omitempty"`
	Language       string            `json:"language,omitempty"`
	CustomEmojiID  string            `json:"custom_emoji_id,omitempty"`
	UnixTime       int64             `json:"unix_time,omitempty"`
	DateTimeFormat string            `json:"date_time_format,omitempty"`
}

// DirectMessagesTopic is kept minimal because v1 only needs to detect this
// context and keep outbound rich delivery on the legacy path until its routing
// identifiers are carried by SendRichMessageRequest.
type DirectMessagesTopic struct {
	TopicID int64 `json:"topic_id"`
}

// User represents a Telegram user or bot.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// Chat represents a chat.
type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
}

// PhotoSize represents one size of a photo or a file / sticker thumbnail.
type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// Document represents a general file (as opposed to photos, voice messages and audio files).
type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// Voice represents a voice note.
type Voice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// Audio represents an audio file (MP3, etc.).
type Audio struct {
	FileID       string     `json:"file_id"`
	FileUniqueID string     `json:"file_unique_id"`
	Duration     int        `json:"duration"`
	Performers   string     `json:"performer,omitempty"`
	Title        string     `json:"title,omitempty"`
	FileName     string     `json:"file_name,omitempty"`
	MimeType     string     `json:"mime_type,omitempty"`
	FileSize     int64      `json:"file_size,omitempty"`
	Thumbnail    *PhotoSize `json:"thumbnail,omitempty"`
}

// Animation represents an animation file (GIF or silent MPEG-4 video).
// FileSize is int64 because Bot API file sizes may exceed 2^31.
type Animation struct {
	FileID       string     `json:"file_id"`
	FileUniqueID string     `json:"file_unique_id"`
	Width        int        `json:"width"`
	Height       int        `json:"height"`
	Duration     int        `json:"duration"`
	Thumbnail    *PhotoSize `json:"thumbnail,omitempty"`
	FileName     string     `json:"file_name,omitempty"`
	MimeType     string     `json:"mime_type,omitempty"`
	FileSize     int64      `json:"file_size,omitempty"`
}

// Video represents a video file received from Telegram.
type Video struct {
	FileID         string         `json:"file_id"`
	FileUniqueID   string         `json:"file_unique_id"`
	Width          int            `json:"width"`
	Height         int            `json:"height"`
	Duration       int            `json:"duration"`
	Thumbnail      *PhotoSize     `json:"thumbnail,omitempty"`
	Cover          []PhotoSize    `json:"cover,omitempty"`
	StartTimestamp int            `json:"start_timestamp,omitempty"`
	Qualities      []VideoQuality `json:"qualities,omitempty"`
	FileName       string         `json:"file_name,omitempty"`
	MimeType       string         `json:"mime_type,omitempty"`
	FileSize       int64          `json:"file_size,omitempty"`
}

// VideoQuality represents one downloadable encoding of a video.
type VideoQuality struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Codec        string `json:"codec"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// Location represents a point on the map. Live-location-only fields are kept
// so a future Bot API response can be projected without silently losing them.
type Location struct {
	Latitude             float64 `json:"latitude"`
	Longitude            float64 `json:"longitude"`
	HorizontalAccuracy   float64 `json:"horizontal_accuracy,omitempty"`
	LivePeriod           int     `json:"live_period,omitempty"`
	Heading              int     `json:"heading,omitempty"`
	ProximityAlertRadius int     `json:"proximity_alert_radius,omitempty"`
}

// VideoNote represents a video message (video circle).
type VideoNote struct {
	FileID       string     `json:"file_id"`
	FileUniqueID string     `json:"file_unique_id"`
	Length       int        `json:"length"`   // Video width and height (diameter of the video message)
	Duration     int        `json:"duration"` // Duration of the video in seconds
	Thumbnail    *PhotoSize `json:"thumbnail,omitempty"`
	FileSize     int64      `json:"file_size,omitempty"`
}

// MessageOrigin represents the origin of a message.
type MessageOrigin struct {
	Type            string `json:"type"`
	Date            int    `json:"date"`
	SenderUser      *User  `json:"sender_user,omitempty"`
	SenderUserName  string `json:"sender_user_name,omitempty"`
	SenderChat      *Chat  `json:"sender_chat,omitempty"`
	AuthorSignature string `json:"author_signature,omitempty"`
}

// EditMessageTextRequest represents the parameters for the editMessageText method.
// MessageThreadID is not editable (Telegram derives it from MessageID), so it is
// omitted here. Used by the streaming sink to update an in-flight reply.
type EditMessageTextRequest struct {
	ChatID    int64  `json:"chat_id"`
	MessageID int    `json:"message_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

// SendMessageRequest represents the parameters for the sendMessage method.
//
// IMPORTANT: MessageThreadID uses *int instead of int so that omitempty works
// correctly for the zero value. The Telegram API interprets message_thread_id: 0
// as an attempt to send to a topic with ID=0, which causes a
// "Bad Request: invalid topic identifier specified" error in regular chats (non-forums).
// With a pointer, nil is not serialized into JSON at all.
type SendMessageRequest struct {
	ChatID           int64  `json:"chat_id"`
	MessageThreadID  *int   `json:"message_thread_id,omitempty"`
	Text             string `json:"text"`
	ParseMode        string `json:"parse_mode,omitempty"`
	ReplyToMessageID int    `json:"reply_to_message_id,omitempty"`
}

// InputRichMessage contains the HTML representation accepted by Telegram's
// sendRichMessage method. Media entries bind tg://photo?id=<ID> references in
// HTML to Telegram InputMedia objects. Local uploads additionally require a
// matching RichMessageAttachment on SendRichMessageRequest.
type InputRichMessage struct {
	HTML                string                  `json:"html,omitempty"`
	Media               []InputRichMessageMedia `json:"media,omitempty"`
	SkipEntityDetection bool                    `json:"skip_entity_detection,omitempty"`
}

// InputRichMessageMedia is the official InputRichMessageMedia envelope. ID is
// referenced by rich HTML as tg://photo?id=<ID>; Media describes the actual
// Telegram photo source.
type InputRichMessageMedia struct {
	ID    string                `json:"id"`
	Media InputRichMessagePhoto `json:"media"`
}

// InputRichMessagePhoto is the photo-only subset of Telegram's InputMedia
// union used by outgoing rich messages. The wire type is always "photo", so
// callers cannot accidentally construct a mismatched media discriminator.
type InputRichMessagePhoto struct {
	Media string `json:"media"`
}

func (p InputRichMessagePhoto) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type  string `json:"type"`
		Media string `json:"media"`
	}{
		Type:  "photo",
		Media: p.Media,
	})
}

// RichPhotoUpload is one trusted local photo to bind into an outgoing Rich
// Message. BuildRichPhotoMedia assigns the Telegram-visible and multipart
// identifiers; callers only provide file metadata and bytes.
type RichPhotoUpload struct {
	Filename string
	MIME     string
	Data     []byte
}

// RichMessageAttachment is a local file uploaded with sendRichMessage. ID is
// the multipart field identifier used by an InputRichMessagePhoto source of
// the form attach://<ID>. Attachments are transport-local and never serialized
// into the rich_message JSON object.
type RichMessageAttachment struct {
	ID          string `json:"-"`
	Filename    string `json:"-"`
	ContentType string `json:"-"`
	Data        []byte `json:"-"`
}

// ReplyParameters identifies the message being replied to.
type ReplyParameters struct {
	MessageID int `json:"message_id"`
}

// SendRichMessageRequest represents the parameters for sendRichMessage.
// Optional integer fields use pointers so a missing value is omitted instead
// of being serialized as an invalid zero identifier.
type SendRichMessageRequest struct {
	ChatID          int64                   `json:"chat_id"`
	MessageThreadID *int                    `json:"message_thread_id,omitempty"`
	RichMessage     InputRichMessage        `json:"rich_message"`
	ReplyParameters *ReplyParameters        `json:"reply_parameters,omitempty"`
	Attachments     []RichMessageAttachment `json:"-"`
}

// SendRichMessageDraftRequest represents the parameters for
// sendRichMessageDraft. DraftID is the stable, positive int32 identifier used to
// animate subsequent snapshots of the same ephemeral draft.
type SendRichMessageDraftRequest struct {
	ChatID          int64            `json:"chat_id"`
	MessageThreadID *int             `json:"message_thread_id,omitempty"`
	DraftID         int64            `json:"draft_id"`
	RichMessage     InputRichMessage `json:"rich_message"`
}

// BotCommand represents a bot command.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetMyCommandsRequest represents the parameters for the setMyCommands method.
type SetMyCommandsRequest struct {
	Commands []BotCommand `json:"commands"`
}

// SetWebhookRequest represents the parameters for the setWebhook method.
type SetWebhookRequest struct {
	URL            string   `json:"url"`
	SecretToken    string   `json:"secret_token,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

// SendChatActionRequest represents the parameters for the sendChatAction method.
// See the SendMessageRequest comment for why MessageThreadID uses *int.
type SendChatActionRequest struct {
	ChatID          int64  `json:"chat_id"`
	MessageThreadID *int   `json:"message_thread_id,omitempty"`
	Action          string `json:"action"`
}

// File represents a file ready to be downloaded.
type File struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int64  `json:"file_size,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
}

// GetFileRequest represents the parameters for the getFile method.
type GetFileRequest struct {
	FileID string `json:"file_id"`
}

// ReactionType represents a reaction type.
type ReactionType struct {
	Type  string `json:"type"`
	Emoji string `json:"emoji,omitempty"`
}

// SetMessageReactionRequest represents the parameters for the setMessageReaction method.
type SetMessageReactionRequest struct {
	ChatID    int64          `json:"chat_id"`
	MessageID int            `json:"message_id"`
	Reaction  []ReactionType `json:"reaction,omitempty"`
	IsBig     bool           `json:"is_big,omitempty"`
}

// GetUpdatesRequest represents the parameters for the getUpdates method.
type GetUpdatesRequest struct {
	Offset         int      `json:"offset,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	Timeout        int      `json:"timeout,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

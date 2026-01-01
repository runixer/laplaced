package telegram

import "encoding/json"

// APIResponse represents a response from the Telegram API.
type APIResponse struct {
	Ok          bool            `json:"ok"`
	Result      json.RawMessage `json:"result,omitempty"`
	Description string          `json:"description,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
}

// Update represents an incoming update.
type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message,omitempty"`
}

// Message represents a message.
type Message struct {
	MessageID       int            `json:"message_id"`
	MessageThreadID int            `json:"message_thread_id,omitempty"`
	From            *User          `json:"from,omitempty"`
	Chat            *Chat          `json:"chat"`
	Date            int            `json:"date"`
	Text            string         `json:"text,omitempty"`
	Caption         string         `json:"caption,omitempty"`
	Photo           []PhotoSize    `json:"photo,omitempty"`
	Document        *Document      `json:"document,omitempty"`
	Voice           *Voice         `json:"voice,omitempty"`
	ForwardOrigin   *MessageOrigin `json:"forward_origin,omitempty"`
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
	FileSize     int    `json:"file_size,omitempty"`
}

// Document represents a general file (as opposed to photos, voice messages and audio files).
type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
}

// Voice represents a voice note.
type Voice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
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

// SendMessageRequest represents the parameters for the sendMessage method.
//
// ВАЖНО: MessageThreadID использует *int вместо int, чтобы omitempty корректно
// работал для нулевого значения. Telegram API интерпретирует message_thread_id: 0
// как попытку отправить в топик с ID=0, что вызывает ошибку
// "Bad Request: invalid topic identifier specified" в обычных чатах (не форумах).
// При использовании указателя nil не сериализуется в JSON вообще.
type SendMessageRequest struct {
	ChatID           int64  `json:"chat_id"`
	MessageThreadID  *int   `json:"message_thread_id,omitempty"`
	Text             string `json:"text"`
	ParseMode        string `json:"parse_mode,omitempty"`
	ReplyToMessageID int    `json:"reply_to_message_id,omitempty"`
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
	URL         string `json:"url"`
	SecretToken string `json:"secret_token,omitempty"`
}

// SendChatActionRequest represents the parameters for the sendChatAction method.
// См. комментарий к SendMessageRequest о причине использования *int для MessageThreadID.
type SendChatActionRequest struct {
	ChatID          int64  `json:"chat_id"`
	MessageThreadID *int   `json:"message_thread_id,omitempty"`
	Action          string `json:"action"`
}

// File represents a file ready to be downloaded.
type File struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int    `json:"file_size,omitempty"`
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

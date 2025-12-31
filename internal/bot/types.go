package bot

import "encoding/json"

// Update is a Telegram object that represents an incoming update.
type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message represents a message.
type Message struct {
	MessageID        int            `json:"message_id"`
	From             *User          `json:"from"`
	Chat             *Chat          `json:"chat"`
	Date             int            `json:"date"`
	Text             string         `json:"text"`
	Caption          string         `json:"caption"`
	Photo            []PhotoSize    `json:"photo"`
	Document         *Document      `json:"document"`
	Voice            *Voice         `json:"voice"`
	ForwardOrigin    *MessageOrigin `json:"forward_origin"`
	IsCommand        bool           `json:"-"` // This will be determined manually
	Command          string         `json:"-"` // This will be determined manually
	CommandArguments string         `json:"-"` // This will be determined manually
}

// User represents a Telegram user or bot.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	UserName  string `json:"username"`
}

// Chat represents a chat.
type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username"`
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
	FileID       string     `json:"file_id"`
	FileUniqueID string     `json:"file_unique_id"`
	Thumbnail    *PhotoSize `json:"thumbnail,omitempty"`
	FileName     string     `json:"file_name,omitempty"`
	MimeType     string     `json:"mime_type,omitempty"`
	FileSize     int        `json:"file_size,omitempty"`
}

// Voice represents a voice note.
type Voice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
}

// MessageOrigin is a union type that can be one of MessageOriginUser,
// MessageOriginHiddenUser, MessageOriginChat, or MessageOriginChannel.
type MessageOrigin struct {
	Type            string `json:"type"`
	Date            int    `json:"date"`
	SenderUser      *User  `json:"sender_user,omitempty"`
	SenderUserName  string `json:"sender_user_name,omitempty"`
	SenderChat      *Chat  `json:"sender_chat,omitempty"`
	AuthorSignature string `json:"author_signature,omitempty"`
	MessageID       int    `json:"message_id,omitempty"`
}

// UnmarshalJSON is a custom unmarshaler for MessageOrigin to handle the union type.
func (mo *MessageOrigin) UnmarshalJSON(data []byte) error {
	type Alias MessageOrigin
	var aux struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	mo.Type = aux.Type
	return json.Unmarshal(data, (*Alias)(mo))
}

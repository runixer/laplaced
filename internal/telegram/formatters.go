package telegram

import (
	"fmt"
	"strings"
	"time"

	"github.com/runixer/laplaced/internal/i18n"
)

// Format formats user information for display, including their username if available.
func (u *User) Format() string {
	if u == nil {
		return "Unknown"
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if u.Username != "" {
		if name != "" {
			name = fmt.Sprintf("%s (@%s)", name, u.Username)
		} else {
			name = "@" + u.Username
		}
	}
	if name == "" {
		name = fmt.Sprintf("ID:%d", u.ID)
	}
	return name
}

// Format formats chat information for display.
func (c *Chat) Format() string {
	if c == nil {
		return "Unknown Chat"
	}
	name := c.Title
	if c.Username != "" {
		if name != "" {
			name = fmt.Sprintf("%s (@%s)", name, c.Username)
		} else {
			name = "@" + c.Username
		}
	}
	if name == "" {
		name = fmt.Sprintf("ChatID:%d", c.ID)
	}
	return name
}

// Format formats a Unix timestamp into a readable string.
func formatTime(unixTime int) string {
	if unixTime == 0 {
		return "unknown time"
	}
	return time.Unix(int64(unixTime), 0).Format("2006-01-02 15:04:05")
}

// Format formats the forward origin information for display.
func (mo *MessageOrigin) Format(forwardedBy *User, translator *i18n.Translator, lang string) string {
	var from string
	switch mo.Type {
	case "user":
		from = mo.SenderUser.Format()
	case "hidden_user":
		from = mo.SenderUserName
	case "chat":
		from = mo.SenderChat.Format()
		if mo.AuthorSignature != "" {
			from = fmt.Sprintf("%s (as %s)", from, mo.AuthorSignature)
		}
	case "channel":
		from = mo.SenderChat.Format()
		if mo.AuthorSignature != "" {
			from = fmt.Sprintf("%s (as %s)", from, mo.AuthorSignature)
		}
	default:
		from = "Unknown Source"
	}

	return translator.Get(lang, "telegram.forwarded_from", from, forwardedBy.Format(), formatTime(mo.Date))
}

// BuildContent constructs the full text content for a message, including prefixes for user, time, and forwarding.
func (m *Message) BuildContent(translator *i18n.Translator, lang string) string {
	var prefixBuilder strings.Builder
	if m.ForwardOrigin != nil {
		prefixBuilder.WriteString(m.ForwardOrigin.Format(m.From, translator, lang))
	} else {
		prefixBuilder.WriteString(fmt.Sprintf("[%s (%s)]",
			m.From.Format(),
			formatTime(m.Date),
		))
	}

	var messageText string
	if m.Text != "" {
		messageText = m.Text
	} else if m.Caption != "" {
		messageText = m.Caption
	}

	if messageText == "" {
		if len(m.Photo) > 0 {
			messageText = "(photo)"
		} else if m.Document != nil {
			// Document content is handled separately in the bot logic
		} else {
			return "" // No text and no media we describe
		}
	}

	return fmt.Sprintf("%s: %s", prefixBuilder.String(), messageText)
}

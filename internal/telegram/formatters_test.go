package telegram

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runixer/laplaced/internal/i18n"

	"github.com/stretchr/testify/assert"
)

func createTestTranslator(t *testing.T) *i18n.Translator {
	tmpDir := t.TempDir()
	content := `
telegram:
  forwarded_from: "[Переслано от %s пользователем %s в %s]"
`
	_ = os.WriteFile(filepath.Join(tmpDir, "en.yaml"), []byte(content), 0644)
	tr, _ := i18n.NewTranslatorFromFS(os.DirFS(tmpDir), "en")
	return tr
}

func TestUser_Format(t *testing.T) {
	testCases := []struct {
		name     string
		user     *User
		expected string
	}{
		{"NilUser", nil, "Unknown"},
		{"FullUser", &User{ID: 1, FirstName: "John", LastName: "Doe", Username: "johndoe"}, "John Doe (@johndoe)"},
		{"FirstNameOnly", &User{ID: 2, FirstName: "Jane"}, "Jane"},
		{"UsernameOnly", &User{ID: 3, Username: "peter"}, "@peter"},
		{"IDOnly", &User{ID: 4}, "ID:4"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.user.Format())
		})
	}
}

func TestChat_Format(t *testing.T) {
	testCases := []struct {
		name     string
		chat     *Chat
		expected string
	}{
		{"NilChat", nil, "Unknown Chat"},
		{"FullChat", &Chat{ID: 1, Title: "Test Chat", Username: "testchat"}, "Test Chat (@testchat)"},
		{"TitleOnly", &Chat{ID: 2, Title: "Another Chat"}, "Another Chat"},
		{"UsernameOnly", &Chat{ID: 3, Username: "group"}, "@group"},
		{"IDOnly", &Chat{ID: 4}, "ChatID:4"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.chat.Format())
		})
	}
}

func TestMessageOrigin_Format(t *testing.T) {
	translator := createTestTranslator(t)
	forwardedBy := &User{ID: 1, FirstName: "John", LastName: "Doe", Username: "johndoe"}
	now := int(time.Now().Unix())
	formattedTime := formatTime(now)

	testCases := []struct {
		name     string
		origin   *MessageOrigin
		expected string
	}{
		{
			name: "Forward from user",
			origin: &MessageOrigin{
				Type:       "user",
				Date:       now,
				SenderUser: &User{ID: 2, FirstName: "Jane", Username: "jane"},
			},
			expected: fmt.Sprintf("[Переслано от Jane (@jane) пользователем John Doe (@johndoe) в %s]", formattedTime),
		},
		{
			name: "Forward from channel",
			origin: &MessageOrigin{
				Type:       "channel",
				Date:       now,
				SenderChat: &Chat{ID: 100, Title: "News", Username: "news_ch"},
			},
			expected: fmt.Sprintf("[Переслано от News (@news_ch) пользователем John Doe (@johndoe) в %s]", formattedTime),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.origin.Format(forwardedBy, translator, "en"))
		})
	}
}

func TestMessage_BuildContent(t *testing.T) {
	translator := createTestTranslator(t)
	now := int(time.Now().Unix())
	user := &User{ID: 1, FirstName: "Test", Username: "tester"}
	formattedTime := formatTime(now)

	testCases := []struct {
		name     string
		message  *Message
		expected string
	}{
		{
			name:     "Simple text message",
			message:  &Message{From: user, Date: now, Text: "Hello"},
			expected: fmt.Sprintf("[Test (@tester) (%s)]: Hello", formattedTime),
		},
		{
			name:     "Caption message",
			message:  &Message{From: user, Date: now, Caption: "World"},
			expected: fmt.Sprintf("[Test (@tester) (%s)]: World", formattedTime),
		},
		{
			name: "Forwarded message",
			message: &Message{
				From: user,
				Date: now,
				Text: "Forwarded text",
				ForwardOrigin: &MessageOrigin{
					Type:       "user",
					Date:       now - 100,
					SenderUser: &User{ID: 2, FirstName: "Original", Username: "original_sender"},
				},
			},
			expected: fmt.Sprintf("%s: Forwarded text", (&MessageOrigin{
				Type:       "user",
				Date:       now - 100,
				SenderUser: &User{ID: 2, FirstName: "Original", Username: "original_sender"},
			}).Format(user, translator, "en")),
		},
		{
			name:     "Photo with no text",
			message:  &Message{From: user, Date: now, Photo: []PhotoSize{{FileID: "123"}}},
			expected: fmt.Sprintf("[Test (@tester) (%s)]: (photo)", formattedTime),
		},
		{
			name:     "Empty message",
			message:  &Message{From: user, Date: now},
			expected: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.message.BuildContent(translator, "en"))
		})
	}
}

package bot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/testutil"
)

func TestScopeLabel(t *testing.T) {
	tests := []struct {
		name string
		im   IncomingMessage
		want string
	}{
		{"DM uses sender", IncomingMessage{IsDirect: true, SenderDisplay: "John Doe (@jdoe)", ConversationDisplay: "ignored"}, "John Doe (@jdoe)"},
		{"channel uses channel name", IncomingMessage{IsDirect: false, SenderDisplay: "John Doe (@jdoe)", ConversationDisplay: "Release Team"}, "Release Team"},
		{"channel without name falls back to sender", IncomingMessage{IsDirect: false, SenderDisplay: "@alice", ConversationDisplay: ""}, "@alice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, scopeLabel(tt.im))
		})
	}
}

func TestResolveScopeID_TelegramPassthrough(t *testing.T) {
	mockStore := new(testutil.MockStorage)

	id, err := resolveScopeID(mockStore, transportTelegram, IncomingMessage{SenderID: "123456", IsDirect: true})
	require.NoError(t, err)
	assert.Equal(t, int64(123456), id)

	// Passthrough must not touch the scopes table on the home path.
	mockStore.AssertNotCalled(t, "ResolveScope")
	mockStore.AssertExpectations(t)
}

func TestResolveScopeID_TelegramInvalid(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	_, err := resolveScopeID(mockStore, transportTelegram, IncomingMessage{SenderID: "not-an-int", IsDirect: true})
	assert.Error(t, err)
}

func TestResolveScopeID_TimeDMMintsUserScope(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	// A DM is scoped to its sender, tagged 'user'.
	mockStore.On("ResolveScope", transportTime, "user", "u26charstring").Return(int64(7), nil)

	id, err := resolveScopeID(mockStore, transportTime, IncomingMessage{
		SenderID:       "u26charstring",
		ConversationID: "dm26charchannel",
		IsDirect:       true,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(7), id)
	mockStore.AssertExpectations(t)
}

func TestResolveScopeID_TimeChannelMintsChannelScope(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	// A channel is scoped to the conversation, tagged 'channel'.
	mockStore.On("ResolveScope", transportTime, "channel", "chan26charchannel").Return(int64(42), nil)

	id, err := resolveScopeID(mockStore, transportTime, IncomingMessage{
		SenderID:       "u26charstring",
		ConversationID: "chan26charchannel",
		IsDirect:       false,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(42), id)
	mockStore.AssertExpectations(t)
}

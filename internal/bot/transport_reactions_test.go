package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/reactor"
	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/mattermost"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/telegram"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestTelegramReactionEmoji_APIExactForms(t *testing.T) {
	assert.Len(t, telegramReactionEmoji, 73, "the Bot API ReactionTypeEmoji set has 73 entries")
	seen := make(map[string]bool)
	for _, e := range telegramReactionEmoji {
		assert.NotContains(t, e, "\ufe0f", "emoji %q must not carry a variation selector (API-exact form)", e)
		assert.False(t, seen[e], "duplicate emoji %q", e)
		seen[e] = true
	}
}

func TestTelegramTransport_Capabilities_Reactions(t *testing.T) {
	tr := NewTelegramTransport(new(testutil.MockBotAPI), testutil.TestConfig(), testutil.TestTranslator(t), testutil.TestLogger())
	caps := tr.Capabilities()
	assert.True(t, caps.SupportsReactions)
	assert.Equal(t, telegramReactionEmoji, caps.AvailableReactions)
}

func TestTelegramTransport_SetReaction_ForwardsEmoji(t *testing.T) {
	mockAPI := new(testutil.MockBotAPI)
	var captured telegram.SetMessageReactionRequest
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			captured = args.Get(1).(telegram.SetMessageReactionRequest)
		}).
		Return(nil)

	tr := NewTelegramTransport(mockAPI, testutil.TestConfig(), testutil.TestTranslator(t), testutil.TestLogger())
	err := tr.SetReaction(context.Background(), "12345", "100", "🔥")
	require.NoError(t, err)

	assert.Equal(t, int64(12345), captured.ChatID)
	assert.Equal(t, 100, captured.MessageID)
	require.Len(t, captured.Reaction, 1)
	assert.Equal(t, "emoji", captured.Reaction[0].Type)
	assert.Equal(t, "🔥", captured.Reaction[0].Emoji)
	mockAPI.AssertExpectations(t)
}

func TestMMTransport_Capabilities_Reactions(t *testing.T) {
	assert.NotEmpty(t, mmReactions)
	for _, name := range mmReactions {
		assert.NotContains(t, name, ":", "shortcode %q must be bare (no colons)", name)
		assert.Equal(t, strings.TrimSpace(name), name)
	}
}

func TestMMTransport_SetReaction_ForwardsShortcode(t *testing.T) {
	var gotBody struct {
		UserID    string `json:"user_id"`
		PostID    string `json:"post_id"`
		EmojiName string `json:"emoji_name"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "botid", "username": "laplaced", "is_bot": true})
		case "/api/v4/config/client":
			_ = json.NewEncoder(w).Encode(map[string]string{"MaxPostSize": "16383"})
		case "/api/v4/reactions":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			_ = json.NewEncoder(w).Encode(map[string]string{"emoji_name": gotBody.EmojiName})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := mattermost.NewClient(context.Background(), mattermost.Config{ServerURL: srv.URL, BotToken: "tok"}, testutil.TestLogger())
	require.NoError(t, err)
	tr := NewMattermostTransport(client, &config.Config{}, testutil.TestLogger())

	err = tr.SetReaction(context.Background(), "chan1", "post1", "fire")
	require.NoError(t, err)
	assert.Equal(t, "post1", gotBody.PostID)
	assert.Equal(t, "fire", gotBody.EmojiName)
}

// fakeReactorAgent is a local stub for the bot-level reaction flow tests
// (avoids an LLM round-trip; the real agent has its own tests).
type fakeReactorAgent struct {
	result *reactor.Result
	err    error
	calls  atomic.Int32
}

func (f *fakeReactorAgent) Type() agent.AgentType { return agent.TypeReactor }
func (f *fakeReactorAgent) Execute(context.Context, *agent.Request) (*agent.Response, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return &agent.Response{Structured: f.result}, nil
}

// setupReactionBot builds a struct-literal Bot with a Telegram transport over
// MockBotAPI, ready for maybeReact calls.
func setupReactionBot(t *testing.T, fake *fakeReactorAgent) (*Bot, *testutil.MockBotAPI, *testutil.MockStorage) {
	t.Helper()
	mockAPI := new(testutil.MockBotAPI)
	mockStore := new(testutil.MockStorage)
	mockStore.On("GetRecentHistory", mock.Anything, mock.Anything).Return([]storage.Message{}, nil).Maybe()

	cfg := testutil.TestConfig()
	cfg.Agents.Reactor.Enabled = true

	b := &Bot{
		cfg:          cfg,
		msgRepo:      mockStore,
		logger:       testutil.TestLogger(),
		transport:    NewTelegramTransport(mockAPI, cfg, testutil.TestTranslator(t), testutil.TestLogger()),
		reactorAgent: fake,
	}
	return b, mockAPI, mockStore
}

func TestMaybeReact_SetsChosenReaction(t *testing.T) {
	fake := &fakeReactorAgent{result: &reactor.Result{Emoji: "👍"}}
	b, mockAPI, _ := setupReactionBot(t, fake)

	var captured telegram.SetMessageReactionRequest
	mockAPI.On("SetMessageReaction", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			captured = args.Get(1).(telegram.SetMessageReactionRequest)
		}).
		Return(nil)

	b.maybeReact(context.Background(), "123", "12345", "100", "great news!", nil, testutil.TestLogger())
	b.wg.Wait()

	assert.Equal(t, int32(1), fake.calls.Load())
	require.Len(t, captured.Reaction, 1)
	assert.Equal(t, "👍", captured.Reaction[0].Emoji)
	mockAPI.AssertExpectations(t)
}

func TestMaybeReact_NoneDecision_NoReaction(t *testing.T) {
	fake := &fakeReactorAgent{result: &reactor.Result{Emoji: ""}}
	b, mockAPI, _ := setupReactionBot(t, fake)

	b.maybeReact(context.Background(), "123", "12345", "100", "meh", nil, testutil.TestLogger())
	b.wg.Wait()

	assert.Equal(t, int32(1), fake.calls.Load())
	mockAPI.AssertNotCalled(t, "SetMessageReaction")
}

func TestMaybeReact_Disabled_NoAgentCall(t *testing.T) {
	fake := &fakeReactorAgent{result: &reactor.Result{Emoji: "👍"}}
	b, mockAPI, _ := setupReactionBot(t, fake)
	b.cfg.Agents.Reactor.Enabled = false

	b.maybeReact(context.Background(), "123", "12345", "100", "great news!", nil, testutil.TestLogger())
	b.wg.Wait()

	assert.Equal(t, int32(0), fake.calls.Load())
	mockAPI.AssertNotCalled(t, "SetMessageReaction")
}

func TestMaybeReact_NilAgent_NoPanic(t *testing.T) {
	b, mockAPI, _ := setupReactionBot(t, &fakeReactorAgent{})
	b.reactorAgent = nil

	b.maybeReact(context.Background(), "123", "12345", "100", "hi", nil, testutil.TestLogger())
	b.wg.Wait()

	mockAPI.AssertNotCalled(t, "SetMessageReaction")
}

func TestMaybeReact_AgentError_Swallowed(t *testing.T) {
	fake := &fakeReactorAgent{err: assert.AnError}
	b, mockAPI, _ := setupReactionBot(t, fake)

	b.maybeReact(context.Background(), "123", "12345", "100", "hi", nil, testutil.TestLogger())
	b.wg.Wait()

	assert.Equal(t, int32(1), fake.calls.Load())
	mockAPI.AssertNotCalled(t, "SetMessageReaction")
}

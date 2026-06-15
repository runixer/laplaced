package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/config"
	"github.com/runixer/laplaced/internal/storage"
	"github.com/runixer/laplaced/internal/testutil"
)

func TestAuthorizeSender(t *testing.T) {
	tests := []struct {
		name      string
		resolver  PrincipalResolver
		transport *stubTransport
		senderID  string
		want      senderVerdict
		wantErr   bool
	}{
		{
			name:      "simple mode allows allowlisted sender",
			transport: &stubTransport{kind: transportTelegram, allowed: map[string]bool{"42": true}},
			senderID:  "42",
			want:      verdictAllow,
		},
		{
			name:      "simple mode denies non-allowlisted sender",
			transport: &stubTransport{kind: transportTelegram, allowed: map[string]bool{"42": true}},
			senderID:  "99",
			want:      verdictDeny,
		},
		{
			name:      "SSO mode allows trusted sender with empty allowlist",
			resolver:  &fakeResolver{trusted: true},
			transport: &stubTransport{kind: transportMattermost, allowlistConfigured: false},
			senderID:  "u1",
			want:      verdictAllow,
		},
		{
			name:      "SSO mode denies untrusted sender",
			resolver:  &fakeResolver{trusted: false},
			transport: &stubTransport{kind: transportMattermost},
			senderID:  "u1",
			want:      verdictDeny,
		},
		{
			name:      "SSO mode subset filter admits listed trusted sender",
			resolver:  &fakeResolver{trusted: true},
			transport: &stubTransport{kind: transportMattermost, allowlistConfigured: true, allowed: map[string]bool{"u1": true}},
			senderID:  "u1",
			want:      verdictAllow,
		},
		{
			name:      "SSO mode subset filter rejects unlisted trusted sender",
			resolver:  &fakeResolver{trusted: true},
			transport: &stubTransport{kind: transportMattermost, allowlistConfigured: true, allowed: map[string]bool{"other": true}},
			senderID:  "u1",
			want:      verdictDeny,
		},
		{
			name:      "SSO mode propagates resolver error (fail closed)",
			resolver:  &fakeResolver{err: errors.New("mm down")},
			transport: &stubTransport{kind: transportMattermost},
			senderID:  "u1",
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Bot{transport: tt.transport, principalResolver: tt.resolver}
			got, err := b.authorizeSender(context.Background(), IncomingMessage{SenderID: tt.senderID})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.verdict)
		})
	}
}

// fakeBotResolver is a PrincipalResolver that also implements botClassifier, so
// authorizeSender's bot-allowlist branch is exercised. classifyCalls records
// whether ClassifyBot was consulted (it must not be for an SSO-trusted sender).
type fakeBotResolver struct {
	trusted      bool
	isBot        bool
	allowlisted  bool
	err          error
	classifyErr  error
	invalidated  int
	classifyCall int
}

func (f *fakeBotResolver) Resolve(context.Context, string) (*storage.PrincipalInput, error) {
	return nil, f.err
}
func (f *fakeBotResolver) IsTrusted(context.Context, string) (bool, error) { return f.trusted, f.err }
func (f *fakeBotResolver) Invalidate(string)                               { f.invalidated++ }
func (f *fakeBotResolver) ClassifyBot(context.Context, string) (bool, bool, error) {
	f.classifyCall++
	return f.isBot, f.allowlisted, f.classifyErr
}

func TestAuthorizeSender_BotClassification(t *testing.T) {
	tr := func() *stubTransport { return &stubTransport{kind: transportMattermost} }
	tests := []struct {
		name            string
		resolver        *fakeBotResolver
		wantVerdict     senderVerdict
		wantIsBot       bool
		wantErr         bool
		wantInvalidated int
		wantClassified  int
	}{
		{
			name:           "allowlisted bot is admitted, not invalidated",
			resolver:       &fakeBotResolver{trusted: false, isBot: true, allowlisted: true},
			wantVerdict:    verdictAllow,
			wantIsBot:      true,
			wantClassified: 1,
		},
		{
			name:           "non-allowlisted bot is ignored silently, not invalidated",
			resolver:       &fakeBotResolver{trusted: false, isBot: true, allowlisted: false},
			wantVerdict:    verdictIgnore,
			wantIsBot:      true,
			wantClassified: 1,
		},
		{
			name:            "untrusted human is denied and invalidated",
			resolver:        &fakeBotResolver{trusted: false, isBot: false},
			wantVerdict:     verdictDeny,
			wantInvalidated: 1,
			wantClassified:  1,
		},
		{
			name:           "trusted SSO human is allowed without consulting ClassifyBot",
			resolver:       &fakeBotResolver{trusted: true},
			wantVerdict:    verdictAllow,
			wantClassified: 0,
		},
		{
			name:     "ClassifyBot error is propagated",
			resolver: &fakeBotResolver{trusted: false, classifyErr: errors.New("mm down")},
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Bot{transport: tr(), principalResolver: tt.resolver}
			got, err := b.authorizeSender(context.Background(), IncomingMessage{SenderID: "x"})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantVerdict, got.verdict)
			assert.Equal(t, tt.wantIsBot, got.isBot)
			assert.Equal(t, tt.wantInvalidated, tt.resolver.invalidated)
			assert.Equal(t, tt.wantClassified, tt.resolver.classifyCall)
		})
	}
}

// A denial drops the sender's cached profile (via the invalidator upgrade) so a
// just-migrated SSO account recovers on its next message instead of being denied
// off a stale cache. A trusted sender is never invalidated. Regression guard for
// the stale-GetUser-cache bug.
func TestAuthorizeSender_InvalidatesCacheOnDenialOnly(t *testing.T) {
	t.Run("denied sender is invalidated", func(t *testing.T) {
		r := &fakeResolver{trusted: false}
		b := &Bot{transport: &stubTransport{kind: transportMattermost}, principalResolver: r}
		got, err := b.authorizeSender(context.Background(), IncomingMessage{SenderID: "u1"})
		require.NoError(t, err)
		assert.Equal(t, verdictDeny, got.verdict)
		assert.Equal(t, 1, r.invalidated, "denied sender's stale profile must be dropped")
	})

	t.Run("trusted sender is not invalidated", func(t *testing.T) {
		r := &fakeResolver{trusted: true}
		b := &Bot{transport: &stubTransport{kind: transportMattermost}, principalResolver: r}
		_, err := b.authorizeSender(context.Background(), IncomingMessage{SenderID: "u1"})
		require.NoError(t, err)
		assert.Equal(t, 0, r.invalidated)
	})
}

func TestNotifyAccessDenied(t *testing.T) {
	newBot := func(transport *stubTransport, pr *config.PrincipalResolverConfig, resolver PrincipalResolver) *Bot {
		cfg := &config.Config{}
		cfg.Bot.Language = "en"
		cfg.Mattermost.PrincipalResolver = pr
		return &Bot{
			cfg:               cfg,
			transport:         transport,
			translator:        testutil.TestTranslator(t),
			logger:            testutil.TestLogger(),
			principalResolver: resolver,
		}
	}
	im := IncomingMessage{ConversationID: "dm1", SenderID: "u1", ThreadRoot: "p1"}

	t.Run("no-op in simple mode (no resolver)", func(t *testing.T) {
		tr := &stubTransport{kind: transportMattermost}
		b := newBot(tr, nil, nil)
		b.notifyAccessDenied(context.Background(), im)
		assert.Empty(t, tr.sent, "simple mode must stay silent")
	})

	t.Run("sends neutral i18n default when no override", func(t *testing.T) {
		tr := &stubTransport{kind: transportMattermost}
		b := newBot(tr, &config.PrincipalResolverConfig{}, &fakeResolver{})
		b.notifyAccessDenied(context.Background(), im)
		require.Len(t, tr.sent, 1)
		assert.Equal(t, "No access to this bot.", tr.sent[0].Text)
		assert.Equal(t, "dm1", tr.sent[0].ConversationID)
		assert.Equal(t, "p1", tr.sent[0].ThreadRoot)
	})

	t.Run("uses config override verbatim", func(t *testing.T) {
		tr := &stubTransport{kind: transportMattermost}
		b := newBot(tr, &config.PrincipalResolverConfig{AccessDeniedMessage: "Sign in via SSO first."}, &fakeResolver{})
		b.notifyAccessDenied(context.Background(), im)
		require.Len(t, tr.sent, 1)
		assert.Equal(t, "Sign in via SSO first.", tr.sent[0].Text)
	})

	t.Run("deduplicates per sender", func(t *testing.T) {
		tr := &stubTransport{kind: transportMattermost}
		b := newBot(tr, &config.PrincipalResolverConfig{}, &fakeResolver{})
		b.notifyAccessDenied(context.Background(), im)
		b.notifyAccessDenied(context.Background(), im)
		assert.Len(t, tr.sent, 1, "denied sender is notified at most once per process")
	})
}

// handleIncomingTestBot wires a Mattermost-kind bot with the given resolver and a
// non-firing grouper (turnWait time.Hour), so HandleIncoming's access-control
// branches are observable via the transport's sent buffer and the mock store.
func handleIncomingTestBot(t *testing.T, resolver PrincipalResolver, tr *stubTransport) (*Bot, *testutil.MockStorage) {
	t.Helper()
	mockStore := new(testutil.MockStorage)
	testutil.SetupDefaultMocks(mockStore)
	cfg := &config.Config{}
	cfg.Bot.Language = "en"
	cfg.Mattermost.PrincipalResolver = &config.PrincipalResolverConfig{}
	b := &Bot{
		cfg:               cfg,
		transport:         tr,
		principalResolver: resolver,
		scopeRepo:         mockStore,
		userRepo:          mockStore,
		msgRepo:           mockStore,
		peopleRepo:        mockStore,
		translator:        testutil.TestTranslator(t),
		logger:            testutil.TestLogger(),
	}
	b.messageGrouper = NewMessageGrouper(b, testutil.TestLogger(), time.Hour, func(context.Context, *MessageGroup) {})
	return b, mockStore
}

func TestHandleIncoming_DMAccessGate(t *testing.T) {
	t.Run("untrusted DM sender is denied and not stored", func(t *testing.T) {
		tr := &stubTransport{kind: transportMattermost}
		b, mockStore := handleIncomingTestBot(t, &fakeResolver{trusted: false}, tr)

		b.HandleIncoming(IncomingMessage{IsDirect: true, ConversationID: "dm1", SenderID: "local1", Text: "hi"})

		require.Len(t, tr.sent, 1, "denied DM sender gets a notice")
		mockStore.AssertNotCalled(t, "AddMessageToHistory", mock.Anything, mock.Anything)
		mockStore.AssertNotCalled(t, "UpsertUser", mock.Anything)
	})

	t.Run("trusted DM sender is admitted (no denial, no passive store)", func(t *testing.T) {
		tr := &stubTransport{kind: transportMattermost}
		resolver := &fakeResolver{trusted: true, out: &storage.PrincipalInput{ADLogin: "j.doe"}}
		b, mockStore := handleIncomingTestBot(t, resolver, tr)
		mockStore.On("GetIdentity", transportMattermost, "saml1").Return((*storage.Identity)(nil), nil)
		mockStore.On("GetOrCreatePrincipal", mock.Anything).Return(storage.ScopeID("scope-uuid"), true, nil)
		mockStore.On("PutIdentity", transportMattermost, "saml1", storage.ScopeID("scope-uuid")).Return(nil)
		mockStore.On("UpsertUser", mock.Anything).Return(nil)

		b.HandleIncoming(IncomingMessage{IsDirect: true, ConversationID: "dm1", SenderID: "saml1", Text: "hi"})

		assert.Empty(t, tr.sent, "trusted DM sender gets no denial")
		mockStore.AssertNotCalled(t, "AddMessageToHistory", mock.Anything, mock.Anything)
	})
}

// routedResolver answers per sender id so one channel can carry both a bot and a
// human. A sender in botAllow is a bot (value = allowlisted); a sender in
// trustedHumans is a trusted SSO human. Anyone else is an untrusted human.
type routedResolver struct {
	botAllow      map[string]bool
	trustedHumans map[string]bool
}

func (r *routedResolver) Resolve(context.Context, string) (*storage.PrincipalInput, error) {
	return nil, nil // bots/untrusted → isolated passthrough scope
}
func (r *routedResolver) IsTrusted(_ context.Context, id string) (bool, error) {
	return r.trustedHumans[id], nil
}
func (r *routedResolver) Invalidate(string) {}
func (r *routedResolver) ClassifyBot(_ context.Context, id string) (bool, bool, error) {
	allow, isBot := r.botAllow[id]
	return isBot, allow, nil
}

func TestHandleIncoming_BotAccessGate(t *testing.T) {
	t.Run("allowlisted bot mention replies, no denial", func(t *testing.T) {
		tr := &stubTransport{kind: transportMattermost}
		b, mockStore := handleIncomingTestBot(t, &routedResolver{botAllow: map[string]bool{"bot1": true}}, tr)
		mockStore.On("GetOrCreateChannel", transportMattermost, "chan1", mock.Anything).Return(storage.ScopeID("chan-scope"), nil)
		mockStore.On("UpsertUser", mock.Anything).Return(nil)

		b.HandleIncoming(IncomingMessage{
			IsDirect: false, Mention: true, ConversationID: "chan1", ThreadRoot: "t1",
			SenderID: "bot1", SenderDisplay: "@bot1", Text: "@laplaced summary?",
		})

		assert.Empty(t, tr.sent, "allowlisted bot gets no denial")
		mockStore.AssertNotCalled(t, "AddMessageToHistory", mock.Anything, mock.Anything)
	})

	t.Run("non-allowlisted bot mention is ignored silently, stored passively", func(t *testing.T) {
		tr := &stubTransport{kind: transportMattermost}
		b, mockStore := handleIncomingTestBot(t, &routedResolver{botAllow: map[string]bool{"bot2": false}}, tr)
		mockStore.On("GetOrCreateChannel", transportMattermost, "chan1", mock.Anything).Return(storage.ScopeID("chan-scope"), nil)
		mockStore.On("UpsertUser", mock.Anything).Return(nil)
		mockStore.On("AddMessageToHistory", storage.ScopeID("chan-scope"), mock.Anything).Return(nil)
		mockStore.On("FindPersonByExternalID", storage.ScopeID("chan-scope"), transportMattermost, "bot2").Return((*storage.Person)(nil), nil)
		mockStore.On("AddPerson", mock.Anything).Return(int64(1), nil)

		b.HandleIncoming(IncomingMessage{
			IsDirect: false, Mention: true, ConversationID: "chan1", ThreadRoot: "t1",
			SenderID: "bot2", SenderDisplay: "@bot2", Text: "@laplaced hi",
		})

		assert.Empty(t, tr.sent, "non-allowlisted bot gets NO access-denied notice")
		mockStore.AssertCalled(t, "AddMessageToHistory", storage.ScopeID("chan-scope"), mock.Anything)
	})

	t.Run("allowlisted bot DM is admitted on an isolated passthrough scope", func(t *testing.T) {
		tr := &stubTransport{kind: transportMattermost}
		b, mockStore := handleIncomingTestBot(t, &routedResolver{botAllow: map[string]bool{"bot1": true}}, tr)
		mockStore.On("GetIdentity", transportMattermost, "bot1").Return((*storage.Identity)(nil), nil)
		mockStore.On("PutIdentity", transportMattermost, "bot1", mock.Anything).Return(nil)
		mockStore.On("UpsertUser", mock.Anything).Return(nil)

		b.HandleIncoming(IncomingMessage{IsDirect: true, ConversationID: "dm1", SenderID: "bot1", Text: "hi"})

		assert.Empty(t, tr.sent, "allowlisted bot DM gets no denial")
		mockStore.AssertNotCalled(t, "AddMessageToHistory", mock.Anything, mock.Anything)
	})
}

func TestHandleIncoming_BotLoopGuard(t *testing.T) {
	channelMsg := func(sender string) IncomingMessage {
		return IncomingMessage{
			IsDirect: false, Mention: true, ConversationID: "chan1", ThreadRoot: "t1",
			SenderID: sender, SenderDisplay: "@" + sender, Text: "@laplaced ping",
		}
	}

	t.Run("suppresses bot reply past max chain depth", func(t *testing.T) {
		tr := &stubTransport{kind: transportMattermost}
		b, mockStore := handleIncomingTestBot(t, &routedResolver{botAllow: map[string]bool{"bot1": true}}, tr)
		b.cfg.Mattermost.PrincipalResolver.MaxBotChainDepth = 2
		mockStore.On("GetOrCreateChannel", transportMattermost, "chan1", mock.Anything).Return(storage.ScopeID("chan-scope"), nil)
		mockStore.On("UpsertUser", mock.Anything).Return(nil)
		// Only the suppressed (3rd) message is passive-stored.
		mockStore.On("AddMessageToHistory", storage.ScopeID("chan-scope"), mock.Anything).Return(nil)
		mockStore.On("FindPersonByExternalID", storage.ScopeID("chan-scope"), transportMattermost, "bot1").Return((*storage.Person)(nil), nil)
		mockStore.On("AddPerson", mock.Anything).Return(int64(1), nil)

		b.HandleIncoming(channelMsg("bot1")) // depth 1 → reply
		b.HandleIncoming(channelMsg("bot1")) // depth 2 → reply
		b.HandleIncoming(channelMsg("bot1")) // depth 3 > 2 → suppressed, passive-stored

		assert.Empty(t, tr.sent, "loop guard suppresses silently — no denial notice")
		mockStore.AssertNumberOfCalls(t, "AddMessageToHistory", 1)
	})

	t.Run("authorized human resets the chain", func(t *testing.T) {
		tr := &stubTransport{kind: transportMattermost}
		resolver := &routedResolver{
			botAllow:      map[string]bool{"bot1": true},
			trustedHumans: map[string]bool{"human1": true},
		}
		b, mockStore := handleIncomingTestBot(t, resolver, tr)
		b.cfg.Mattermost.PrincipalResolver.MaxBotChainDepth = 2
		mockStore.On("GetOrCreateChannel", transportMattermost, "chan1", mock.Anything).Return(storage.ScopeID("chan-scope"), nil)
		mockStore.On("UpsertUser", mock.Anything).Return(nil)
		// No message should be passive-stored: every one replies (human resets the budget).
		mockStore.On("AddMessageToHistory", storage.ScopeID("chan-scope"), mock.Anything).Return(nil)

		b.HandleIncoming(channelMsg("bot1"))   // depth 1 → reply
		b.HandleIncoming(channelMsg("bot1"))   // depth 2 → reply
		b.HandleIncoming(channelMsg("human1")) // human → reset
		b.HandleIncoming(channelMsg("bot1"))   // depth 1 again → reply (would be 3/suppressed without reset)

		assert.Empty(t, tr.sent)
		mockStore.AssertNotCalled(t, "AddMessageToHistory", mock.Anything, mock.Anything)
	})
}

func TestHandleIncoming_ChannelMentionGate(t *testing.T) {
	t.Run("untrusted mention is denied but stored passively", func(t *testing.T) {
		tr := &stubTransport{kind: transportMattermost}
		b, mockStore := handleIncomingTestBot(t, &fakeResolver{trusted: false}, tr)
		mockStore.On("GetOrCreateChannel", transportMattermost, "chan1", mock.Anything).Return(storage.ScopeID("chan-scope"), nil)
		mockStore.On("UpsertUser", mock.Anything).Return(nil)
		mockStore.On("AddMessageToHistory", storage.ScopeID("chan-scope"), mock.Anything).Return(nil)
		mockStore.On("FindPersonByExternalID", storage.ScopeID("chan-scope"), transportMattermost, "local1").Return((*storage.Person)(nil), nil)
		mockStore.On("AddPerson", mock.Anything).Return(int64(1), nil)

		b.HandleIncoming(IncomingMessage{
			IsDirect: false, Mention: true, ConversationID: "chan1",
			SenderID: "local1", SenderDisplay: "@local1", Text: "@bot hi",
		})

		require.Len(t, tr.sent, 1, "unauthorized mention gets a notice")
		mockStore.AssertCalled(t, "AddMessageToHistory", storage.ScopeID("chan-scope"), mock.Anything)
	})

	t.Run("trusted mention replies (no denial, not passive-stored)", func(t *testing.T) {
		tr := &stubTransport{kind: transportMattermost}
		b, mockStore := handleIncomingTestBot(t, &fakeResolver{trusted: true}, tr)
		mockStore.On("GetOrCreateChannel", transportMattermost, "chan1", mock.Anything).Return(storage.ScopeID("chan-scope"), nil)
		mockStore.On("UpsertUser", mock.Anything).Return(nil)

		b.HandleIncoming(IncomingMessage{
			IsDirect: false, Mention: true, ConversationID: "chan1",
			SenderID: "saml1", SenderDisplay: "@saml1", Text: "@bot hi",
		})

		assert.Empty(t, tr.sent, "authorized mention gets no denial")
		mockStore.AssertNotCalled(t, "AddMessageToHistory", mock.Anything, mock.Anything)
	})

	t.Run("authorized sender clears a prior denial dedup", func(t *testing.T) {
		tr := &stubTransport{kind: transportMattermost}
		b, mockStore := handleIncomingTestBot(t, &fakeResolver{trusted: true}, tr)
		// As if the sender was denied before migrating: the dedup flag is set.
		b.accessDeniedNotified.Store("saml1", true)
		mockStore.On("GetOrCreateChannel", transportMattermost, "chan1", mock.Anything).Return(storage.ScopeID("chan-scope"), nil)
		mockStore.On("UpsertUser", mock.Anything).Return(nil)

		b.HandleIncoming(IncomingMessage{
			IsDirect: false, Mention: true, ConversationID: "chan1",
			SenderID: "saml1", SenderDisplay: "@saml1", Text: "@bot hi",
		})

		_, stillFlagged := b.accessDeniedNotified.Load("saml1")
		assert.False(t, stillFlagged, "authorized sender's denial dedup is cleared so a later denial re-notifies")
	})
}

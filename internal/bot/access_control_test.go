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
		want      bool
		wantErr   bool
	}{
		{
			name:      "simple mode allows allowlisted sender",
			transport: &stubTransport{kind: transportTelegram, allowed: map[string]bool{"42": true}},
			senderID:  "42",
			want:      true,
		},
		{
			name:      "simple mode denies non-allowlisted sender",
			transport: &stubTransport{kind: transportTelegram, allowed: map[string]bool{"42": true}},
			senderID:  "99",
			want:      false,
		},
		{
			name:      "SSO mode allows trusted sender with empty allowlist",
			resolver:  &fakeResolver{trusted: true},
			transport: &stubTransport{kind: transportMattermost, allowlistConfigured: false},
			senderID:  "u1",
			want:      true,
		},
		{
			name:      "SSO mode denies untrusted sender",
			resolver:  &fakeResolver{trusted: false},
			transport: &stubTransport{kind: transportMattermost},
			senderID:  "u1",
			want:      false,
		},
		{
			name:      "SSO mode subset filter admits listed trusted sender",
			resolver:  &fakeResolver{trusted: true},
			transport: &stubTransport{kind: transportMattermost, allowlistConfigured: true, allowed: map[string]bool{"u1": true}},
			senderID:  "u1",
			want:      true,
		},
		{
			name:      "SSO mode subset filter rejects unlisted trusted sender",
			resolver:  &fakeResolver{trusted: true},
			transport: &stubTransport{kind: transportMattermost, allowlistConfigured: true, allowed: map[string]bool{"other": true}},
			senderID:  "u1",
			want:      false,
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
			assert.Equal(t, tt.want, got)
		})
	}
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
		resolver := &fakeResolver{trusted: true, out: &storage.PrincipalInput{ADLogin: "k.gruzdev"}}
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
}

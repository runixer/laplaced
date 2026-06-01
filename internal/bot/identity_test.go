package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/storage"
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

// fakeResolver is a test PrincipalResolver: returns a fixed result, or an error.
type fakeResolver struct {
	out    *storage.PrincipalInput
	err    error
	called int
}

func (f *fakeResolver) Resolve(_ context.Context, _ string) (*storage.PrincipalInput, error) {
	f.called++
	return f.out, f.err
}

func TestResolveScopeID_TelegramPassthrough(t *testing.T) {
	mockStore := new(testutil.MockStorage)

	id, err := resolveScopeID(context.Background(), mockStore, nil, transportTelegram, IncomingMessage{SenderID: "123456", IsDirect: true})
	require.NoError(t, err)
	assert.Equal(t, storage.PassthroughScopeID("telegram", "123456"), id)

	// Passthrough must not touch the identity store on the home path.
	mockStore.AssertNotCalled(t, "ResolveScope")
	mockStore.AssertNotCalled(t, "GetIdentity")
	mockStore.AssertExpectations(t)
}

func TestResolveScopeID_TelegramNonNumericIsPassthrough(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	id, err := resolveScopeID(context.Background(), mockStore, nil, transportTelegram, IncomingMessage{SenderID: "not-an-int", IsDirect: true})
	require.NoError(t, err)
	assert.Equal(t, storage.PassthroughScopeID("telegram", "not-an-int"), id)
}

func TestResolveScopeID_MattermostDMNoResolverIsPassthrough(t *testing.T) {
	mockStore := new(testutil.MockStorage)

	// A DM on a transport with no resolver wired is deterministic passthrough —
	// the same scope id ResolveScope used to mint, but with no identity state and
	// no principal linkage.
	id, err := resolveScopeID(context.Background(), mockStore, nil, transportMattermost, IncomingMessage{
		SenderID:       "u26charstring",
		ConversationID: "dm26charchannel",
		IsDirect:       true,
	})
	require.NoError(t, err)
	assert.Equal(t, storage.PassthroughScopeID(transportMattermost, "u26charstring"), id)
	mockStore.AssertNotCalled(t, "GetIdentity")
	mockStore.AssertNotCalled(t, "GetOrCreatePrincipal")
	mockStore.AssertExpectations(t)
}

func TestResolveScopeID_ChannelGetsChannelScope(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	// A channel is its own scope, keyed by the conversation; never principal-resolved.
	mockStore.On("GetOrCreateChannel", transportMattermost, "chan26charchannel", "Release Team").
		Return(storage.ScopeID("scope-42"), nil)

	// Even with a resolver wired, channels bypass it.
	resolver := &fakeResolver{}
	id, err := resolveScopeID(context.Background(), mockStore, resolver, transportMattermost, IncomingMessage{
		SenderID:            "u26charstring",
		ConversationID:      "chan26charchannel",
		ConversationDisplay: "Release Team",
		IsDirect:            false,
	})
	require.NoError(t, err)
	assert.Equal(t, storage.ScopeID("scope-42"), id)
	assert.Zero(t, resolver.called, "channels must not invoke the principal resolver")
	mockStore.AssertExpectations(t)
}

func TestResolveScopeID_DMWithResolver_LinksPrincipal(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	pin := storage.PrincipalInput{ADLogin: "k.gruzdev", DisplayName: "K G"}
	resolver := &fakeResolver{out: &pin}

	// First sight: no identity yet → resolve → get-or-create principal → link.
	mockStore.On("GetIdentity", transportMattermost, "u26charstring").Return(nil, nil)
	mockStore.On("GetOrCreatePrincipal", pin).Return(storage.ScopeID("principal-1"), true, nil)
	mockStore.On("PutIdentity", transportMattermost, "u26charstring", storage.ScopeID("principal-1")).Return(nil)

	id, err := resolveScopeID(context.Background(), mockStore, resolver, transportMattermost, IncomingMessage{
		SenderID: "u26charstring",
		IsDirect: true,
	})
	require.NoError(t, err)
	assert.Equal(t, storage.ScopeID("principal-1"), id)
	assert.Equal(t, 1, resolver.called)
	mockStore.AssertExpectations(t)
}

func TestResolveScopeID_DMWithResolver_ReusesExistingIdentity(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	resolver := &fakeResolver{out: &storage.PrincipalInput{ADLogin: "should.not.matter"}}

	// A known handle short-circuits: reuse the mapped scope, never re-resolve.
	mockStore.On("GetIdentity", transportMattermost, "u26charstring").
		Return(&storage.Identity{Transport: transportMattermost, NativeID: "u26charstring", ScopeID: "principal-9"}, nil)

	id, err := resolveScopeID(context.Background(), mockStore, resolver, transportMattermost, IncomingMessage{
		SenderID: "u26charstring",
		IsDirect: true,
	})
	require.NoError(t, err)
	assert.Equal(t, storage.ScopeID("principal-9"), id)
	assert.Zero(t, resolver.called, "known identity must not re-invoke the resolver")
	mockStore.AssertNotCalled(t, "GetOrCreatePrincipal")
	mockStore.AssertExpectations(t)
}

func TestResolveScopeID_DMWithResolver_UnlinkableGetsIsolatedScope(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	// Resolver returns nil (local account / untrusted) → isolated passthrough scope,
	// NEVER linked to a principal.
	resolver := &fakeResolver{out: nil}

	mockStore.On("GetIdentity", transportMattermost, "local-user").Return(nil, nil)
	isolated := storage.PassthroughScopeID(transportMattermost, "local-user")
	mockStore.On("PutIdentity", transportMattermost, "local-user", isolated).Return(nil)

	id, err := resolveScopeID(context.Background(), mockStore, resolver, transportMattermost, IncomingMessage{
		SenderID: "local-user",
		IsDirect: true,
	})
	require.NoError(t, err)
	assert.Equal(t, isolated, id)
	assert.Equal(t, 1, resolver.called)
	mockStore.AssertNotCalled(t, "GetOrCreatePrincipal")
	mockStore.AssertExpectations(t)
}

func TestResolveScopeID_DMWithResolver_ResolveErrorPropagates(t *testing.T) {
	mockStore := new(testutil.MockStorage)
	resolver := &fakeResolver{err: errors.New("mm down")}
	mockStore.On("GetIdentity", transportMattermost, "u1").Return(nil, nil)

	_, err := resolveScopeID(context.Background(), mockStore, resolver, transportMattermost, IncomingMessage{
		SenderID: "u1",
		IsDirect: true,
	})
	require.Error(t, err)
	mockStore.AssertNotCalled(t, "PutIdentity")
}

package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/internal/mattermost"
	"github.com/runixer/laplaced/internal/testutil"
)

// fakeMMUser is a stub mmUserLookup returning a fixed user (or error). It counts
// InvalidateUser calls so wiring can be asserted.
type fakeMMUser struct {
	user        *mattermost.User
	err         error
	invalidated int
}

func (f *fakeMMUser) GetUser(_ context.Context, _ string) (*mattermost.User, error) {
	return f.user, f.err
}

func (f *fakeMMUser) InvalidateUser(_ string) { f.invalidated++ }

func TestMattermostPrincipalResolver_TrustGate(t *testing.T) {
	tests := []struct {
		name        string
		user        *mattermost.User
		trusted     []string
		wantNil     bool   // expect isolation (nil principal)
		wantADLogin string // expected lowercased ad_login when linked
	}{
		{
			name:        "SAML account links by lowercased auth_data",
			user:        &mattermost.User{AuthService: "saml", AuthData: "J.Doe", Email: "j.doe@corp", FirstName: "J", LastName: "D"},
			wantADLogin: "j.doe",
		},
		{
			name:    "local account is never linked",
			user:    &mattermost.User{AuthService: "", AuthData: "", Email: "anything@corp"},
			wantNil: true,
		},
		{
			// The proven trap: a local account self-claims an email in a
			// trusted-looking domain. auth_service=="" must isolate regardless of email.
			name:    "local account with a trusted-looking email is never linked",
			user:    &mattermost.User{AuthService: "", AuthData: "localuser", Email: "someone@example.com"},
			wantNil: true,
		},
		{
			name:    "trusted-services allowlist excludes other services",
			user:    &mattermost.User{AuthService: "gitlab", AuthData: "someone"},
			trusted: []string{"saml"},
			wantNil: true,
		},
		{
			name:        "trusted-services allowlist admits a listed service",
			user:        &mattermost.User{AuthService: "saml", AuthData: "Alice"},
			trusted:     []string{"saml"},
			wantADLogin: "alice",
		},
		{
			name:    "trusted service but empty auth_data isolates",
			user:    &mattermost.User{AuthService: "saml", AuthData: "   "},
			wantNil: true,
		},
		{
			// auth_data shaped like an email means the IdP maps NameID to an
			// email — wrong join key + would break the never-link-by-email rule.
			name:    "email-shaped auth_data isolates",
			user:    &mattermost.User{AuthService: "saml", AuthData: "user@corp.example"},
			wantNil: true,
		},
		{
			// Config "SAML" must match the wire value "saml" (case-insensitive).
			name:        "trusted-services match is case-insensitive",
			user:        &mattermost.User{AuthService: "SAML", AuthData: "Bob"},
			trusted:     []string{"saml"},
			wantADLogin: "bob",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewMattermostPrincipalResolver(&fakeMMUser{user: tt.user}, tt.trusted, testutil.TestLogger())
			got, err := r.Resolve(context.Background(), "uid")
			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, got, "sender must be isolated, not linked")
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.wantADLogin, got.ADLogin)
			// Email is carried as an attribute but is never the link key.
			assert.Equal(t, tt.user.Email, got.Email)
			// objectGUID is left empty for now.
			assert.Empty(t, got.ObjectGUID)
		})
	}
}

func TestMattermostPrincipalResolver_GetUserErrorPropagates(t *testing.T) {
	r := NewMattermostPrincipalResolver(&fakeMMUser{err: errors.New("503")}, nil, testutil.TestLogger())
	_, err := r.Resolve(context.Background(), "uid")
	require.Error(t, err)
}

// IsTrusted is the access gate: it tracks auth_service trust only and is looser
// than Resolve — an SSO account with an unusable auth_data (email-shaped/empty)
// is still trusted for access, even though Resolve isolates it (nil principal).
func TestMattermostPrincipalResolver_IsTrusted(t *testing.T) {
	tests := []struct {
		name    string
		user    *mattermost.User
		trusted []string
		want    bool
	}{
		{"SAML account trusted", &mattermost.User{AuthService: "saml", AuthData: "j.doe"}, nil, true},
		{"local account not trusted", &mattermost.User{AuthService: "", AuthData: "x"}, nil, false},
		{"untrusted service excluded", &mattermost.User{AuthService: "gitlab", AuthData: "x"}, []string{"saml"}, false},
		{"listed service trusted", &mattermost.User{AuthService: "saml", AuthData: "x"}, []string{"saml"}, true},
		{"trusted match is case-insensitive", &mattermost.User{AuthService: "SAML", AuthData: "x"}, []string{"saml"}, true},
		// Access is broader than linkability: trusted for access, but Resolve would isolate.
		{"SSO with email-shaped auth_data still trusted for access", &mattermost.User{AuthService: "saml", AuthData: "a@b.c"}, nil, true},
		{"SSO with empty auth_data still trusted for access", &mattermost.User{AuthService: "saml", AuthData: "  "}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewMattermostPrincipalResolver(&fakeMMUser{user: tt.user}, tt.trusted, testutil.TestLogger())
			got, err := r.IsTrusted(context.Background(), "uid")
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMattermostPrincipalResolver_IsTrusted_GetUserErrorPropagates(t *testing.T) {
	r := NewMattermostPrincipalResolver(&fakeMMUser{err: errors.New("503")}, nil, testutil.TestLogger())
	_, err := r.IsTrusted(context.Background(), "uid")
	require.Error(t, err)
}

// Invalidate must forward to the client so a denied (pre-migration) profile can
// be dropped — the wiring that lets an SSO-migrated account recover without a
// process restart. Regression guard for the stale-GetUser-cache bug.
func TestMattermostPrincipalResolver_InvalidateForwardsToClient(t *testing.T) {
	f := &fakeMMUser{user: &mattermost.User{AuthService: ""}}
	r := NewMattermostPrincipalResolver(f, nil, testutil.TestLogger())
	r.Invalidate("uid")
	assert.Equal(t, 1, f.invalidated, "Invalidate must drop the client's cached profile")
}

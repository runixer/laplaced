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

// fakeMMUser is a stub mmUserLookup returning a fixed user (or error).
type fakeMMUser struct {
	user *mattermost.User
	err  error
}

func (f *fakeMMUser) GetUser(_ context.Context, _ string) (*mattermost.User, error) {
	return f.user, f.err
}

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
			user:        &mattermost.User{AuthService: "saml", AuthData: "K.Gruzdev", Email: "k.gruzdev@corp", FirstName: "K", LastName: "G"},
			wantADLogin: "k.gruzdev",
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

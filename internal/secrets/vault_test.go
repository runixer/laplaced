package secrets

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runixer/laplaced/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vaultStub serves the minimal Vault HTTP surface this package uses: auth
// login, KV v2, KV v1 and a generic logical read. The last requested login
// path is recorded so tests can assert the configured auth mount path was used.
func vaultStub(loginPath *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/login"):
			*loginPath = r.URL.Path
			_, _ = io.WriteString(w, `{"auth":{"client_token":"test-token","lease_duration":3600}}`)
		case strings.Contains(r.URL.Path, "/data/"): // KV v2
			_, _ = io.WriteString(w, `{"data":{"data":{"api_key":"KV2VAL"},"metadata":{"version":1}}}`)
		default: // KV v1 / generic logical read (flat data)
			_, _ = io.WriteString(w, `{"data":{"token":"FLATVAL"}}`)
		}
	}))
}

func newTokenProvider(t *testing.T, addr string) *Provider {
	t.Helper()
	t.Setenv("VAULT_TOKEN", "dev-token")
	p, err := New(context.Background(), config.VaultConfig{
		Address: addr,
		Auth:    config.VaultAuthConfig{Method: "token"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	return p
}

func TestNewTokenAuthAndGet(t *testing.T) {
	var loginPath string
	srv := vaultStub(&loginPath)
	defer srv.Close()
	p := newTokenProvider(t, srv.URL)

	// token auth performs no network login
	assert.Empty(t, loginPath)

	tests := []struct {
		name string
		ref  config.VaultRef
		want string
	}{
		{"kv2", config.VaultRef{Kind: "kv2", Mount: "secret", Path: "laplaced/dev", Key: "api_key"}, "KV2VAL"},
		{"kv1", config.VaultRef{Kind: "kv1", Mount: "legacy", Path: "laplaced", Key: "token"}, "FLATVAL"},
		{"raw", config.VaultRef{Kind: "raw", Mount: "database", Path: "creds/role", Key: "token"}, "FLATVAL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.Get(context.Background(), tt.ref)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetErrors(t *testing.T) {
	var loginPath string
	srv := vaultStub(&loginPath)
	defer srv.Close()
	p := newTokenProvider(t, srv.URL)

	t.Run("missing key", func(t *testing.T) {
		_, err := p.Get(context.Background(), config.VaultRef{Kind: "kv2", Mount: "secret", Path: "laplaced/dev", Key: "absent"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "absent")
	})
	t.Run("unsupported kind", func(t *testing.T) {
		_, err := p.Get(context.Background(), config.VaultRef{Kind: "bogus", Mount: "secret", Path: "x", Key: "y"})
		require.Error(t, err)
	})
}

func TestNewKubernetesUsesMountPath(t *testing.T) {
	var loginPath string
	srv := vaultStub(&loginPath)
	defer srv.Close()

	// kubernetes auth reads the service-account JWT from a file
	saTokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(saTokenFile, []byte("fake.jwt.token"), 0o600))

	_, err := New(context.Background(), config.VaultConfig{
		Address: srv.URL,
		Auth: config.VaultAuthConfig{
			Method:                  "kubernetes",
			Role:                    "laplaced",
			MountPath:               "kubernetes-2",
			ServiceAccountTokenPath: saTokenFile,
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	assert.Equal(t, "/v1/auth/kubernetes-2/login", loginPath)
}

func TestNewAppRoleUsesMountPath(t *testing.T) {
	var loginPath string
	srv := vaultStub(&loginPath)
	defer srv.Close()

	t.Setenv(appRoleSecretIDEnv, "secret-id-value")
	_, err := New(context.Background(), config.VaultConfig{
		Address: srv.URL,
		Auth: config.VaultAuthConfig{
			Method:    "approle",
			RoleID:    "role-id-value",
			MountPath: "approle-2",
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	assert.Equal(t, "/v1/auth/approle-2/login", loginPath)
}

func TestNewUnknownMethod(t *testing.T) {
	_, err := New(context.Background(), config.VaultConfig{
		Address: "http://127.0.0.1:0",
		Auth:    config.VaultAuthConfig{Method: "ldap"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported vault auth method")
}

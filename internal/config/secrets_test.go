package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVaultRef(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    VaultRef
		wantErr bool
	}{
		{"kv2 default", "vault:secret/laplaced/dev#api_key", VaultRef{"kv2", "secret", "laplaced/dev", "api_key"}, false},
		{"kv2 single-segment path", "vault:secret/foo#k", VaultRef{"kv2", "secret", "foo", "k"}, false},
		{"explicit kv1", "vault:kv1:legacy/laplaced#token", VaultRef{"kv1", "legacy", "laplaced", "token"}, false},
		{"raw deep path", "vault:raw:database/creds/role#password", VaultRef{"raw", "database", "creds/role", "password"}, false},
		{"not a ref", "plain-literal-token", VaultRef{}, true},
		{"missing #key", "vault:secret/foo", VaultRef{}, true},
		{"empty key", "vault:secret/foo#", VaultRef{}, true},
		{"unknown kind", "vault:bad:secret/foo#k", VaultRef{}, true},
		{"missing path", "vault:secret#k", VaultRef{}, true},
		{"empty mount", "vault:#k", VaultRef{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVaultRef(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// fakeProvider is a SecretProvider stub keyed by "mount/path#key".
type fakeProvider struct {
	vals map[string]string
	err  error
}

func (f *fakeProvider) Get(_ context.Context, ref VaultRef) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if v, ok := f.vals[ref.Mount+"/"+ref.Path+"#"+ref.Key]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}

func TestResolveSecrets(t *testing.T) {
	t.Run("replaces ref, leaves literal untouched", func(t *testing.T) {
		cfg := &Config{}
		cfg.OpenRouter.APIKey = "vault:secret/laplaced/dev#api_key"
		cfg.Telegram.Token = "123:literal-token"
		p := &fakeProvider{vals: map[string]string{"secret/laplaced/dev#api_key": "resolved-key"}}

		require.NoError(t, cfg.ResolveSecrets(context.Background(), p))
		assert.Equal(t, "resolved-key", cfg.OpenRouter.APIKey)
		assert.Equal(t, "123:literal-token", cfg.Telegram.Token)
	})

	t.Run("ref present but no provider errors", func(t *testing.T) {
		cfg := &Config{}
		cfg.OpenRouter.APIKey = "vault:secret/laplaced/dev#api_key"

		err := cfg.ResolveSecrets(context.Background(), nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "openrouter.api_key")
		assert.Contains(t, err.Error(), "no [vault] block")
	})

	t.Run("provider error is wrapped with field name", func(t *testing.T) {
		cfg := &Config{}
		cfg.Database.Postgres.Password = "vault:secret/laplaced/dev#pg_password"
		p := &fakeProvider{err: errors.New("boom")}

		err := cfg.ResolveSecrets(context.Background(), p)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "database.postgres.password")
		assert.Contains(t, err.Error(), "boom")
	})

	t.Run("malformed ref errors with field name even with provider", func(t *testing.T) {
		cfg := &Config{}
		cfg.Mattermost.BotToken = "vault:secret/foo" // missing #key
		p := &fakeProvider{vals: map[string]string{}}

		err := cfg.ResolveSecrets(context.Background(), p)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mattermost.bot_token")
	})

	t.Run("no refs is a no-op", func(t *testing.T) {
		cfg := &Config{}
		cfg.OpenRouter.APIKey = "literal"
		require.NoError(t, cfg.ResolveSecrets(context.Background(), nil))
		assert.Equal(t, "literal", cfg.OpenRouter.APIKey)
	})
}

// TestSecretFieldsRegistryNonNil guards against a typo'd pointer in the registry.
func TestSecretFieldsRegistryNonNil(t *testing.T) {
	cfg := &Config{}
	for _, f := range cfg.secretFields() {
		assert.NotEmptyf(t, f.name, "field has empty name")
		assert.NotNilf(t, f.ptr, "field %q has nil pointer", f.name)
	}
}

// TestSecretFieldsS3 verifies the S3 credentials are only registered when the
// capability block is present, and that they resolve from vault when set.
func TestSecretFieldsS3(t *testing.T) {
	t.Run("absent block registers no s3 fields", func(t *testing.T) {
		cfg := &Config{}
		for _, f := range cfg.secretFields() {
			assert.NotContains(t, f.name, "artifacts.s3")
		}
	})

	t.Run("present block resolves credentials", func(t *testing.T) {
		cfg := &Config{}
		cfg.Artifacts.S3 = &S3Config{
			Bucket:    "laplaced-dev",
			Endpoint:  "https://storage.yandexcloud.net",
			AccessKey: "vault:secret/ai/laplaced#s3_access_key",
			SecretKey: "vault:secret/ai/laplaced#s3_secret_key",
		}
		p := &fakeProvider{vals: map[string]string{
			"secret/ai/laplaced#s3_access_key": "AK",
			"secret/ai/laplaced#s3_secret_key": "SK",
		}}
		require.NoError(t, cfg.ResolveSecrets(context.Background(), p))
		assert.Equal(t, "AK", cfg.Artifacts.S3.AccessKey)
		assert.Equal(t, "SK", cfg.Artifacts.S3.SecretKey)
	})
}

func TestVaultConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		auth    VaultAuthConfig
		wantErr string // substring; "" = valid
	}{
		{"token", VaultAuthConfig{Method: "token"}, ""},
		{"kubernetes ok", VaultAuthConfig{Method: "kubernetes", Role: "laplaced", MountPath: "kubernetes-2"}, ""},
		{"kubernetes no role", VaultAuthConfig{Method: "kubernetes"}, "vault.auth.role"},
		{"approle ok", VaultAuthConfig{Method: "approle", RoleID: "rid"}, ""},
		{"approle no role_id", VaultAuthConfig{Method: "approle"}, "vault.auth.role_id"},
		{"empty method", VaultAuthConfig{}, "vault.auth.method is required"},
		{"bad method", VaultAuthConfig{Method: "ldap"}, "invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := (&VaultConfig{Auth: tt.auth}).validate()
			if tt.wantErr == "" {
				assert.Empty(t, errs)
				return
			}
			require.NotEmpty(t, errs)
			assert.Contains(t, errors.Join(errs...).Error(), tt.wantErr)
		})
	}
}

// TestValidateWiresVault confirms Config.Validate surfaces vault block errors.
func TestValidateWiresVault(t *testing.T) {
	cfg := &Config{}
	cfg.OpenRouter.APIKey = "k"
	cfg.Telegram.Token = "t"
	cfg.Database.Driver = "sqlite"
	cfg.Database.Path = "x.db"
	cfg.Vault = &VaultConfig{Auth: VaultAuthConfig{Method: "kubernetes"}} // missing role

	err := cfg.Validate()
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "vault.auth.role"))
}

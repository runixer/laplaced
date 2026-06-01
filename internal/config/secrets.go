package config

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// vaultRefPrefix marks a config value that should be resolved from Vault at
// startup instead of taken literally, e.g. "vault:secret/laplaced/dev#api_key".
const vaultRefPrefix = "vault:"

// validVaultKinds are the secret-engine flavours a reference may name. The
// engine determines how the read response is unwrapped (see VaultRef).
var validVaultKinds = []string{"kv2", "kv1", "raw"}

// VaultConfig enables pulling secrets from HashiCorp Vault. Its mere presence
// turns the feature on; the block carries only connection + auth. Which secret
// lives where (mount, path, engine) is encoded per-reference in the field value
// itself — see VaultRef / parseVaultRef — so different secrets can come from
// different mounts and engines without a global mount setting.
type VaultConfig struct {
	// Address of the Vault server. May be set here (often as ${VAULT_ADDR}) or
	// left empty to fall back to the standard VAULT_ADDR environment variable.
	Address string `yaml:"address" env:"VAULT_ADDR"`
	// Namespace for Vault Enterprise / HCP (optional).
	Namespace string          `yaml:"namespace" env:"VAULT_NAMESPACE"`
	Auth      VaultAuthConfig `yaml:"auth"`
}

// VaultAuthConfig selects how the bot authenticates to Vault.
type VaultAuthConfig struct {
	// Method selects the auth backend: "token" | "kubernetes" | "approle".
	Method string `yaml:"method" env:"LAPLACED_VAULT_AUTH_METHOD"`
	// Role is the Vault role for kubernetes auth.
	Role string `yaml:"role" env:"LAPLACED_VAULT_AUTH_ROLE"`
	// MountPath overrides the auth mount (e.g. "kubernetes-2"). Empty = the
	// method's own default mount ("kubernetes" / "approle").
	MountPath string `yaml:"mount_path" env:"LAPLACED_VAULT_AUTH_MOUNT_PATH"`
	// ServiceAccountTokenPath overrides the Kubernetes service-account JWT path
	// (optional; defaults to the in-cluster location).
	ServiceAccountTokenPath string `yaml:"service_account_token_path" env:"LAPLACED_VAULT_K8S_SA_TOKEN_PATH"`
	// RoleID for approle auth. The matching secret_id is read ONLY from the
	// LAPLACED_VAULT_APPROLE_SECRET_ID env var, never from a config literal.
	RoleID string `yaml:"role_id" env:"LAPLACED_VAULT_APPROLE_ROLE_ID"`
}

// VaultRef is a parsed "vault:" reference. Engine kind decides how a read
// response is unwrapped:
//   - kv2: read <mount>/data/<path>, return the value of <key> under .data
//   - kv1: read <mount>/<path>, return <key> from the flat map
//   - raw: generic logical read of <mount>/<path>, return <key> from .Data
//     (works for any engine; static reads only — leased/dynamic secrets are
//     not renewed here).
type VaultRef struct {
	Kind  string // "kv2" | "kv1" | "raw"
	Mount string // secrets-engine mount, e.g. "secret"
	Path  string // path under the mount, e.g. "laplaced/dev"
	Key   string // field within the secret
}

// SecretProvider fetches a single secret value for a parsed reference.
type SecretProvider interface {
	Get(ctx context.Context, ref VaultRef) (string, error)
}

// isVaultRef reports whether a config value is a Vault reference (vs a literal).
func isVaultRef(s string) bool { return strings.HasPrefix(s, vaultRefPrefix) }

// parseVaultRef parses "vault:[kind:]mount/path#key". A leading "kind:" is
// optional and defaults to "kv2". It returns an error for any value that has
// the vault: prefix but is malformed, so a typo'd reference fails loudly
// instead of being used as a literal secret.
func parseVaultRef(s string) (VaultRef, error) {
	body, ok := strings.CutPrefix(s, vaultRefPrefix)
	if !ok {
		return VaultRef{}, fmt.Errorf("not a vault reference: %q", s)
	}
	spec, key, ok := strings.Cut(body, "#")
	if !ok || key == "" {
		return VaultRef{}, fmt.Errorf("vault reference %q must be [kind:]mount/path#key (missing #key)", s)
	}
	// A ':' before the first '/' denotes the engine kind.
	kind := "kv2"
	if i := strings.IndexAny(spec, ":/"); i >= 0 && spec[i] == ':' {
		k := spec[:i]
		if !slices.Contains(validVaultKinds, k) {
			return VaultRef{}, fmt.Errorf("vault reference %q has unknown kind %q (want kv2|kv1|raw)", s, k)
		}
		kind, spec = k, spec[i+1:]
	}
	mount, path, ok := strings.Cut(spec, "/")
	if !ok || mount == "" || path == "" {
		return VaultRef{}, fmt.Errorf("vault reference %q must be [kind:]mount/path#key", s)
	}
	return VaultRef{Kind: kind, Mount: mount, Path: path, Key: key}, nil
}

// secretFields is the explicit registry of config fields that may hold a Vault
// reference. Listing them by pointer (rather than walking the struct by
// reflection) keeps resolution in the same explicit style as Validate and makes
// the set auditable. Add new secret fields here.
func (c *Config) secretFields() []struct {
	name string
	ptr  *string
} {
	return []struct {
		name string
		ptr  *string
	}{
		{"telegram.token", &c.Telegram.Token},
		{"openrouter.api_key", &c.OpenRouter.APIKey},
		{"mattermost.bot_token", &c.Mattermost.BotToken},
		{"database.postgres.password", &c.Database.Postgres.Password},
		{"server.auth.password", &c.Server.Auth.Password},
	}
}

// ResolveSecrets replaces any "vault:" reference held by a known secret field
// with the value fetched from the provider. Fields holding plain literals are
// left untouched. Call it after Load and before Validate.
//
// A reference present while provider is nil (no [vault] block configured) is an
// error rather than a silent miss — see the "config-driven data shape" incident
// class in CLAUDE.md.
func (c *Config) ResolveSecrets(ctx context.Context, provider SecretProvider) error {
	var errs []error
	for _, f := range c.secretFields() {
		if !isVaultRef(*f.ptr) {
			continue
		}
		ref, err := parseVaultRef(*f.ptr)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.name, err))
			continue
		}
		if provider == nil {
			errs = append(errs, fmt.Errorf("%s holds a vault reference but no [vault] block is configured", f.name))
			continue
		}
		val, err := provider.Get(ctx, ref)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolving %s from vault: %w", f.name, err))
			continue
		}
		*f.ptr = val
	}
	return errors.Join(errs...)
}

// validate checks the Vault block for required fields. Called from Config.Validate
// only when the block is present.
func (v *VaultConfig) validate() []error {
	var errs []error
	switch v.Auth.Method {
	case "token":
		// Token is read from $VAULT_TOKEN / ~/.vault-token at runtime; nothing
		// to require statically here.
	case "kubernetes":
		if v.Auth.Role == "" {
			errs = append(errs, errors.New("vault.auth.role is required for method \"kubernetes\""))
		}
	case "approle":
		if v.Auth.RoleID == "" {
			errs = append(errs, errors.New("vault.auth.role_id is required for method \"approle\""))
		}
	case "":
		errs = append(errs, errors.New("vault.auth.method is required when the [vault] block is present"))
	default:
		errs = append(errs, fmt.Errorf("vault.auth.method %q is invalid (want token|kubernetes|approle)", v.Auth.Method))
	}
	return errs
}

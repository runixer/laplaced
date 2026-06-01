// Package secrets provides a HashiCorp Vault-backed implementation of
// config.SecretProvider. It authenticates once at startup (token, Kubernetes,
// or AppRole) and reads static secrets from KV / generic engines.
//
// Scope note: secrets are fetched once and the client is then discarded — there
// is no background lease renewal. This is correct for static KV values (API
// keys, a database password). Dynamic/leased secrets (e.g. a "raw" reference to
// database/creds/<role>) are reachable but their short-lived credentials are NOT
// renewed here; renewing them would need a lease watcher and reconnect logic,
// which is intentionally out of scope.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/api/auth/approle"
	"github.com/hashicorp/vault/api/auth/kubernetes"
	"github.com/runixer/laplaced/internal/config"
)

// appRoleSecretIDEnv is the name of the env var an AppRole secret_id is read
// from — never a config literal. (Not a credential itself; just its env key.)
const appRoleSecretIDEnv = "LAPLACED_VAULT_APPROLE_SECRET_ID" //nolint:gosec // G101: env var name, not a secret

// Provider reads secrets from Vault. It implements config.SecretProvider.
type Provider struct {
	client *api.Client
	log    *slog.Logger
}

// New builds a Vault client from vcfg and authenticates using the configured
// method. The returned Provider is ready to resolve references.
func New(ctx context.Context, vcfg config.VaultConfig, log *slog.Logger) (*Provider, error) {
	apiCfg := api.DefaultConfig()
	if apiCfg.Error != nil {
		return nil, fmt.Errorf("vault default config: %w", apiCfg.Error)
	}
	if vcfg.Address != "" {
		apiCfg.Address = vcfg.Address
	}
	client, err := api.NewClient(apiCfg)
	if err != nil {
		return nil, fmt.Errorf("create vault client: %w", err)
	}
	if vcfg.Namespace != "" {
		client.SetNamespace(vcfg.Namespace)
	}

	p := &Provider{client: client, log: log}
	if err := p.authenticate(ctx, vcfg.Auth); err != nil {
		return nil, err
	}
	log.Info("authenticated to vault", "method", vcfg.Auth.Method, "address", client.Address())
	return p, nil
}

// authenticate logs the client in according to the chosen method.
func (p *Provider) authenticate(ctx context.Context, auth config.VaultAuthConfig) error {
	switch auth.Method {
	case "token":
		token, err := tokenFromEnvOrFile()
		if err != nil {
			return err
		}
		p.client.SetToken(token)
		return nil

	case "kubernetes":
		var opts []kubernetes.LoginOption
		if auth.MountPath != "" {
			opts = append(opts, kubernetes.WithMountPath(auth.MountPath))
		}
		if auth.ServiceAccountTokenPath != "" {
			opts = append(opts, kubernetes.WithServiceAccountTokenPath(auth.ServiceAccountTokenPath))
		}
		m, err := kubernetes.NewKubernetesAuth(auth.Role, opts...)
		if err != nil {
			return fmt.Errorf("configure kubernetes auth: %w", err)
		}
		return p.login(ctx, m)

	case "approle":
		var opts []approle.LoginOption
		if auth.MountPath != "" {
			opts = append(opts, approle.WithMountPath(auth.MountPath))
		}
		secretID := &approle.SecretID{FromEnv: appRoleSecretIDEnv}
		m, err := approle.NewAppRoleAuth(auth.RoleID, secretID, opts...)
		if err != nil {
			return fmt.Errorf("configure approle auth: %w", err)
		}
		return p.login(ctx, m)

	default:
		return fmt.Errorf("unsupported vault auth method %q (want token|kubernetes|approle)", auth.Method)
	}
}

// login runs an api.AuthMethod and verifies a client token came back.
func (p *Provider) login(ctx context.Context, m api.AuthMethod) error {
	secret, err := p.client.Auth().Login(ctx, m)
	if err != nil {
		return fmt.Errorf("vault login: %w", err)
	}
	if secret == nil || secret.Auth == nil || secret.Auth.ClientToken == "" {
		return errors.New("vault login returned no client token")
	}
	return nil
}

// Get resolves a single reference. The engine kind decides how the read
// response is unwrapped.
func (p *Provider) Get(ctx context.Context, ref config.VaultRef) (string, error) {
	p.log.Debug("resolving vault secret", "kind", ref.Kind, "mount", ref.Mount, "path", ref.Path, "key", ref.Key)
	var data map[string]any
	switch ref.Kind {
	case "kv2":
		s, err := p.client.KVv2(ref.Mount).Get(ctx, ref.Path)
		if err != nil {
			return "", fmt.Errorf("read %s/data/%s: %w", ref.Mount, ref.Path, err)
		}
		if s != nil {
			data = s.Data
		}
	case "kv1":
		s, err := p.client.KVv1(ref.Mount).Get(ctx, ref.Path)
		if err != nil {
			return "", fmt.Errorf("read %s/%s: %w", ref.Mount, ref.Path, err)
		}
		if s != nil {
			data = s.Data
		}
	case "raw":
		s, err := p.client.Logical().ReadWithContext(ctx, ref.Mount+"/"+ref.Path)
		if err != nil {
			return "", fmt.Errorf("read %s/%s: %w", ref.Mount, ref.Path, err)
		}
		if s != nil {
			data = s.Data
		}
	default:
		return "", fmt.Errorf("unsupported vault ref kind %q", ref.Kind)
	}

	if data == nil {
		return "", fmt.Errorf("vault: no secret at %s/%s", ref.Mount, ref.Path)
	}
	raw, ok := data[ref.Key]
	if !ok {
		return "", fmt.Errorf("vault: key %q not found at %s/%s", ref.Key, ref.Mount, ref.Path)
	}
	val, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("vault: value at %s/%s#%s is not a string", ref.Mount, ref.Path, ref.Key)
	}
	return val, nil
}

// tokenFromEnvOrFile reads a Vault token from VAULT_TOKEN, falling back to the
// standard ~/.vault-token sink used by the Vault CLI for local development.
func tokenFromEnvOrFile() (string, error) {
	if t := strings.TrimSpace(os.Getenv("VAULT_TOKEN")); t != "" {
		return t, nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if b, err := os.ReadFile(filepath.Join(home, ".vault-token")); err == nil {
			if t := strings.TrimSpace(string(b)); t != "" {
				return t, nil
			}
		}
	}
	return "", errors.New("vault token auth: VAULT_TOKEN is not set and ~/.vault-token is unavailable")
}

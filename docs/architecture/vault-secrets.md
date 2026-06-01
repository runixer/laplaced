# Vault-backed secrets

Laplaced can resolve secrets from [HashiCorp Vault](https://developer.hashicorp.com/vault)
at startup instead of taking them from config literals or `LAPLACED_*` env vars. The feature
is **optional and capability-gated**: with no `vault:` block in the config, behaviour is
exactly as before (literals / env). It is an **in-process** integration — the bot
authenticates to Vault itself, so one binary works both locally (token auth) and in a
cluster (Kubernetes auth), with no Vault Agent sidecar required.

## How it works

1. `config.Load()` reads YAML + env as usual (it does no network I/O).
2. If a `vault:` block is present, the bot builds a Vault client, authenticates, and
   resolves every config field whose value is a `vault:` reference — **before** validation
   (validation requires the resolved values).
3. Secrets are fetched **once** at startup; the client is then discarded.

Resolution runs in both entry points (`cmd/bot` and `cmd/testbot`). If a field holds a
`vault:` reference but no `vault:` block is configured, startup fails with a clear error
rather than silently using the literal string.

## Reference syntax

Each secret field carries a self-describing reference, so different secrets can live in
different mounts and even different secret engines:

```
vault:[kind:]<mount>/<path>#<key>
```

| `kind`  | Read                                  | Returns                          |
|---------|---------------------------------------|----------------------------------|
| `kv2` (default) | `<mount>/data/<path>` (KV v2) | value of `<key>` under `.data`   |
| `kv1`   | `<mount>/<path>` (KV v1)              | `<key>` from the flat map        |
| `raw`   | generic logical read of `<mount>/<path>` | `<key>` from `.Data` (any engine) |

Examples:

```yaml
openrouter:
  api_key: "vault:secret/laplaced/dev#openrouter_key"   # kv2 (default)
telegram:
  token: "vault:kv1:legacy/laplaced#telegram_token"     # different mount, KV v1
database:
  postgres:
    password: "vault:secret/laplaced/dev#pg_password"
```

Fields that may hold a reference: `telegram.token`, `openrouter.api_key`,
`mattermost.bot_token`, `database.postgres.password`, `server.auth.password`.

> **Dynamic secrets caveat.** A `raw:` reference to a dynamic engine (e.g.
> `database/creds/<role>`) is readable, but its credentials are leased and short-lived.
> Because secrets are fetched once with no background lease renewal, this integration targets
> **static** KV secrets. Renewing leased credentials (a watcher + reconnect) is not done here.

## Configuration

```yaml
vault:
  address: ${VAULT_ADDR}        # or rely on the standard VAULT_ADDR env var
  # namespace: ns               # Vault Enterprise / HCP (optional)
  auth:
    method: kubernetes          # token | kubernetes | approle
    role: laplaced              # role for kubernetes auth
    mount_path: kubernetes-2    # auth mount path; empty = method default ("kubernetes")
    # service_account_token_path: /custom/sa/token   # optional, kubernetes only
    # role_id: <role-id>        # approle only (secret_id comes from env, never here)
```

### Auth methods

- **token** — for local development. The token is read from `VAULT_TOKEN`, falling back to
  `~/.vault-token` (the Vault CLI's sink). Nothing token-related goes in the config file.
- **kubernetes** — for in-cluster runs. Uses the pod's service-account JWT. Set `role`, and
  `mount_path` if your auth backend is mounted somewhere other than `kubernetes`.
- **approle** — for CI / non-Kubernetes environments. Set `role_id` in config (or
  `LAPLACED_VAULT_APPROLE_ROLE_ID`); the `secret_id` is read **only** from
  `LAPLACED_VAULT_APPROLE_SECRET_ID`.

Other auth methods are an extension point behind the same `api.AuthMethod` seam in
`internal/secrets/vault.go`; only the three above are wired today.

### Relevant environment variables

| Variable | Purpose |
|----------|---------|
| `VAULT_ADDR` | Vault server address (also honored by the SDK directly) |
| `VAULT_TOKEN` | token for `method: token` |
| `LAPLACED_VAULT_APPROLE_ROLE_ID` / `LAPLACED_VAULT_APPROLE_SECRET_ID` | AppRole credentials |
| `LAPLACED_VAULT_AUTH_METHOD` / `LAPLACED_VAULT_AUTH_ROLE` / `LAPLACED_VAULT_AUTH_MOUNT_PATH` | override auth fields without editing YAML |

## Public-repo note

The `vault:` block and its references belong in gitignored overlays (`configs/dev-*.yaml`),
never in `default.yaml` or any tracked file — real addresses, mounts, and paths are internal.

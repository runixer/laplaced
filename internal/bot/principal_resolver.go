package bot

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/runixer/laplaced/internal/mattermost"
	"github.com/runixer/laplaced/internal/storage"
)

// PrincipalResolver maps a transport-native DM sender to the principal
// (AD-backed person) behind it, for unified cross-transport memory. It returns
// nil — NOT an error — when the sender cannot be trusted-linked (a local account,
// no usable external subject); the caller then falls back to an isolated
// passthrough scope. A transport with no resolver wired never links at all
// (passthrough behavior).
//
// Resolution is per-transport because the trust signal is transport-specific
// (Mattermost auth_service, later Talk/MAX equivalents).
type PrincipalResolver interface {
	Resolve(ctx context.Context, nativeID string) (*storage.PrincipalInput, error)
	// IsTrusted reports whether the sender is an externally-authenticated (SSO)
	// account this resolver trusts — the access gate. It is intentionally looser
	// than Resolve: it does NOT require a linkable ad_login, so an SSO user with
	// an email-shaped or empty auth_data is granted access but still resolves to
	// an isolated scope (Resolve returns nil). Access and linkability are
	// distinct concerns.
	IsTrusted(ctx context.Context, nativeID string) (bool, error)
}

// principalCacheInvalidator is an optional upgrade of PrincipalResolver,
// implemented by resolvers whose trust decision is backed by a cache that can go
// stale across an auth_service migration. The bot type-asserts for it and drops a
// denied sender so the next message re-reads a fresh profile.
type principalCacheInvalidator interface {
	Invalidate(nativeID string)
}

// botClassifier is an optional upgrade of PrincipalResolver: it reports whether a
// sender is a transport-level bot account and whether that bot is on the
// configured trusted-bots allowlist. The bot type-asserts for it on the
// not-trusted-by-SSO path so trusted automation (e.g. an alerting bot asking for
// an incident summary) can be admitted even though bot accounts are local
// (auth_service == "") and so fail the SSO trust gate. A non-allowlisted bot is
// ignored silently rather than sent an access-denied notice.
type botClassifier interface {
	ClassifyBot(ctx context.Context, nativeID string) (isBot, allowlisted bool, err error)
}

// mmUserLookup is the slice of the Mattermost client the resolver needs (the
// cached profile read plus its invalidation); narrowed for testability.
type mmUserLookup interface {
	GetUser(ctx context.Context, userID string) (*mattermost.User, error)
	InvalidateUser(userID string)
}

// MattermostPrincipalResolver implements federated-passive resolution for the
// Mattermost/Time transport. It reads the sender's GetUser record and
// links only externally-authenticated accounts.
//
// Trust gates (the highest-stakes lines — one slip merges two people's memory):
//   - auth_service == ""  → local account → nil (isolation). NEVER linked.
//   - trustedServices set and auth_service not in it → nil (isolation).
//   - link key is the lowercased auth_data (the AD login), NEVER the email. A
//     local user can self-claim an email in a trusted-looking domain, so email is stored as a
//     principal attribute only.
//
// objectGUID stays empty for now and is backfilled (additively) later
// via a Keycloak/LDAP lookup; principal dedup falls back to ad_login until then.
type MattermostPrincipalResolver struct {
	client          mmUserLookup
	trustedServices []string // empty = trust any non-empty auth_service
	trustedBots     []string // bot usernames admitted despite being local accounts; empty = no bots
	logger          *slog.Logger
}

// NewMattermostPrincipalResolver builds the resolver. trustedServices restricts
// which auth_service values are trusted (empty = any non-empty). trustedBots
// lists bot-account usernames admitted despite failing the SSO gate (empty = no
// bots trusted — fail-closed). Both lists are lowercased/trimmed so the
// comparison against Mattermost's auth_service / username is case-insensitive
// (config "SAML" matches the wire value "saml"; "@AlertBot" matches "alertbot").
func NewMattermostPrincipalResolver(client mmUserLookup, trustedServices, trustedBots []string, logger *slog.Logger) *MattermostPrincipalResolver {
	return &MattermostPrincipalResolver{
		client:          client,
		trustedServices: normalizeList(trustedServices),
		trustedBots:     normalizeList(trustedBots),
		logger:          logger.With("component", "mm-principal-resolver"),
	}
}

// normalizeList lowercases and trims each entry and drops empties, so allowlist
// comparisons against wire values are case-insensitive and tolerant of stray
// whitespace or a leading "@".
func normalizeList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		v := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "@")))
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Resolve returns the principal attributes for nativeID, or nil if the sender is
// not trusted-linkable (local account / untrusted service / no AD login).
func (r *MattermostPrincipalResolver) Resolve(ctx context.Context, nativeID string) (*storage.PrincipalInput, error) {
	u, err := r.client.GetUser(ctx, nativeID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch mm user %s for principal resolution: %w", nativeID, err)
	}

	// Trust gate: only externally-authenticated accounts may be linked.
	if !r.authServiceTrusted(u) {
		r.logger.Debug("local or untrusted auth_service, not linking to principal",
			"user_id", nativeID, "auth_service", u.AuthService)
		return nil, nil
	}

	// The AD login is the external join key (litellm/Jira/Keycloak), lowercased.
	adLogin := strings.ToLower(strings.TrimSpace(u.AuthData))
	if adLogin == "" {
		r.logger.Warn("trusted account has empty auth_data, isolating", "user_id", nativeID, "auth_service", u.AuthService)
		return nil, nil
	}
	// Guard the "never link by email" invariant at the source: auth_data is
	// expected to be a login (sAMAccountName / preferred_username), not a UPN or
	// email. An '@' means the IdP is mapping NameID to an email-shaped value — the
	// wrong join key for litellm/Jira and a silent break of the email invariant.
	// Isolate rather than link on a value we can't trust as a login.
	if strings.Contains(adLogin, "@") {
		r.logger.Warn("auth_data looks like an email, not a login; isolating",
			"user_id", nativeID, "auth_service", u.AuthService)
		return nil, nil
	}

	return &storage.PrincipalInput{
		ADLogin:     adLogin,
		Email:       u.Email, // attribute only — never the link key
		DisplayName: mmPrincipalDisplayName(u),
		// ObjectGUID intentionally empty for now (backfilled later).
	}, nil
}

// IsTrusted reports whether the sender is an externally-authenticated account
// this resolver trusts (the access gate). It checks only auth_service — a
// non-empty value that, when a trustedServices allowlist is configured, is a
// member of it. It deliberately skips the ad_login/email checks Resolve applies:
// access is broader than linkability (an SSO user with an unusable auth_data
// still gets in, just on an isolated scope).
func (r *MattermostPrincipalResolver) IsTrusted(ctx context.Context, nativeID string) (bool, error) {
	u, err := r.client.GetUser(ctx, nativeID)
	if err != nil {
		return false, fmt.Errorf("failed to fetch mm user %s for access check: %w", nativeID, err)
	}
	return r.authServiceTrusted(u), nil
}

// Invalidate drops the resolver's cached view of nativeID so the next trust
// check re-reads the profile from the server. The bot calls this on a denial:
// the denied account may have just migrated to SSO, and re-reading on its next
// message gives instant recovery instead of waiting out the profile-cache TTL.
func (r *MattermostPrincipalResolver) Invalidate(nativeID string) {
	r.client.InvalidateUser(nativeID)
}

// ClassifyBot reports whether nativeID is a bot account and, if so, whether its
// username is on the trusted-bots allowlist. It is consulted only after the SSO
// trust gate (IsTrusted) has already rejected the sender, so the GetUser here is
// a cache hit. An empty allowlist trusts no bots (fail-closed): slices.Contains
// over an empty slice is false, by design — there is deliberately no "empty =
// trust all bots" branch.
func (r *MattermostPrincipalResolver) ClassifyBot(ctx context.Context, nativeID string) (bool, bool, error) {
	u, err := r.client.GetUser(ctx, nativeID)
	if err != nil {
		return false, false, fmt.Errorf("failed to fetch mm user %s for bot classification: %w", nativeID, err)
	}
	if !u.IsBot {
		return false, false, nil
	}
	return true, slices.Contains(r.trustedBots, strings.ToLower(strings.TrimSpace(u.Username))), nil
}

// authServiceTrusted is the shared auth_service trust predicate behind both
// Resolve (gate 1) and IsTrusted: the account is externally authenticated
// (auth_service != "") and, when a trustedServices allowlist is set, its
// auth_service is in it (case-insensitive). Centralized so the access gate and
// the linkage gate can never drift apart.
func (r *MattermostPrincipalResolver) authServiceTrusted(u *mattermost.User) bool {
	if u.AuthService == "" {
		return false
	}
	if len(r.trustedServices) > 0 && !slices.Contains(r.trustedServices, strings.ToLower(u.AuthService)) {
		return false
	}
	return true
}

// mmPrincipalDisplayName renders a human label from the MM user, preferring the
// real name and falling back to @username.
func mmPrincipalDisplayName(u *mattermost.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name != "" {
		return name
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	return u.ID
}

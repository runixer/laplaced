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
}

// mmUserLookup is the slice of the Mattermost client the resolver needs (just
// GetUser); narrowed for testability.
type mmUserLookup interface {
	GetUser(ctx context.Context, userID string) (*mattermost.User, error)
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
	logger          *slog.Logger
}

// NewMattermostPrincipalResolver builds the resolver. trustedServices restricts
// which auth_service values are trusted (empty = any non-empty). Values are
// lowercased so the comparison against Mattermost's auth_service is
// case-insensitive (config "SAML" matches the wire value "saml").
func NewMattermostPrincipalResolver(client mmUserLookup, trustedServices []string, logger *slog.Logger) *MattermostPrincipalResolver {
	normalized := make([]string, len(trustedServices))
	for i, s := range trustedServices {
		normalized[i] = strings.ToLower(strings.TrimSpace(s))
	}
	return &MattermostPrincipalResolver{
		client:          client,
		trustedServices: normalized,
		logger:          logger.With("component", "mm-principal-resolver"),
	}
}

// Resolve returns the principal attributes for nativeID, or nil if the sender is
// not trusted-linkable (local account / untrusted service / no AD login).
func (r *MattermostPrincipalResolver) Resolve(ctx context.Context, nativeID string) (*storage.PrincipalInput, error) {
	u, err := r.client.GetUser(ctx, nativeID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch mm user %s for principal resolution: %w", nativeID, err)
	}

	// Trust gate: only externally-authenticated accounts may be linked.
	if u.AuthService == "" {
		r.logger.Debug("local account, not linking to principal", "user_id", nativeID)
		return nil, nil
	}
	if len(r.trustedServices) > 0 && !slices.Contains(r.trustedServices, strings.ToLower(u.AuthService)) {
		r.logger.Debug("untrusted auth_service, not linking", "user_id", nativeID, "auth_service", u.AuthService)
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

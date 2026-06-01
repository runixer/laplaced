package bot

import (
	"context"
	"fmt"

	"github.com/runixer/laplaced/internal/storage"
)

// Transport kind constants used at the identity seam. These string values are
// also the transport namespace in scope-id derivation (PassthroughScopeID /
// ResolveScope), so they are a data-format constant — changing them re-keys all
// scopes for that transport.
const (
	transportTelegram   = "telegram"
	transportMattermost = "mattermost"
)

// identityStore is the storage surface resolveScopeID needs: the scope/identity
// map plus principal and channel get-or-create. *storage.Store satisfies it; the
// bot holds it as scopeRepo.
type identityStore interface {
	storage.ScopeRepository
	storage.IdentityRepository
	storage.PrincipalRepository
	storage.ChannelRepository
}

// resolveScopeID maps a transport message to the scope id (UUID partition key).
// It is the single place that centralizes identity resolution across transports,
// branching on (a) channel vs DM and (b) whether a PrincipalResolver is wired for
// this transport — never on transport name or a global mode flag.
//
//   - Telegram: deterministic passthrough. The scope id is uuidv5(telegram:id)
//     and no identity state is written — the Telegram path behaves exactly as
//     before, just on UUID keys.
//   - Channel (any transport): a channel is its own scope, keyed deterministically
//     by (transport, conversation); never principal-resolved.
//   - DM, no resolver wired: passthrough — uuidv5(transport:sender). This is the
//     the default passthrough behavior for a transport that hasn't opted into principal resolution.
//   - DM, resolver wired: reuse an existing identity mapping, else resolve the
//     sender to a principal (trust gate inside the resolver) and link the handle.
//     An unlinkable sender (local account) gets an isolated passthrough scope.
func resolveScopeID(ctx context.Context, store identityStore, resolver PrincipalResolver, kind string, im IncomingMessage) (storage.ScopeID, error) {
	if kind == transportTelegram {
		return storage.PassthroughScopeID(transportTelegram, im.SenderID), nil
	}

	if !im.IsDirect {
		return store.GetOrCreateChannel(kind, im.ConversationID, im.ConversationDisplay)
	}

	nativeID := im.SenderID

	// No resolver wired for this transport → passthrough DM scope.
	if resolver == nil {
		return storage.PassthroughScopeID(kind, nativeID), nil
	}

	// Reuse an existing handle→scope mapping if we've seen this sender before.
	idn, err := store.GetIdentity(kind, nativeID)
	if err != nil {
		return "", fmt.Errorf("failed to look up identity %s/%s: %w", kind, nativeID, err)
	}
	if idn != nil {
		return idn.ScopeID, nil
	}

	// First sight: resolve the sender to a principal (trust gate is inside the
	// resolver). nil = not trusted-linkable → isolated passthrough scope.
	pin, err := resolver.Resolve(ctx, nativeID)
	if err != nil {
		return "", fmt.Errorf("principal resolution failed for %s/%s: %w", kind, nativeID, err)
	}

	var scope storage.ScopeID
	if pin == nil {
		scope = storage.PassthroughScopeID(kind, nativeID)
	} else {
		scope, _, err = store.GetOrCreatePrincipal(*pin)
		if err != nil {
			return "", fmt.Errorf("get-or-create principal for %s/%s: %w", kind, nativeID, err)
		}
	}

	// Record the mapping so subsequent messages skip resolution (and, for a
	// principal, so a second handle of the same person can be linked to the scope).
	if err := store.PutIdentity(kind, nativeID, scope); err != nil {
		return "", fmt.Errorf("failed to record identity %s/%s: %w", kind, nativeID, err)
	}
	return scope, nil
}

// scopeLabel picks the users-table label for a non-Telegram scope: a channel
// scope is labeled by its channel name (not the last poster), a DM by the
// sender. Idempotent — the channel name is stable, so participants posting in a
// channel don't overwrite its label. Falls back to the sender when no channel
// name is available (resolve failed).
func scopeLabel(im IncomingMessage) string {
	if !im.IsDirect && im.ConversationDisplay != "" {
		return im.ConversationDisplay
	}
	return im.SenderDisplay
}

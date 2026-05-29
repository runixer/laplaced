package bot

import (
	"fmt"
	"strconv"

	"github.com/runixer/laplaced/internal/storage"
)

// Transport kind constants used at the identity seam.
const (
	transportTelegram = "telegram"
	transportTime     = "time"
)

// resolveScopeID maps a (transport, native sender/conversation id) pair to the
// internal int64 scope id used as the storage partition key (`user_id`).
//
// This is the single place that centralizes identity resolution across
// transports (variant A of the migration plan):
//
//   - Telegram: identity passthrough. The native id IS the int64 user id, so we
//     parse and return it directly and write NO scopes row — the home instance
//     never accrues identity-mapping state.
//   - Other transports (Time/Mattermost have 26-char string ids): mint/look up a
//     surrogate int64 via the scopes table.
//
// scope_type is "user" for the v0.10 DM-only PoC; channel scopes are a future
// additive branch (see migration 010).
func resolveScopeID(scopes storage.ScopeRepository, kind, nativeID string) (int64, error) {
	if kind == transportTelegram {
		id, err := strconv.ParseInt(nativeID, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid telegram native id %q: %w", nativeID, err)
		}
		return id, nil
	}
	return scopes.ResolveScope(kind, "user", nativeID)
}

// scopeNativeID centralizes the scope-selection policy: a DM is scoped to its
// sender, a channel to the conversation. v0.10 only exercises the DM/sender
// branch; the channel branch is forward-design for Phase 6 channel RAG.
func scopeNativeID(im IncomingMessage) string {
	if im.IsDirect {
		return im.SenderID
	}
	return im.ConversationID
}

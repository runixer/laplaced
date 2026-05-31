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

// Scope type constants. A scope is the memory tenant keyed by the internal
// int64 `user_id`. Most scopes are a single person (DM); a channel is a scope
// whose subject is the conversation, with many participants (Phase 6).
const (
	scopeTypeUser    = "user"
	scopeTypeChannel = "channel"
)

// resolveScopeID maps a transport message to the internal int64 scope id used as
// the storage partition key (`user_id`).
//
// This is the single place that centralizes identity resolution across
// transports (variant A of the migration plan). It owns both halves of the
// policy: which native id identifies the scope (scopeNativeID) and which
// scope_type it is (DM vs channel, from IsDirect).
//
//   - Telegram: identity passthrough. The native id IS the int64 user id, so we
//     parse and return it directly and write NO scopes row — the home instance
//     never accrues identity-mapping state. Telegram is always DM-scoped.
//   - Other transports (Time/Mattermost have 26-char string ids): mint/look up a
//     surrogate int64 via the scopes table, tagged 'user' for a DM or 'channel'
//     for a multi-participant conversation.
func resolveScopeID(scopes storage.ScopeRepository, kind string, im IncomingMessage) (int64, error) {
	nativeID := scopeNativeID(im)
	if kind == transportTelegram {
		id, err := strconv.ParseInt(nativeID, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid telegram native id %q: %w", nativeID, err)
		}
		return id, nil
	}
	scopeType := scopeTypeUser
	if !im.IsDirect {
		scopeType = scopeTypeChannel
	}
	return scopes.ResolveScope(kind, scopeType, nativeID)
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

// scopeNativeID centralizes the scope-selection policy: a DM is scoped to its
// sender, a channel to the conversation. v0.10 only exercises the DM/sender
// branch; the channel branch is forward-design for Phase 6 channel RAG.
func scopeNativeID(im IncomingMessage) string {
	if im.IsDirect {
		return im.SenderID
	}
	return im.ConversationID
}

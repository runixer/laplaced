package bot

import (
	"log/slog"

	"github.com/runixer/laplaced/internal/storage"
)

// privacyModeEnabled reports whether the scope's do-not-store mode is on.
// Read fresh at each history save so a mid-turn privacy_mode(enable) tool
// call already covers the assistant reply of the same turn. Fails open to
// normal storage: a read error is logged and the message stored unflagged.
func (b *Bot) privacyModeEnabled(userID storage.ScopeID, logger *slog.Logger) bool {
	if b.userRepo == nil {
		return false
	}
	enabled, err := b.userRepo.GetPrivacyMode(userID)
	if err != nil {
		logger.Warn("failed to read privacy mode, storing message normally", "error", err)
		return false
	}
	return enabled
}

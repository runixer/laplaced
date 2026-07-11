package tools

import (
	"fmt"

	"github.com/runixer/laplaced/internal/storage"
)

// performPrivacyMode toggles the scope's do-not-store mode. The returned
// text is fed back to the model and states the mechanism's honest limits so
// the model cannot over-promise: raw messages remain in the operator-visible
// log; only long-term memory (topics, facts, embeddings) is excluded.
func (e *ToolExecutor) performPrivacyMode(userID storage.ScopeID, args map[string]interface{}) (string, error) {
	if e.userRepo == nil {
		return "", fmt.Errorf("privacy_mode tool is not configured")
	}
	action, _ := args["action"].(string)
	switch action {
	case "enable":
		if err := e.userRepo.SetPrivacyMode(userID, true); err != nil {
			return "", fmt.Errorf("enabling privacy mode: %w", err)
		}
		return "Privacy mode ENABLED. From now on, messages in this conversation (including your replies) are excluded from long-term memory: no topics, no facts, no embeddings. HONEST LIMITS to relay to the user: the raw message log still exists until the session is archived and is visible to the system operator; messages sent BEFORE this call are stored normally. The mode turns off automatically when this session is archived — call privacy_mode(disable) when the sensitive part of the conversation is over.", nil
	case "disable":
		if err := e.userRepo.SetPrivacyMode(userID, false); err != nil {
			return "", fmt.Errorf("disabling privacy mode: %w", err)
		}
		return "Privacy mode DISABLED. Messages are stored in long-term memory normally again.", nil
	default:
		return "", fmt.Errorf("invalid action %q: must be 'enable' or 'disable'", action)
	}
}

package telegram

// AllowedUpdateTypes is the single subscription source for both webhook and
// long polling. Keep it aligned with fields actually decoded and handled by
// Update; subscribing to an update kind and then silently acknowledging it is
// data loss.
func AllowedUpdateTypes() []string {
	return []string{"message", "message_reaction"}
}

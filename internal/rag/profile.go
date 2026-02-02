package rag

import (
	"github.com/runixer/laplaced/internal/storage"
)

// FormatUserProfile formats user facts for inclusion in agent prompts.
// Delegates to storage.FormatUserProfile.
func FormatUserProfile(facts []storage.Fact) string {
	return storage.FormatUserProfile(facts)
}

// FormatUserProfileCompact formats user facts without Fact IDs.
// Delegates to storage.FormatUserProfileCompact.
func FormatUserProfileCompact(facts []storage.Fact) string {
	return storage.FormatUserProfileCompact(facts)
}

// FormatRecentTopics formats recent topics for inclusion in agent prompts.
// Delegates to storage.FormatRecentTopics.
func FormatRecentTopics(topics []storage.TopicExtended) string {
	return storage.FormatRecentTopics(topics)
}

// FilterProfileFacts filters facts to identity and high-importance facts only.
// Delegates to storage.FilterProfileFacts.
func FilterProfileFacts(facts []storage.Fact) []storage.Fact {
	return storage.FilterProfileFacts(facts)
}

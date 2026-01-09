package storage

import (
	"fmt"
	"strings"
)

// FormatUserProfile formats user facts for inclusion in agent prompts.
// Returns content wrapped in <user_profile> tags.
// Format: - [ID:X] [Entity] [Category/Type] (Updated: date) Content
func FormatUserProfile(facts []Fact) string {
	if len(facts) == 0 {
		return "<user_profile>\n</user_profile>"
	}

	var sb strings.Builder
	sb.WriteString("<user_profile>\n")
	for _, f := range facts {
		sb.WriteString(fmt.Sprintf("- [ID:%d] [%s] [%s/%s] (Updated: %s) %s\n",
			f.ID, f.Entity, f.Category, f.Type, f.LastUpdated.Format("2006-01-02"), f.Content))
	}
	sb.WriteString("</user_profile>")
	return sb.String()
}

// FormatRecentTopics formats recent topics for inclusion in agent prompts.
// Returns content wrapped in <recent_topics> tags.
// Format: - date: "summary" (N msg, ~Xk chars)
func FormatRecentTopics(topics []TopicExtended) string {
	if len(topics) == 0 {
		return "<recent_topics>\n</recent_topics>"
	}

	var sb strings.Builder
	sb.WriteString("<recent_topics>\n")
	for _, t := range topics {
		sb.WriteString(fmt.Sprintf("- %s: %q (%d msg, ~%dk chars)\n",
			t.CreatedAt.Format("2006-01-02"),
			t.Summary,
			t.MessageCount,
			t.SizeChars/1000,
		))
	}
	sb.WriteString("</recent_topics>")
	return sb.String()
}

// FilterProfileFacts filters facts to identity and high-importance facts only.
// This is the standard filter used across all agents.
func FilterProfileFacts(facts []Fact) []Fact {
	var relevant []Fact
	for _, f := range facts {
		if f.Type == "identity" || f.Importance >= 80 {
			relevant = append(relevant, f)
		}
	}
	return relevant
}

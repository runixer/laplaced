package storage

import (
	"fmt"
	"strings"
)

// FormatUserProfile formats user facts for inclusion in agent prompts.
// Returns content wrapped in <user_profile> tags.
// Format: - [Fact:X] [Category/Type] (Updated: date) Content
// Use this for agents that need Fact IDs (e.g., Archivist for update/delete).
func FormatUserProfile(facts []Fact) string {
	return formatProfileWithIDs(facts, "user_profile")
}

// FormatChannelProfile is the channel-scope counterpart of FormatUserProfile:
// the same fact rendering wrapped in <channel_profile> tags (Phase 6). For a
// channel scope these facts describe the channel itself (shared context,
// decisions, state) rather than a single person.
func FormatChannelProfile(facts []Fact) string {
	return formatProfileWithIDs(facts, "channel_profile")
}

// formatProfileWithIDs renders facts (with Fact IDs) wrapped in the given tag.
func formatProfileWithIDs(facts []Fact, tag string) string {
	if len(facts) == 0 {
		return fmt.Sprintf("<%s>\n</%s>", tag, tag)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "<%s>\n", tag)
	for _, f := range facts {
		fmt.Fprintf(&sb, "- [Fact:%d] [%s/%s] (Updated: %s) %s\n",
			f.ID, f.Category, f.Type, f.LastUpdated.Format("2006-01-02"), f.Content)
	}
	fmt.Fprintf(&sb, "</%s>", tag)
	return sb.String()
}

// FormatUserProfileCompact formats user facts without Fact IDs.
// Returns content wrapped in <user_profile> tags.
// Format: - [Category/Type] (Updated: date) Content
// Use this for agents that don't need Fact IDs (e.g., Reranker, Laplace).
// This prevents ID format confusion with [Person:N], [Topic:N], [Artifact:N].
func FormatUserProfileCompact(facts []Fact) string {
	if len(facts) == 0 {
		return "<user_profile>\n</user_profile>"
	}

	var sb strings.Builder
	sb.WriteString("<user_profile>\n")
	for _, f := range facts {
		fmt.Fprintf(&sb, "- [%s/%s] (%s) %s\n",
			f.Category, f.Type, f.LastUpdated.Format("2006-01-02"), f.Content)
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
		fmt.Fprintf(&sb, "- %s: %q (%d msg, ~%dk chars)\n",
			t.CreatedAt.Format("2006-01-02"),
			t.Summary,
			t.MessageCount,
			t.SizeChars/1000)
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

// XML tag constants for people formatting.
const (
	TagInnerCircle         = "inner_circle"         // Work_Inner + Family, system prompt
	TagRelevantPeople      = "relevant_people"      // Reranker selected, user prompt
	TagPeople              = "people"               // All people, for Archivist
	TagChannelParticipants = "channel_participants" // Active channel members (Phase 6)
)

// FormatPeople formats people list with specified XML tag.
// Format: [Person:ID] Name (@username, aka Alias1, Alias2) [Circle]: Bio
// If tag is empty, outputs plain list without XML wrapper.
func FormatPeople(people []Person, tag string) string {
	if len(people) == 0 {
		if tag == "" || tag == TagRelevantPeople {
			return "" // No tag or relevant people = empty string
		}
		return fmt.Sprintf("<%s>\n</%s>", tag, tag)
	}

	var sb strings.Builder
	if tag != "" {
		fmt.Fprintf(&sb, "<%s>\n", tag)
	}
	for _, p := range people {
		fmt.Fprintf(&sb, "[Person:%d] %s", p.ID, p.DisplayName)

		if p.Username != nil && *p.Username != "" {
			fmt.Fprintf(&sb, " (@%s)", *p.Username)
		}

		if len(p.Aliases) > 0 {
			fmt.Fprintf(&sb, " (aka %s)", strings.Join(p.Aliases, ", "))
		}

		fmt.Fprintf(&sb, " [%s]", p.Circle)

		if p.Bio != "" {
			fmt.Fprintf(&sb, ": %s", p.Bio)
		}
		sb.WriteString("\n")
	}
	if tag != "" {
		fmt.Fprintf(&sb, "</%s>", tag)
	}
	return sb.String()
}

// FilterInnerCircle returns only Work_Inner and Family people.
func FilterInnerCircle(people []Person) []Person {
	var inner []Person
	for _, p := range people {
		if p.Circle == CircleWorkInner || p.Circle == CircleFamily {
			inner = append(inner, p)
		}
	}
	return inner
}

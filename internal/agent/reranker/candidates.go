// Package reranker provides the Reranker agent that uses tool calls to select
// the most relevant topics from vector search candidates.
package reranker

import (
	"fmt"
	"strings"

	"github.com/runixer/laplaced/internal/storage"
)

// formatCandidatesForReranker formats topic candidates for the LLM prompt.
// Format: [Topic:ID] (score) date | msgs, size | summary
func formatCandidatesForReranker(candidates []Candidate) string {
	var sb strings.Builder
	for _, c := range candidates {
		date := c.Topic.CreatedAt.Format("2006-01-02")
		sizeStr := formatSizeChars(c.SizeChars)
		fmt.Fprintf(&sb, "[Topic:%d] (%.2f) %s | %d msgs, %s | %s\n",
			c.TopicID, c.Score, date, c.MessageCount, sizeStr, c.Topic.Summary)
	}
	return sb.String()
}

// formatArtifactCandidates formats artifact candidates for the LLM prompt (v0.6.0).
// Format: [Artifact:ID] (score) type: "name" | keywords | Entities: entities | summary
// Session candidates render "(session)" instead of cosine similarity to signal that the
// file is part of the active conversation and should be prioritized.
func formatArtifactCandidates(candidates []ArtifactCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, c := range candidates {
		var parts []string

		// Keywords
		if len(c.Keywords) > 0 {
			parts = append(parts, strings.Join(c.Keywords, ", "))
		}
		// Entities (v0.6.0: include extracted entities in context)
		if len(c.Entities) > 0 {
			parts = append(parts, "Entities: "+strings.Join(c.Entities, ", "))
		}
		// Note: RAGHints (Questions) removed in v0.6.0 - too verbose and redundant with summary

		extrasStr := ""
		if len(parts) > 0 {
			extrasStr = " | " + strings.Join(parts, " | ")
		}

		scoreTag := fmt.Sprintf("(%.2f)", c.Score)
		if c.IsSession {
			scoreTag = "(session)"
		}

		fmt.Fprintf(&sb, "[Artifact:%d] %s %s: \"%s\"%s | %s\n",
			c.ArtifactID, scoreTag, c.FileType, c.OriginalName, extrasStr, c.Summary)
	}
	return sb.String()
}

// formatSizeChars formats byte count as "~XK chars" or "~X chars".
func formatSizeChars(chars int) string {
	if chars >= 1000 {
		return fmt.Sprintf("~%dK chars", chars/1000)
	}
	return fmt.Sprintf("~%d chars", chars)
}

// FormatPeopleForReranker formats person candidates for the LLM prompt (v0.5.1).
// Delegates to storage.FormatPeople for consistent formatting.
func FormatPeopleForReranker(candidates []PersonCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	// Extract Person from PersonCandidate and use unified format
	var candidatePeople []storage.Person
	for _, c := range candidates {
		candidatePeople = append(candidatePeople, c.Person)
	}
	return storage.FormatPeople(candidatePeople, "") // Plain list without XML wrapper
}

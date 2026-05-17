package markdown

import "strings"

// BalanceOpenMarkers appends the minimum suffix needed to close any open
// Markdown markers in `s`, so that mid-stream content can be safely fed to
// ToHTML without producing dangling/unrendered markup.
//
// Handled markers (these cover ~99% of LLM chat output):
//   - Triple-backtick fenced code blocks (```)
//   - Inline code (`)
//   - Bold (**)
//
// Intentionally NOT handled — they're rare in chat and the heuristics
// would do more harm than good (snake_case identifiers, mathematical
// asterisks, etc.):
//   - Single-asterisk italic (*)
//   - Underscore italic (_) and underscore bold (__)
//   - Strikethrough (~~)
//
// The function is purely additive: it never modifies or removes characters
// from the input. If a marker can't be cleanly closed (e.g. an opening
// fence with no language line yet) the function still appends a closer so
// goldmark sees a well-formed buffer.
func BalanceOpenMarkers(s string) string {
	if s == "" {
		return s
	}

	// Step 1: triple-backtick fences. If the count is odd, the buffer is
	// currently inside a fenced block — close it on a fresh line so the
	// closing fence is interpreted as such (Markdown requires the closer
	// at the start of a line).
	if strings.Count(s, "```")%2 == 1 {
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		s += "```"
		return s
	}

	// Step 2: balance ** and ` only OUTSIDE fenced blocks. Splitting by
	// the fence delimiter gives alternating segments: index 0 (outside),
	// 1 (inside), 2 (outside), ... — the count being even guarantees the
	// last segment is "outside".
	parts := strings.Split(s, "```")
	for i := range parts {
		if i%2 == 1 {
			// Inside fence — leave alone.
			continue
		}
		parts[i] = balanceOutsideFence(parts[i])
	}
	return strings.Join(parts, "```")
}

// balanceOutsideFence appends closing markers for unmatched ** and `
// pairs in a segment that is known to be outside any fenced block.
func balanceOutsideFence(s string) string {
	// Bold first: ** can contain `, but ` cannot contain ** without a
	// closing tick in between, so closing ** first is safe.
	if strings.Count(s, "**")%2 == 1 {
		s += "**"
	}

	// Inline code: every backtick in this segment is a single-tick marker
	// (triples are eaten by the outer split).
	if strings.Count(s, "`")%2 == 1 {
		s += "`"
	}

	return s
}

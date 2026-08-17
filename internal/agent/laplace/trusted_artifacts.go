package laplace

import (
	"regexp"
	"strconv"
)

var (
	// Assistant history is model-visible and its filename portion originated
	// with a user upload. Trust only the complete marker line emitted by the
	// application, capturing the final reference rather than arbitrary marker-
	// shaped prose inside a filename or model response.
	canonicalArtifactRefRE = regexp.MustCompile(`(?m)^(?:🎨|📄) [^\r\n]* \(artifact:([1-9][0-9]*)\)$`)
)

// trustedArtifactIDs assembles the tool-execution allowlist exclusively from
// app-owned inputs. In particular, it never scans the active user's free text:
// a forged "(artifact:N)" in a request therefore cannot authorize access.
//
// Order is stable and useful to diagnostics: current-message IDs first,
// reranker inventory next, fully-loaded selections next, and canonical markers
// from prior assistant/system history last. Duplicates and non-positive IDs are
// discarded without reordering the first occurrence.
func trustedArtifactIDs(req *Request, data *ContextData) []int64 {
	seen := make(map[int64]struct{})
	ids := make([]int64, 0)
	add := func(id int64) {
		if id <= 0 {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	if req != nil {
		for _, id := range req.TrustedArtifactIDs {
			add(id)
		}
	}
	if data == nil {
		return ids
	}
	for _, artifact := range data.ArtifactResults {
		add(artifact.ArtifactID)
	}
	for _, id := range data.SelectedArtifactIDs {
		add(id)
	}
	for _, message := range data.RecentHistory {
		if message.Role != "assistant" && message.Role != "system" {
			continue
		}
		addCanonicalMatches(message.Content, canonicalArtifactRefRE, add)
	}
	return ids
}

func addCanonicalMatches(content string, re *regexp.Regexp, add func(int64)) {
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		if len(match) != 2 {
			continue
		}
		id, err := strconv.ParseInt(match[1], 10, 64)
		if err == nil {
			add(id)
		}
	}
}

func appendUniqueArtifactIDs(existing []int64, values ...int64) []int64 {
	seen := make(map[int64]struct{}, len(existing)+len(values))
	result := make([]int64, 0, len(existing)+len(values))
	for _, id := range append(append([]int64(nil), existing...), values...) {
		if id <= 0 {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

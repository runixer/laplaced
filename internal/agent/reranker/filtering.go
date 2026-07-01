// Package reranker provides the Reranker agent that uses tool calls to select
// the most relevant topics from vector search candidates.
package reranker

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"

	"github.com/runixer/laplaced/internal/storage"
)

// filterValidTopics removes hallucinated topic IDs from the result.
// Logs warnings for any IDs that don't exist in the candidate map.
func filterValidTopics(
	userID storage.ScopeID,
	result *Result,
	candidateMap map[int64]Candidate,
	logger *slog.Logger,
) *Result {
	var validTopics []TopicSelection
	var hallucinatedIDs []int64

	for _, topic := range result.Topics {
		topicID, err := parseTopicID(topic.ID)
		if err != nil {
			if logger != nil {
				logger.Warn("invalid topic ID format", "user_id", userID, "id", topic.ID, "error", err)
			}
			hallucinatedIDs = append(hallucinatedIDs, -1)
			continue
		}
		if _, ok := candidateMap[topicID]; ok {
			validTopics = append(validTopics, topic)
		} else {
			hallucinatedIDs = append(hallucinatedIDs, topicID)
		}
	}

	if len(hallucinatedIDs) > 0 && logger != nil {
		logger.Warn("reranker hallucinated topic IDs",
			"user_id", userID,
			"hallucinated_ids", hallucinatedIDs,
			"valid_count", len(validTopics),
			"total_returned", len(result.Topics),
		)
	}

	return &Result{
		Topics:    validTopics,
		People:    result.People,    // Preserve people for next filter
		Artifacts: result.Artifacts, // Preserve artifacts for next filter
	}
}

// filterValidPeople removes hallucinated person IDs from the result (v0.5.1).
// Logs warnings for any IDs that don't exist in the candidate map.
func filterValidPeople(
	userID storage.ScopeID,
	result *Result,
	peopleMap map[int64]PersonCandidate,
	logger *slog.Logger,
) *Result {
	if len(result.People) == 0 {
		return result
	}

	var validPeople []PersonSelection
	var hallucinatedIDs []int64

	for _, person := range result.People {
		personID, err := parsePersonID(person.ID)
		if err != nil {
			if logger != nil {
				logger.Warn("invalid person ID format", "user_id", userID, "id", person.ID, "error", err)
			}
			hallucinatedIDs = append(hallucinatedIDs, -1)
			continue
		}
		if _, ok := peopleMap[personID]; ok {
			validPeople = append(validPeople, person)
		} else {
			hallucinatedIDs = append(hallucinatedIDs, personID)
		}
	}

	if len(hallucinatedIDs) > 0 && logger != nil {
		logger.Warn("reranker hallucinated person IDs",
			"user_id", userID,
			"hallucinated_ids", hallucinatedIDs,
			"valid_count", len(validPeople),
			"total_returned", len(result.People),
		)
	}

	return &Result{
		Topics:    result.Topics,
		People:    validPeople,
		Artifacts: result.Artifacts, // Preserve artifacts for next filter
	}
}

// filterValidArtifacts removes hallucinated artifact IDs from the result (v0.6.0).
// Logs warnings for any IDs that don't exist in the candidate map.
func filterValidArtifacts(
	userID storage.ScopeID,
	result *Result,
	artifactsMap map[int64]ArtifactCandidate,
	logger *slog.Logger,
) *Result {
	if len(result.Artifacts) == 0 {
		return result
	}

	var validArtifacts []ArtifactSelection
	var hallucinatedIDs []int64

	for _, artifact := range result.Artifacts {
		artifactID, err := parseArtifactID(artifact.ID)
		if err != nil {
			if logger != nil {
				logger.Warn("invalid artifact ID format", "user_id", userID, "id", artifact.ID, "error", err)
			}
			hallucinatedIDs = append(hallucinatedIDs, -1)
			continue
		}
		if _, ok := artifactsMap[artifactID]; ok {
			validArtifacts = append(validArtifacts, artifact)
		} else {
			hallucinatedIDs = append(hallucinatedIDs, artifactID)
		}
	}

	if len(hallucinatedIDs) > 0 && logger != nil {
		logger.Warn("reranker hallucinated artifact IDs",
			"user_id", userID,
			"hallucinated_ids", hallucinatedIDs,
			"valid_count", len(validArtifacts),
			"total_returned", len(result.Artifacts),
		)
	}

	return &Result{
		Topics:    result.Topics,
		People:    result.People,
		Artifacts: validArtifacts,
	}
}

// sessionishIDRE matches the malformed artifact IDs the model emits when it
// tries to reference a session file by its marker instead of its number:
// "Artifact:session", "session", "Artifact:Session:1", "Artifact:session_2".
// The optional trailing number is a 1-based index into the session candidates
// (they render first, in order, so it is the model's most plausible referent).
var sessionishIDRE = regexp.MustCompile(`(?i)^(?:artifact[:_ ]?)?session(?:[:_ ]?(\d+))?$`)

// recoverSessionArtifactIDs rewrites session-flavored malformed artifact IDs
// onto real session candidates where the mapping is unambiguous: a bare
// "session" with exactly one session candidate, or an explicit index within
// range. Ambiguous references are left untouched (filterValidArtifacts drops
// them). Returns the possibly-rewritten result and the number of recoveries.
//
// Rationale: dropping loses exactly the file the user just asked about, while
// a wrong recovery merely adds a recently-attached file to context — low harm
// (session candidates are few and ≤ hours old), so we recover narrowly.
func recoverSessionArtifactIDs(
	userID storage.ScopeID,
	result *Result,
	artifactCandidates []ArtifactCandidate,
	logger *slog.Logger,
) (*Result, int) {
	if len(result.Artifacts) == 0 {
		return result, 0
	}
	var sessionCandidates []ArtifactCandidate
	for _, c := range artifactCandidates {
		if c.IsSession {
			sessionCandidates = append(sessionCandidates, c)
		}
	}
	if len(sessionCandidates) == 0 {
		return result, 0
	}

	selected := make(map[int64]bool)
	for _, sel := range result.Artifacts {
		if id, err := parseArtifactID(sel.ID); err == nil {
			selected[id] = true
		}
	}

	recovered := 0
	out := make([]ArtifactSelection, 0, len(result.Artifacts))
	for _, sel := range result.Artifacts {
		if _, err := parseArtifactID(sel.ID); err == nil {
			out = append(out, sel)
			continue
		}
		m := sessionishIDRE.FindStringSubmatch(sel.ID)
		if m == nil {
			out = append(out, sel) // not session-flavored; leave for the normal filter
			continue
		}
		var target *ArtifactCandidate
		switch {
		case m[1] != "":
			if n, err := strconv.Atoi(m[1]); err == nil && n >= 1 && n <= len(sessionCandidates) {
				target = &sessionCandidates[n-1]
			}
		case len(sessionCandidates) == 1:
			target = &sessionCandidates[0]
		}
		if target == nil {
			out = append(out, sel) // ambiguous — dropped downstream with a warn
			continue
		}
		if selected[target.ArtifactID] {
			continue // already selected under its real ID; drop the duplicate
		}
		if logger != nil {
			logger.Warn("recovered session artifact ID",
				"user_id", userID, "raw_id", sel.ID, "artifact_id", target.ArtifactID)
		}
		selected[target.ArtifactID] = true
		sel.ID = fmt.Sprintf("Artifact:%d", target.ArtifactID)
		out = append(out, sel)
		recovered++
	}

	return &Result{Topics: result.Topics, People: result.People, Artifacts: out}, recovered
}

// countSessionKept returns how many of the given artifact selections originated
// from session-injection (IsSession=true on the input candidate). Used to emit
// reranker.model_kept.artifacts.session as a separate span attribute alongside
// model_kept.artifacts so operators can see "of N kept files, K were session".
func countSessionKept(selections []ArtifactSelection, artifactsMap map[int64]ArtifactCandidate) int {
	n := 0
	for _, sel := range selections {
		id, err := parseArtifactID(sel.ID)
		if err != nil {
			continue
		}
		if c, ok := artifactsMap[id]; ok && c.IsSession {
			n++
		}
	}
	return n
}

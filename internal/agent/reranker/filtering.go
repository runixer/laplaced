// Package reranker provides the Reranker agent that uses tool calls to select
// the most relevant topics from vector search candidates.
package reranker

import (
	"log/slog"
)

// filterValidTopics removes hallucinated topic IDs from the result.
// Logs warnings for any IDs that don't exist in the candidate map.
func filterValidTopics(
	userID int64,
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
	userID int64,
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
	userID int64,
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

// Package reranker provides the Reranker agent that uses tool calls to select
// the most relevant topics from vector search candidates.
package reranker

import (
	"log/slog"
	"strconv"

	"github.com/runixer/laplaced/internal/config"
)

// fallbackToVectorTop returns top-N candidates by vector score.
// Used when: LLM timeout, parsing error, no tool calls made.
func fallbackToVectorTop(
	cfg *config.Config,
	candidates []Candidate,
	personCandidates []PersonCandidate,
	artifactCandidates []ArtifactCandidate,
	maxTopics int,
	logger *slog.Logger,
) *Result {
	// Get config values, with defaults for tests (nil cfg)
	var topicsMax, peopleMax, artifactsMax int
	if cfg != nil {
		topicsMax = cfg.Agents.Reranker.Topics.Max
		peopleMax = cfg.Agents.Reranker.People.Max
		artifactsMax = cfg.Agents.Reranker.Artifacts.Max
	}

	if maxTopics <= 0 {
		maxTopics = topicsMax
		if maxTopics <= 0 {
			maxTopics = 5
		}
	}
	if len(candidates) > maxTopics {
		candidates = candidates[:maxTopics]
	}

	topics := make([]TopicSelection, len(candidates))
	for i, c := range candidates {
		topics[i] = TopicSelection{ID: formatTopicID(c.TopicID)}
	}

	// v0.5.1: Include top-N people by vector score as well
	if peopleMax <= 0 {
		peopleMax = 3
	}
	if len(personCandidates) > peopleMax {
		personCandidates = personCandidates[:peopleMax]
	}
	people := make([]PersonSelection, len(personCandidates))
	for i, p := range personCandidates {
		people[i] = PersonSelection{ID: formatPersonID(p.PersonID)}
	}

	// v0.6.0: Include top-N artifacts by vector score as well
	if artifactsMax <= 0 {
		artifactsMax = 3
	}
	if len(artifactCandidates) > artifactsMax {
		artifactCandidates = artifactCandidates[:artifactsMax]
	}
	artifacts := make([]ArtifactSelection, len(artifactCandidates))
	for i, a := range artifactCandidates {
		artifacts[i] = ArtifactSelection{ID: formatArtifactID(a.ArtifactID)}
	}

	if logger != nil {
		logger.Info("reranker fallback to vector top",
			"topics_in", len(candidates),
			"topics_out", len(topics),
			"people_out", len(people),
			"artifacts_out", len(artifacts))
	}

	return &Result{Topics: topics, People: people, Artifacts: artifacts}
}

// fallbackFromState returns top-N from requestedIDs if available.
// Falls through to fallbackToVectorTop if no tool calls were made.
func fallbackFromState(
	cfg *config.Config,
	st *state,
	candidates []Candidate,
	personCandidates []PersonCandidate,
	artifactCandidates []ArtifactCandidate,
	maxTopics int,
	logger *slog.Logger,
) *Result {
	// Get config values, with defaults for tests (nil cfg)
	var topicsMax, peopleMax, artifactsMax int
	if cfg != nil {
		topicsMax = cfg.Agents.Reranker.Topics.Max
		peopleMax = cfg.Agents.Reranker.People.Max
		artifactsMax = cfg.Agents.Reranker.Artifacts.Max
	}

	if maxTopics <= 0 {
		maxTopics = topicsMax
		if maxTopics <= 0 {
			maxTopics = 5
		}
	}

	if len(st.requestedIDs) > 0 {
		ids := st.requestedIDs
		if len(ids) > maxTopics {
			ids = ids[:maxTopics]
		}
		topics := make([]TopicSelection, len(ids))
		for i, id := range ids {
			topics[i] = TopicSelection{ID: formatTopicID(id)}
		}

		// v0.5.1: Include top-N people by vector score
		if peopleMax <= 0 {
			peopleMax = 3
		}
		if len(personCandidates) > peopleMax {
			personCandidates = personCandidates[:peopleMax]
		}
		people := make([]PersonSelection, len(personCandidates))
		for i, p := range personCandidates {
			people[i] = PersonSelection{ID: formatPersonID(p.PersonID)}
		}

		// v0.6.0: Include top-N artifacts by vector score
		if artifactsMax <= 0 {
			artifactsMax = 3
		}
		if len(artifactCandidates) > artifactsMax {
			artifactCandidates = artifactCandidates[:artifactsMax]
		}
		artifacts := make([]ArtifactSelection, len(artifactCandidates))
		for i, a := range artifactCandidates {
			artifacts[i] = ArtifactSelection{ID: formatArtifactID(a.ArtifactID)}
		}

		if logger != nil {
			logger.Info("reranker fallback to requested IDs",
				"requested", len(st.requestedIDs),
				"topics_out", len(topics),
				"people_out", len(people),
				"artifacts_out", len(artifacts))
		}

		return &Result{Topics: topics, People: people, Artifacts: artifacts}
	}

	return fallbackToVectorTop(cfg, candidates, personCandidates, artifactCandidates, maxTopics, logger)
}

// Helper functions for ID formatting

func formatTopicID(id int64) string {
	return formatID("Topic", id)
}

func formatPersonID(id int64) string {
	return formatID("Person", id)
}

func formatArtifactID(id int64) string {
	return formatID("Artifact", id)
}

func formatID(prefix string, id int64) string {
	return prefix + ":" + strconv.FormatInt(id, 10)
}

// Package reranker provides the Reranker agent that uses tool calls to select
// the most relevant topics from vector search candidates.
package reranker

import (
	"log/slog"
	"strconv"

	"github.com/runixer/laplaced/internal/config"
)

// fallbackToVectorTop returns top-N topic candidates by vector score.
// Used when the reranker fails before producing a model selection (LLM
// timeout, parse error, protocol violation, no tool calls made, ...).
//
// People and artifacts are deliberately NOT included: vector search for
// them returns dense clusters of similar items (e.g. 50 near-identical
// small-talk topics around "Проверка связи"), and dumping the top-N by
// raw cosine into the chat prompt produces wildly off-topic context —
// the model never chose them, this fallback was overriding "nothing
// relevant" with vector noise. Topics still get a small backstop (top-N)
// because the chat agent can sanity-check them against the user query;
// arbitrary photos/people loaded as multimodal parts can't be unread.
func fallbackToVectorTop(
	cfg *config.Config,
	candidates []Candidate,
	_ []PersonCandidate,
	_ []ArtifactCandidate,
	maxTopics int,
	logger *slog.Logger,
) *Result {
	var topicsMax int
	if cfg != nil {
		topicsMax = cfg.Agents.Reranker.Topics.Max
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

	if logger != nil {
		logger.Info("reranker fallback to vector top",
			"topics_in", len(candidates),
			"topics_out", len(topics),
			"people_out", 0,
			"artifacts_out", 0)
	}

	return &Result{Topics: topics}
}

// fallbackFromState returns topics from requestedIDs (what the model
// explicitly asked to inspect) if available, otherwise falls through to
// fallbackToVectorTop.
//
// As with fallbackToVectorTop, people and artifacts are deliberately
// dropped — they were never requested by the model. The model's
// "interest signal" is captured in requestedIDs for topics only;
// loading arbitrary people/artifacts by cosine would override that.
func fallbackFromState(
	cfg *config.Config,
	st *state,
	candidates []Candidate,
	personCandidates []PersonCandidate,
	artifactCandidates []ArtifactCandidate,
	maxTopics int,
	logger *slog.Logger,
) *Result {
	var topicsMax int
	if cfg != nil {
		topicsMax = cfg.Agents.Reranker.Topics.Max
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

		if logger != nil {
			logger.Info("reranker fallback to requested IDs",
				"requested", len(st.requestedIDs),
				"topics_out", len(topics),
				"people_out", 0,
				"artifacts_out", 0)
		}

		return &Result{Topics: topics}
	}

	return fallbackToVectorTop(cfg, candidates, personCandidates, artifactCandidates, maxTopics, logger)
}

// formatTopicID renders a numeric topic id as "Topic:N", matching the
// candidate-list format the reranker prompt and parser expect.
func formatTopicID(id int64) string {
	return "Topic:" + strconv.FormatInt(id, 10)
}

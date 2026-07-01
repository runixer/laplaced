// Package replay re-runs captured reranker (and, in future, other agent)
// invocations through the production agent code, using the snapshotted
// database as the back-end. Output is a side-by-side comparison of the
// original prod output (from the trace span) vs. the replay output.
//
// Why generic: the runner is agent-agnostic — it iterates trace files, calls
// a per-agent buildRequest function, dispatches to the agent, and dumps a
// uniform JSON result. Each new agent (enricher, laplace, etc.) plugs in
// its own reconstructor without touching the runner.
package replay

import (
	"time"

	"github.com/runixer/laplaced/internal/storage"
)

// AgentOutput is the comparable slice of an agent invocation: which entities
// the agent picked, with what reasons, and at what cost. Both Original (from
// the captured span) and Replay (freshly produced by the production agent
// code) populate this same shape so a downstream eval-rerank step can diff
// them mechanically.
//
// SelectedTopics / SelectedPeople / SelectedArtifacts are nil when the agent
// type doesn't have that concept; reasons may be nil if the path didn't
// emit them (typical of fallback paths).
type AgentOutput struct {
	SelectedTopics    []int          `json:"selected_topics,omitempty"`
	SelectedPeople    []int          `json:"selected_people,omitempty"`
	SelectedArtifacts []int          `json:"selected_artifacts,omitempty"`
	TopicReasons      map[int]string `json:"topic_reasons,omitempty"`
	PersonReasons     map[int]string `json:"person_reasons,omitempty"`
	ArtifactReasons   map[int]string `json:"artifact_reasons,omitempty"`
	// InvalidIDs collects selection IDs the reranker returned that failed
	// numeric parsing (e.g. "Artifact:session") and were therefore dropped.
	InvalidIDs     []string          `json:"invalid_ids,omitempty"`
	FallbackReason string            `json:"fallback_reason,omitempty"`
	CostUSD        float64           `json:"cost_usd,omitempty"`
	LatencyMs      int64             `json:"latency_ms,omitempty"`
	Extra          map[string]string `json:"extra,omitempty"`
}

// SkippedLookup logs a candidate that the reconstructor failed to fetch from
// the snapshot DB — typically because the entity was deleted, merged, or
// chunking-split between snapshot and replay. The runner records these
// without aborting, so a partial replay still ships meaningful results.
type SkippedLookup struct {
	Type   string `json:"type"` // "topic", "person", "artifact"
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

// ReplayResult is one trace's full replay record, written to
// `<snapshot>/replay/<variant>/<trace_id>.json`.
type ReplayResult struct {
	TraceID string          `json:"trace_id"`
	UserID  storage.ScopeID `json:"user_id"`
	Agent   string          `json:"agent"`   // "reranker", "enricher", ...
	Variant string          `json:"variant"` // "baseline", "no_match_patch", ...
	RanAt   time.Time       `json:"ran_at"`
	Skipped []SkippedLookup `json:"skipped_lookups,omitempty"`

	// Inputs that were fed to the agent. Per-agent reconstructors fill the
	// fields that make sense for them — leaving the rest as zero values.
	Input ReplayInput `json:"input"`

	// Captured output from the original prod run (parsed from the trace span).
	Original AgentOutput `json:"original_output"`

	// Output of running the production agent code over the reconstructed input.
	Replay AgentOutput `json:"replay_output"`

	// ReplayError records why a replay invocation could not produce output.
	// When set, Replay is the zero value. The trace is still written so the
	// summary can count failures.
	ReplayError string `json:"replay_error,omitempty"`
}

// ReplayInput is a thin description of what the agent received. Only fields
// that are common across agents live here; per-agent specifics belong in
// Extra (string-only to keep the JSON debuggable).
type ReplayInput struct {
	RawQuery           string         `json:"raw_query,omitempty"`
	EnrichedQuery      string         `json:"enriched_query,omitempty"`
	TopicCandidates    []CandidateRef `json:"topic_candidates,omitempty"`
	PeopleCandidates   []CandidateRef `json:"people_candidates,omitempty"`
	ArtifactCandidates []CandidateRef `json:"artifact_candidates,omitempty"`
}

// CandidateRef is the minimal id+score pair used in ReplayInput. Full content
// lives in the snapshot DB; replay results stay slim.
type CandidateRef struct {
	ID         int64   `json:"id"`
	Similarity float64 `json:"similarity,omitempty"`
}

// Summary aggregates per-trace results across one replay run. Written to
// `<snapshot>/replay/<variant>/summary.json`.
type Summary struct {
	Agent               string    `json:"agent"`
	Variant             string    `json:"variant"`
	StartedAt           time.Time `json:"started_at"`
	FinishedAt          time.Time `json:"finished_at"`
	TraceCount          int       `json:"trace_count"`
	ReplayedOK          int       `json:"replayed_ok"`
	ReplayedErr         int       `json:"replayed_err"`
	TotalCostUSD        float64   `json:"total_cost_usd"`
	TotalSkippedLookups int       `json:"total_skipped_lookups"`
}

package replay

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/runixer/laplaced/cmd/testbot/snapshot"
	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/reranker"
	"github.com/runixer/laplaced/internal/storage"
)

// RerankerStore is the slice of storage.Store the reconstructor needs.
// Defined as an interface so tests can stub the lookups without spinning up
// a real DB.
type RerankerStore interface {
	GetTopicsByIDs(userID storage.ScopeID, ids []int64) ([]storage.Topic, error)
	GetPeopleByIDs(userID storage.ScopeID, ids []int64) ([]storage.Person, error)
	GetArtifactsByIDs(userID storage.ScopeID, artifactIDs []int64) ([]storage.Artifact, error)
}

// SharedContextLoader produces a SharedContext for the replay user. In the
// Cobra command we pass services.ContextService.Load; tests can stub.
type SharedContextLoader func(ctx context.Context, userID storage.ScopeID) *agent.SharedContext

// NewRerankerBuilder constructs a BuildRequestFunc that turns a captured
// reranker.Execute span into an agent.Request.
//
// The reconstructor:
//  1. Parses the span via snapshot.ExtractRerankerSpan.
//  2. Delegates to BuildRerankerRequest, which re-parses the candidate event
//     bodies, looks IDs up in the snapshot DB, and assembles the request.
//
// Split this way so tests can drive BuildRerankerRequest directly with a
// hand-built RerankerSpanData and skip the JSON synthesis.
func NewRerankerBuilder(store RerankerStore, loadShared SharedContextLoader) BuildRequestFunc {
	return func(ctx context.Context, traceID string, traceJSON []byte) (BuildResult, error) {
		span, err := snapshot.ExtractRerankerSpan(traceID, traceJSON)
		if err != nil {
			return BuildResult{}, fmt.Errorf("extract span: %w", err)
		}
		return BuildRerankerRequest(ctx, span, store, loadShared)
	}
}

// BuildRerankerRequest reconstructs a reranker request from an already-parsed
// span. Same logic as the wrapper above — separated for testability.
func BuildRerankerRequest(
	ctx context.Context,
	span *snapshot.RerankerSpanData,
	store RerankerStore,
	loadShared SharedContextLoader,
) (BuildResult, error) {
	if span.UserID == "" {
		return BuildResult{}, fmt.Errorf("trace has no user.id attribute")
	}

	topicLines := snapshot.ParseCandidates(span.CandidatesInput)
	personLines := snapshot.ParsePersonCandidates(span.PeopleCandidatesInput)
	artifactLines := snapshot.ParseArtifactCandidates(span.ArtifactCandidatesInput)

	topics, missingTopics, err := loadTopics(store, span.UserID, topicLines)
	if err != nil {
		return BuildResult{}, err
	}
	people, missingPeople, err := loadPeople(store, span.UserID, personLines)
	if err != nil {
		return BuildResult{}, err
	}
	artifacts, missingArts, err := loadArtifacts(store, span.UserID, artifactLines)
	if err != nil {
		return BuildResult{}, err
	}

	var skipped []SkippedLookup
	for _, id := range missingTopics {
		skipped = append(skipped, SkippedLookup{Type: "topic", ID: id, Reason: "not in snapshot DB"})
	}
	for _, id := range missingPeople {
		skipped = append(skipped, SkippedLookup{Type: "person", ID: id, Reason: "not in snapshot DB"})
	}
	for _, id := range missingArts {
		skipped = append(skipped, SkippedLookup{Type: "artifact", ID: id, Reason: "not in snapshot DB"})
	}

	// Faithful path: span captured the SharedContext bodies that landed in
	// the system prompt at trace time. Use those verbatim — replay sees the
	// exact frozen state, not today's DB.
	//
	// Fallback path (round-2 and earlier): events absent. Fall back to the
	// loader (typically services.ContextService.Load on the snapshot DB).
	// Drift-prone but better than empty.
	var shared *agent.SharedContext
	if span.UserProfile != "" || span.RecentTopics != "" {
		shared = &agent.SharedContext{
			UserID:       span.UserID,
			Profile:      span.UserProfile,
			RecentTopics: span.RecentTopics,
		}
	} else if loadShared != nil {
		shared = loadShared(ctx, span.UserID)
	}
	if shared == nil {
		shared = &agent.SharedContext{UserID: span.UserID}
	}

	req := &agent.Request{
		Shared: shared,
		Query:  span.RawQuery,
		Params: map[string]any{
			reranker.ParamCandidates:          topics,
			reranker.ParamPersonCandidates:    people,
			reranker.ParamArtifactCandidates:  artifacts,
			reranker.ParamContextualizedQuery: span.EnrichedQuery,
			reranker.ParamOriginalQuery:       span.RawQuery,
			reranker.ParamCurrentMessages:     "", // not captured in trace events
		},
	}

	return BuildResult{
		UserID:   span.UserID,
		Request:  req,
		Input:    rerankerInput(span, topicLines, personLines, artifactLines),
		Original: rerankerOriginal(span),
		Skipped:  skipped,
	}, nil
}

// ExtractRerankerOutput pulls the comparable AgentOutput out of an
// agent.Response produced by reranker.Execute. The reranker stores its
// structured result in Response.Structured as *reranker.Result.
func ExtractRerankerOutput(resp *agent.Response) AgentOutput {
	out := AgentOutput{
		TopicReasons:    map[int]string{},
		PersonReasons:   map[int]string{},
		ArtifactReasons: map[int]string{},
	}
	if resp == nil {
		return out
	}
	if resp.Tokens.Cost != nil {
		out.CostUSD = *resp.Tokens.Cost
	}
	out.LatencyMs = resp.Duration.Milliseconds()

	r, ok := resp.Structured.(*reranker.Result)
	if !ok || r == nil {
		return out
	}
	for _, ts := range r.Topics {
		id, err := ts.GetNumericID()
		if err != nil {
			continue
		}
		out.SelectedTopics = append(out.SelectedTopics, int(id))
		if ts.Reason != "" {
			out.TopicReasons[int(id)] = ts.Reason
		}
	}
	for _, ps := range r.People {
		id, err := ps.GetNumericID()
		if err != nil {
			continue
		}
		out.SelectedPeople = append(out.SelectedPeople, int(id))
		if ps.Reason != "" {
			out.PersonReasons[int(id)] = ps.Reason
		}
	}
	for _, as := range r.Artifacts {
		id, err := as.GetNumericID()
		if err != nil {
			continue
		}
		out.SelectedArtifacts = append(out.SelectedArtifacts, int(id))
		if as.Reason != "" {
			out.ArtifactReasons[int(id)] = as.Reason
		}
	}
	// reranker.Result doesn't surface the fallback_reason in agent.Response;
	// it lives only on the OTel trace span. Replay diffs comparing IDs work
	// fine without it; the Original.FallbackReason (from the captured span)
	// is the only side that can observe it.
	return out
}

func rerankerOriginal(span *snapshot.RerankerSpanData) AgentOutput {
	return AgentOutput{
		SelectedTopics:    span.SelectedTopics,
		SelectedPeople:    span.SelectedPeople,
		SelectedArtifacts: span.SelectedArtifacts,
		TopicReasons:      span.SelectedReasons,
		PersonReasons:     span.SelectedReasonsP,
		ArtifactReasons:   span.SelectedReasonsA,
		FallbackReason:    span.FallbackReason,
		CostUSD:           span.CostUSD,
		LatencyMs:         span.DurationMs,
	}
}

func rerankerInput(
	span *snapshot.RerankerSpanData,
	topicLines []snapshot.CandidateLine,
	personLines []snapshot.PersonCandidateLine,
	artifactLines []snapshot.ArtifactCandidateLine,
) ReplayInput {
	in := ReplayInput{
		RawQuery:      span.RawQuery,
		EnrichedQuery: span.EnrichedQuery,
	}
	for _, c := range topicLines {
		in.TopicCandidates = append(in.TopicCandidates, CandidateRef{ID: int64(c.ID), Similarity: c.Similarity})
	}
	for _, p := range personLines {
		in.PeopleCandidates = append(in.PeopleCandidates, CandidateRef{ID: int64(p.ID)})
	}
	for _, a := range artifactLines {
		in.ArtifactCandidates = append(in.ArtifactCandidates, CandidateRef{ID: int64(a.ID), Similarity: a.Similarity})
	}
	return in
}

// loadTopics looks up the captured topic IDs in the snapshot DB and returns
// fully-formed reranker.Candidate values along with the IDs that weren't
// found (chunking-split, deleted, etc).
func loadTopics(store RerankerStore, userID storage.ScopeID, lines []snapshot.CandidateLine) ([]reranker.Candidate, []int64, error) {
	if len(lines) == 0 {
		return nil, nil, nil
	}
	ids := make([]int64, 0, len(lines))
	for _, l := range lines {
		ids = append(ids, int64(l.ID))
	}
	topics, err := store.GetTopicsByIDs(userID, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("GetTopicsByIDs: %w", err)
	}
	byID := make(map[int64]storage.Topic, len(topics))
	for _, t := range topics {
		byID[t.ID] = t
	}
	out := make([]reranker.Candidate, 0, len(lines))
	var missing []int64
	for _, l := range lines {
		t, ok := byID[int64(l.ID)]
		if !ok {
			missing = append(missing, int64(l.ID))
			continue
		}
		msgCount := int(t.EndMsgID - t.StartMsgID + 1)
		if msgCount < 0 {
			msgCount = 0
		}
		out = append(out, reranker.Candidate{
			TopicID:      int64(l.ID),
			Score:        float32(l.Similarity),
			Topic:        t,
			MessageCount: msgCount,
			SizeChars:    t.SizeChars,
		})
	}
	return out, missing, nil
}

func loadPeople(store RerankerStore, userID storage.ScopeID, lines []snapshot.PersonCandidateLine) ([]reranker.PersonCandidate, []int64, error) {
	if len(lines) == 0 {
		return nil, nil, nil
	}
	ids := make([]int64, 0, len(lines))
	for _, l := range lines {
		ids = append(ids, int64(l.ID))
	}
	people, err := store.GetPeopleByIDs(userID, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("GetPeopleByIDs: %w", err)
	}
	byID := make(map[int64]storage.Person, len(people))
	for _, p := range people {
		byID[p.ID] = p
	}
	out := make([]reranker.PersonCandidate, 0, len(lines))
	var missing []int64
	for _, l := range lines {
		p, ok := byID[int64(l.ID)]
		if !ok {
			missing = append(missing, int64(l.ID))
			continue
		}
		// Score not captured in the people event body — leave at 0. Reranker
		// uses Score only for fallback ordering, so the impact is limited to
		// fallback paths in replay (rare in success-path traces).
		out = append(out, reranker.PersonCandidate{
			PersonID: int64(l.ID),
			Person:   p,
		})
	}
	return out, missing, nil
}

func loadArtifacts(store RerankerStore, userID storage.ScopeID, lines []snapshot.ArtifactCandidateLine) ([]reranker.ArtifactCandidate, []int64, error) {
	if len(lines) == 0 {
		return nil, nil, nil
	}
	ids := make([]int64, 0, len(lines))
	for _, l := range lines {
		ids = append(ids, int64(l.ID))
	}
	arts, err := store.GetArtifactsByIDs(userID, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("GetArtifactsByIDs: %w", err)
	}
	byID := make(map[int64]storage.Artifact, len(arts))
	for _, a := range arts {
		byID[a.ID] = a
	}
	out := make([]reranker.ArtifactCandidate, 0, len(lines))
	var missing []int64
	for _, l := range lines {
		a, ok := byID[int64(l.ID)]
		if !ok {
			missing = append(missing, int64(l.ID))
			continue
		}
		summary := ""
		if a.Summary != nil {
			summary = *a.Summary
		}
		out = append(out, reranker.ArtifactCandidate{
			ArtifactID:   int64(l.ID),
			Score:        float32(l.Similarity),
			FileType:     a.FileType,
			OriginalName: a.OriginalName,
			Summary:      summary,
			Keywords:     parseJSONStringArray(a.Keywords),
			Entities:     parseJSONStringArray(a.Entities),
			RAGHints:     parseJSONStringArray(a.RAGHints),
		})
	}
	return out, missing, nil
}

// parseJSONStringArray parses a *string field that storage emits as a JSON
// array. Returns nil for nil/blank/invalid — same lenient behaviour as the
// production SearchArtifactsBySummary path.
func parseJSONStringArray(s *string) []string {
	if s == nil || *s == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(*s), &out); err != nil {
		return nil
	}
	return out
}

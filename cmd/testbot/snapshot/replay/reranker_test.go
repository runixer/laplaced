package replay

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/runixer/laplaced/cmd/testbot/snapshot"
	"github.com/runixer/laplaced/internal/agent"
	"github.com/runixer/laplaced/internal/agent/reranker"
	"github.com/runixer/laplaced/internal/storage"
)

// stubStore is a tiny RerankerStore that returns rows by ID from in-memory
// maps. Lookups for unknown IDs are silently absent — same shape as the
// production sqlite getters which return only the rows they find.
type stubStore struct {
	topics    map[int64]storage.Topic
	people    map[int64]storage.Person
	artifacts map[int64]storage.Artifact
}

func (s *stubStore) GetTopicsByIDs(_ int64, ids []int64) ([]storage.Topic, error) {
	out := make([]storage.Topic, 0, len(ids))
	for _, id := range ids {
		if t, ok := s.topics[id]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *stubStore) GetPeopleByIDs(_ int64, ids []int64) ([]storage.Person, error) {
	out := make([]storage.Person, 0, len(ids))
	for _, id := range ids {
		if p, ok := s.people[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *stubStore) GetArtifactsByIDs(_ int64, ids []int64) ([]storage.Artifact, error) {
	out := make([]storage.Artifact, 0, len(ids))
	for _, id := range ids {
		if a, ok := s.artifacts[id]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

func TestBuildRerankerRequest_HappyPath(t *testing.T) {
	span := &snapshot.RerankerSpanData{
		TraceID:       "t1",
		UserID:        42,
		RawQuery:      "что про H100?",
		EnrichedQuery: "vLLM H100 production",
		CandidatesInput: `[Topic:9695] (0.81) 2026-04-07 | 2 msgs, ~4K chars | sglang vs vLLM
[Topic:4815] (0.79) 2025-12-24 | 6 msgs, ~6K chars | 2xH100 NVL`,
		PeopleCandidatesInput:   `[Person:304] Наиля [Work_Outer]: short bio`,
		ArtifactCandidatesInput: `[Artifact:978] (0.74) image: "photo.jpg" | k1, k2 | Entities: e1 | hand of mother`,
		SelectedTopics:          []int{4815},
		SelectedReasons:         map[int]string{4815: "prior history"},
		FallbackReason:          "",
		CostUSD:                 0.0123,
		DurationMs:              1500,
	}

	keywordsJSON := `["k1","k2"]`
	entitiesJSON := `["Mom","Foo"]`
	store := &stubStore{
		topics: map[int64]storage.Topic{
			9695: {ID: 9695, UserID: 42, Summary: "sglang vs vLLM", StartMsgID: 100, EndMsgID: 101, SizeChars: 4000},
			4815: {ID: 4815, UserID: 42, Summary: "H100 NVL", StartMsgID: 200, EndMsgID: 205, SizeChars: 6000},
		},
		people: map[int64]storage.Person{
			304: {ID: 304, UserID: 42, DisplayName: "Наиля"},
		},
		artifacts: map[int64]storage.Artifact{
			978: {ID: 978, UserID: 42, FileType: "image", OriginalName: "photo.jpg",
				Summary:  ptrString("hand of mother"),
				Keywords: ptrString(keywordsJSON),
				Entities: ptrString(entitiesJSON)},
		},
	}

	br, err := BuildRerankerRequest(context.Background(), span, store, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(42), br.UserID)

	// Topic candidates wired through with similarity from event body and
	// MessageCount derived from the topic's StartMsgID..EndMsgID range.
	cands := br.Request.Params[reranker.ParamCandidates].([]reranker.Candidate)
	require.Len(t, cands, 2)
	assert.Equal(t, int64(9695), cands[0].TopicID)
	assert.InDelta(t, 0.81, cands[0].Score, 1e-6)
	assert.Equal(t, 2, cands[0].MessageCount, "MessageCount = EndMsgID - StartMsgID + 1")
	assert.Equal(t, 4000, cands[0].SizeChars)

	// People: similarity not captured in event body — Score stays 0.
	people := br.Request.Params[reranker.ParamPersonCandidates].([]reranker.PersonCandidate)
	require.Len(t, people, 1)
	assert.Equal(t, int64(304), people[0].PersonID)
	assert.Equal(t, float32(0), people[0].Score, "people score isn't in event body")

	// Artifacts: keywords and entities parsed from JSON-encoded fields.
	arts := br.Request.Params[reranker.ParamArtifactCandidates].([]reranker.ArtifactCandidate)
	require.Len(t, arts, 1)
	assert.Equal(t, int64(978), arts[0].ArtifactID)
	assert.InDelta(t, 0.74, arts[0].Score, 1e-6)
	assert.Equal(t, []string{"k1", "k2"}, arts[0].Keywords)
	assert.Equal(t, []string{"Mom", "Foo"}, arts[0].Entities)
	assert.Equal(t, "hand of mother", arts[0].Summary)

	// Original output mirrors what we put on the span fixture.
	assert.Equal(t, []int{4815}, br.Original.SelectedTopics)
	assert.Equal(t, "prior history", br.Original.TopicReasons[4815])
	assert.Equal(t, 0.0123, br.Original.CostUSD)
	assert.Equal(t, int64(1500), br.Original.LatencyMs)

	// Input snapshot keeps just id+score for slim JSON.
	assert.Len(t, br.Input.TopicCandidates, 2)
	assert.Equal(t, int64(9695), br.Input.TopicCandidates[0].ID)
	assert.InDelta(t, 0.81, br.Input.TopicCandidates[0].Similarity, 1e-6)

	// SharedContext fallback: nil loader → minimal SharedContext{UserID}.
	require.NotNil(t, br.Request.Shared)
	assert.Equal(t, int64(42), br.Request.Shared.UserID)
}

func TestBuildRerankerRequest_MissingFromDB(t *testing.T) {
	// Reranker saw 3 topic candidates at trace time; only 1 still exists in
	// the snapshot DB (others chunking-split or deleted). The reconstructor
	// must drop the missing IDs from the request and surface them via Skipped.
	span := &snapshot.RerankerSpanData{
		TraceID: "t2",
		UserID:  42,
		CandidatesInput: `[Topic:9695] (0.81) 2026-04-07 | 2 msgs, ~4K chars | exists
[Topic:9999] (0.80) 2026-04-07 | 2 msgs, ~3K chars | gone-1
[Topic:8888] (0.75) 2026-04-07 | 2 msgs, ~3K chars | gone-2`,
	}
	store := &stubStore{
		topics: map[int64]storage.Topic{
			9695: {ID: 9695, UserID: 42, StartMsgID: 1, EndMsgID: 2, SizeChars: 4000},
		},
	}

	br, err := BuildRerankerRequest(context.Background(), span, store, nil)
	require.NoError(t, err)

	cands := br.Request.Params[reranker.ParamCandidates].([]reranker.Candidate)
	require.Len(t, cands, 1, "only the present topic survives")
	assert.Equal(t, int64(9695), cands[0].TopicID)

	require.Len(t, br.Skipped, 2, "two missing IDs surfaced")
	skippedIDs := []int64{br.Skipped[0].ID, br.Skipped[1].ID}
	assert.ElementsMatch(t, []int64{9999, 8888}, skippedIDs)
	assert.Equal(t, "topic", br.Skipped[0].Type)
}

func TestBuildRerankerRequest_PrefersSpanSharedContext(t *testing.T) {
	// When the span captured user_profile / recent_topics events (post-2026-04-29
	// instrumentation), faithful replay must use those verbatim and skip the
	// loader entirely — no drift through today's DB.
	span := &snapshot.RerankerSpanData{
		TraceID:      "tspan",
		UserID:       42,
		UserProfile:  "<user_profile>\n- frozen at trace time\n</user_profile>",
		RecentTopics: "<recent_topics>\n- frozen too\n</recent_topics>",
	}
	loaderCalled := false
	loader := func(_ context.Context, _ int64) *agent.SharedContext {
		loaderCalled = true
		return &agent.SharedContext{Profile: "today's drift", RecentTopics: "today's drift"}
	}

	br, err := BuildRerankerRequest(context.Background(), span, &stubStore{}, loader)
	require.NoError(t, err)
	assert.False(t, loaderCalled, "loader must NOT be called when span has shared-context events")
	assert.Equal(t, span.UserProfile, br.Request.Shared.Profile)
	assert.Equal(t, span.RecentTopics, br.Request.Shared.RecentTopics)
}

func TestBuildRerankerRequest_FallsBackToLoaderForOldTraces(t *testing.T) {
	// Pre-2026-04-29 traces have no shared-context events. Fall back to the
	// loader (snapshot DB) and accept the drift.
	span := &snapshot.RerankerSpanData{TraceID: "told", UserID: 42}
	loader := func(_ context.Context, _ int64) *agent.SharedContext {
		return &agent.SharedContext{Profile: "from loader", RecentTopics: "from loader"}
	}

	br, err := BuildRerankerRequest(context.Background(), span, &stubStore{}, loader)
	require.NoError(t, err)
	assert.Equal(t, "from loader", br.Request.Shared.Profile)
}

func TestBuildRerankerRequest_RejectsZeroUserID(t *testing.T) {
	// Defensive: a trace with no user.id attribute is corrupt and we don't
	// want to ship it to a real DB / agent — fail fast.
	span := &snapshot.RerankerSpanData{TraceID: "x", UserID: 0}
	_, err := BuildRerankerRequest(context.Background(), span, &stubStore{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user.id")
}

func TestExtractRerankerOutput_HappyPath(t *testing.T) {
	cost := 0.05
	resp := &agent.Response{
		Structured: &reranker.Result{
			Topics: []reranker.TopicSelection{
				{ID: "Topic:42", Reason: "direct match"},
				{ID: "Topic:18", Reason: ""},
			},
			People: []reranker.PersonSelection{
				{ID: "Person:99", Reason: "named in query"},
			},
			Artifacts: []reranker.ArtifactSelection{
				{ID: "Artifact:7"},
			},
		},
		Tokens: agent.TokenUsage{Cost: &cost},
	}

	out := ExtractRerankerOutput(resp)
	assert.Equal(t, []int{42, 18}, out.SelectedTopics)
	assert.Equal(t, "direct match", out.TopicReasons[42])
	_, has18 := out.TopicReasons[18]
	assert.False(t, has18, "blank reason not stored")

	assert.Equal(t, []int{99}, out.SelectedPeople)
	assert.Equal(t, []int{7}, out.SelectedArtifacts)
	assert.Equal(t, 0.05, out.CostUSD)
}

func TestExtractRerankerOutput_NilOrWrongStructured(t *testing.T) {
	out := ExtractRerankerOutput(nil)
	assert.Empty(t, out.SelectedTopics)

	// Wrong type in Structured — must not panic, returns empty selections.
	out = ExtractRerankerOutput(&agent.Response{Structured: "not a Result"})
	assert.Empty(t, out.SelectedTopics)
}

func ptrString(s string) *string { return &s }
